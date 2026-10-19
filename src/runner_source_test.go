package main

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"
)

type fakeEventSource struct {
	batches []EventBatch
	fetches int
	commits int
}

type memoryStateStore struct {
	state State
	fail  func(State) error
}

func (s *memoryStateStore) Load() (State, error) { return s.state, nil }

func (s *memoryStateStore) Save(state State) error {
	if s.fail != nil {
		if err := s.fail(state); err != nil {
			return err
		}
	}
	s.state = state
	return nil
}

type failingStateStore struct {
	base StateStore
	fail func(State) error
}

func (s failingStateStore) Load() (State, error) { return s.base.Load() }

func (s failingStateStore) Save(state State) error {
	if err := s.fail(state); err != nil {
		return err
	}
	return s.base.Save(state)
}

func (s *fakeEventSource) Fetch(context.Context) (EventBatch, error) {
	if s.fetches >= len(s.batches) {
		return EventBatch{}, errors.New("unexpected fetch")
	}
	batch := s.batches[s.fetches]
	s.fetches++
	return batch, nil
}

func (s *fakeEventSource) Commit() { s.commits++ }

func TestPollWithFakeEventSource(t *testing.T) {
	old := event("1", "IssuesEvent", "opened", 1, "Old")
	newEvent := event("2", "IssuesEvent", "opened", 2, "New")
	otherRepo := event("3", "IssuesEvent", "opened", 3, "Other")
	otherRepo.Repo.Name = "other/repo"
	source := &fakeEventSource{batches: []EventBatch{
		{Events: []Event{old}, PollInterval: 2 * time.Minute},
		{Events: []Event{otherRepo, newEvent, old}},
	}}
	var dispatched []string
	store := &memoryStateStore{}
	r := &Runner{
		Config: Config{Repository: "acme/app", Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}}}},
		Store:  store,
		Source: source,
		AgentRunner: fakeAgentRunner(func(_ context.Context, e Event, _ Rule, _ eventValues) error {
			dispatched = append(dispatched, e.ID)
			return nil
		}),
		Logger: log.New(io.Discard, "", 0),
	}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 0 || !r.State.Initialized || r.State.Cursor != "1" || source.commits != 1 {
		t.Fatalf("initial poll: dispatched=%v state=%+v commits=%d", dispatched, r.State, source.commits)
	}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 1 || dispatched[0] != "2" || r.State.Cursor != "3" || r.PollInterval != 2*time.Minute || source.commits != 2 {
		t.Fatalf("new poll: dispatched=%v state=%+v interval=%s commits=%d", dispatched, r.State, r.PollInterval, source.commits)
	}
	restarted := &Runner{Config: r.Config, Store: store, Source: &fakeEventSource{batches: []EventBatch{{Events: []Event{otherRepo, newEvent, old}}}}, AgentRunner: r.AgentRunner, Logger: r.Logger}
	var err error
	restarted.State, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Poll(context.Background(), false); err != nil || len(dispatched) != 1 {
		t.Fatalf("restart poll error = %v, dispatched = %v", err, dispatched)
	}
}

func TestPollDoesNotCommitSourceAfterPersistenceFailure(t *testing.T) {
	source := &fakeEventSource{batches: []EventBatch{{Events: []Event{event("1", "IssuesEvent", "opened", 1, "Old")}}}}
	store := &memoryStateStore{fail: func(State) error { return errors.New("disk failed") }}
	r := &Runner{
		Store:  store,
		Source: source,
		Logger: log.New(io.Discard, "", 0),
	}
	if err := r.Poll(context.Background(), false); err == nil || source.commits != 0 || r.State.Initialized || store.state.Initialized {
		t.Fatalf("poll error = %v, commits = %d, state = %+v, stored = %+v", err, source.commits, r.State, store.state)
	}
}
