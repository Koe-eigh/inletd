package main

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"
)

type fakeRouter func(Event) (*ExecutionSpec, error)

func (f fakeRouter) Route(e Event) (*ExecutionSpec, error) { return f(e) }

func TestPollDispatchesWithFakeRouter(t *testing.T) {
	e := event("42", "IssuesEvent", "opened", 42, "Requested")
	source := &fakeEventSource{batches: []EventBatch{{Events: []Event{e}}}}
	store := &memoryStateStore{}
	calls := 0
	r := &Runner{
		Config: Config{Repository: "acme/app"},
		Source: source,
		Router: fakeRouter(func(got Event) (*ExecutionSpec, error) {
			calls++
			if got.ID != e.ID {
				t.Fatalf("routed event %s, want %s", got.ID, e.ID)
			}
			return &ExecutionSpec{Rule: Rule{Prompt: "fake"}, Values: eventValues{action: "opened", number: 42, url: "https://example.com/42"}}, nil
		}),
		AgentRunner: fakeAgentRunner(func(_ context.Context, got Event, rule Rule, values eventValues) error {
			if got.ID != e.ID || rule.Prompt != "fake" || values.number != 42 || store.state.Jobs[e.ID].Status != jobRunning {
				t.Fatalf("agent received event=%+v rule=%+v values=%+v state=%+v", got, rule, values, store.state)
			}
			return nil
		}),
		Store:  store,
		State:  State{Initialized: true},
		Logger: log.New(io.Discard, "", 0),
	}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || r.State.Jobs[e.ID].Status != jobCompleted || r.State.Jobs[e.ID].URL != "https://example.com/42" || source.commits != 1 {
		t.Fatalf("calls=%d state=%+v commits=%d", calls, r.State, source.commits)
	}
}

func TestPendingJobReplaysThroughRouter(t *testing.T) {
	e := event("42", "IssuesEvent", "opened", 42, "Requested")
	for _, tc := range []struct {
		name  string
		match bool
	}{
		{name: "matched", match: true},
		{name: "configuration changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &fakeEventSource{batches: []EventBatch{{}}}
			store := &memoryStateStore{}
			calls := 0
			r := &Runner{
				Config: Config{Repository: "acme/app"},
				Source: source,
				Router: fakeRouter(func(got Event) (*ExecutionSpec, error) {
					calls++
					if got.ID != e.ID {
						t.Fatalf("routed event %s, want %s", got.ID, e.ID)
					}
					if !tc.match {
						return nil, nil
					}
					return &ExecutionSpec{Rule: Rule{Prompt: "current"}, Values: eventValues{number: 42}}, nil
				}),
				AgentRunner: fakeAgentRunner(func(_ context.Context, _ Event, rule Rule, _ eventValues) error {
					if rule.Prompt != "current" {
						t.Fatalf("rule prompt = %q", rule.Prompt)
					}
					return nil
				}),
				Store:  store,
				State:  State{Initialized: true, Jobs: map[string]Job{e.ID: {Status: jobPending, Event: &e}}},
				Logger: log.New(io.Discard, "", 0),
			}
			err := r.Poll(context.Background(), false)
			if tc.match {
				if err != nil || r.State.Jobs[e.ID].Status != jobCompleted || source.fetches != 1 {
					t.Fatalf("poll error=%v state=%+v fetches=%d", err, r.State, source.fetches)
				}
			} else if err == nil || !strings.Contains(err.Error(), "no longer matches") || r.State.Jobs[e.ID].Status != jobPending || source.fetches != 0 {
				t.Fatalf("poll error=%v state=%+v fetches=%d", err, r.State, source.fetches)
			}
			if calls != 1 {
				t.Fatalf("router calls = %d, want 1", calls)
			}
		})
	}
}
