package main

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeAgentRunner func(context.Context, Event, Rule, eventValues) error

func (f fakeAgentRunner) Run(ctx context.Context, e Event, rule Rule, values eventValues) error {
	return f(ctx, e, rule, values)
}

func TestRunJobWithInjectedAgentRunner(t *testing.T) {
	agentErr := errors.New("fake agent failed")
	for _, tc := range []struct {
		name       string
		result     error
		wantStatus string
		wantSeen   bool
	}{
		{name: "success", wantStatus: jobCompleted, wantSeen: true},
		{name: "failure", result: agentErr, wantStatus: jobRunning},
		{name: "cancellation", result: context.Canceled, wantStatus: jobRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateFile := filepath.Join(t.TempDir(), "state.json")
			e := event("42", "IssuesEvent", "opened", 42, "Fake")
			job := Job{Status: jobPending, Event: &e}
			rule := Rule{Command: []string{"missing-agent-binary"}, Prompt: "Issue {number}"}
			values := eventValues{number: 42, title: "Fake"}
			calls := 0
			r := &Runner{Config: Config{StateFile: stateFile}, Logger: log.New(io.Discard, "", 0)}
			r.AgentRunner = fakeAgentRunner(func(ctx context.Context, gotEvent Event, gotRule Rule, gotValues eventValues) error {
				calls++
				if gotEvent.ID != e.ID || gotRule.Prompt != rule.Prompt || gotValues.number != values.number {
					t.Fatalf("agent request: event=%+v, rule=%+v, values=%+v", gotEvent, gotRule, gotValues)
				}
				state, err := loadState(stateFile)
				if err != nil || state.Jobs[e.ID].Status != jobRunning {
					t.Fatalf("job was not running before agent call: state=%+v, err=%v", state, err)
				}
				if tc.result == context.Canceled {
					return ctx.Err()
				}
				return tc.result
			})
			ctx := context.Background()
			if tc.result == context.Canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := r.runJob(ctx, e, job, rule, values)
			if calls != 1 {
				t.Fatalf("agent calls = %d, want 1", calls)
			}
			if tc.result == nil && err != nil || tc.result != nil && !errors.Is(err, tc.result) {
				t.Fatalf("runJob error = %v, want %v", err, tc.result)
			}
			if tc.result != nil && (!strings.Contains(err.Error(), "agent failed") || !strings.Contains(err.Error(), "job remains running until reconciled")) {
				t.Fatalf("runJob error lost context: %v", err)
			}
			state, err := loadState(stateFile)
			if err != nil {
				t.Fatal(err)
			}
			if state.Jobs[e.ID].Status != tc.wantStatus || slices.Contains(state.Seen, e.ID) != tc.wantSeen {
				t.Fatalf("final state: %+v", state)
			}
		})
	}
}
