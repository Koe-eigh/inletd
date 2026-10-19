package main

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"
)

type fakeEventSource struct {
	batches []EventBatch
	fetches int
	commits int
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
	r := &Runner{
		Config: Config{Repository: "acme/app", StateFile: filepath.Join(t.TempDir(), "state.json"), Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}}}},
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
}

func TestPollDoesNotCommitSourceAfterPersistenceFailure(t *testing.T) {
	source := &fakeEventSource{batches: []EventBatch{{Events: []Event{event("1", "IssuesEvent", "opened", 1, "Old")}}}}
	r := &Runner{
		Config: Config{StateFile: filepath.Join(t.TempDir(), "state.json")},
		Source: source,
		Logger: log.New(io.Discard, "", 0),
		save:   func(string, State) error { return errors.New("disk failed") },
	}
	if err := r.Poll(context.Background(), false); err == nil || source.commits != 0 {
		t.Fatalf("poll error = %v, commits = %d", err, source.commits)
	}
}
