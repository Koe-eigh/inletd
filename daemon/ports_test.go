package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Koe-eigh/inletd/daemon"
	"github.com/Koe-eigh/inletd/routing"
)

type oneEventSource struct{ event routing.Event }

func (source oneEventSource) Receive(ctx context.Context, deliver func(routing.Event) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return deliver(source.event)
}

type recordingExecutor struct {
	ctx    context.Context
	action routing.Action
	event  routing.Event
}

func (executor *recordingExecutor) Execute(ctx context.Context, action routing.Action, event routing.Event) error {
	executor.ctx, executor.action, executor.event = ctx, action, event
	return nil
}

func TestSourceAndExecutorContractsPassSelectedActionAndFullEvent(t *testing.T) {
	eventTime := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	event := routing.NewEvent("pull_request.opened",
		routing.WithSource("github"),
		routing.WithSourceEventID("delivery-42"),
		routing.WithSourceEventTime(eventTime),
		routing.WithPayload([]byte(`{"number":42}`)),
	)
	assertEvent := func(t *testing.T, got routing.Event) {
		t.Helper()
		if got.Name() != event.Name() || got.Source() != event.Source() || got.SourceEventID() != event.SourceEventID() || !got.SourceEventTime().Equal(eventTime) || !bytes.Equal(got.Payload(), event.Payload()) {
			t.Fatalf("received a different event: %+v", got)
		}
	}
	for _, name := range []string{"declarative", "functional"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			var router routing.Router = routing.NewDeclarativeRouter(map[string][]string{"pull_request.opened": {"review"}})
			if name == "functional" {
				router = routing.NewFunctionalRouter(func(gotCtx context.Context, got routing.Event) ([]routing.Action, error) {
					if gotCtx != ctx {
						t.Fatal("functional router received a different context")
					}
					assertEvent(t, got)
					return []routing.Action{routing.NewAction("review")}, nil
				})
			}
			var source daemon.Source = oneEventSource{event: event}
			executor := &recordingExecutor{}
			var actionExecutor daemon.ActionExecutor = executor
			err := source.Receive(ctx, func(received routing.Event) error {
				actions, err := router.Route(ctx, received)
				if err != nil {
					return err
				}
				for _, action := range actions {
					if err := actionExecutor.Execute(ctx, action, received); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if executor.ctx != ctx || executor.action.Name() != "review" {
				t.Fatalf("executor received wrong context or action: %v, %q", executor.ctx, executor.action.Name())
			}
			assertEvent(t, executor.event)
		})
	}
}

type sequenceSource struct {
	events      []routing.Event
	terminalErr error
}

func (source sequenceSource) Receive(ctx context.Context, deliver func(routing.Event) error) error {
	for _, event := range source.events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := deliver(event); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return source.terminalErr
}

type failingExecutor struct{ failure error }

func (executor failingExecutor) Execute(ctx context.Context, _ routing.Action, _ routing.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return executor.failure
}

func TestSourceContractStopsOnDeliveryError(t *testing.T) {
	wantErr := errors.New("delivery failed")
	var source daemon.Source = sequenceSource{events: []routing.Event{
		routing.NewEvent("first"), routing.NewEvent("second"),
	}}
	calls := 0
	err := source.Receive(t.Context(), func(routing.Event) error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("expected one delivery and its error, got %d deliveries and %v", calls, err)
	}
}

func TestSourceContractReportsSourceFailure(t *testing.T) {
	wantErr := errors.New("source failed")
	var source daemon.Source = sequenceSource{terminalErr: wantErr}
	if err := source.Receive(t.Context(), func(routing.Event) error {
		t.Fatal("source delivered an unexpected event")
		return nil
	}); !errors.Is(err, wantErr) {
		t.Fatalf("expected source failure, got %v", err)
	}
}

func TestSourceContractStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var source daemon.Source = sequenceSource{events: []routing.Event{
		routing.NewEvent("first"), routing.NewEvent("second"),
	}}
	calls := 0
	err := source.Receive(ctx, func(routing.Event) error {
		calls++
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("expected one delivery and cancellation, got %d deliveries and %v", calls, err)
	}
}

func TestExecutorContractReportsFailureAndCancellation(t *testing.T) {
	wantErr := errors.New("execution failed")
	var executor daemon.ActionExecutor = failingExecutor{failure: wantErr}
	action, event := routing.NewAction("review"), routing.NewEvent("pull_request.opened")
	if err := executor.Execute(t.Context(), action, event); !errors.Is(err, wantErr) {
		t.Fatalf("expected execution failure, got %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := executor.Execute(ctx, action, event); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestRoutingAndExecutionErrorsStopSourceDelivery(t *testing.T) {
	for _, stage := range []string{"routing", "execution"} {
		t.Run(stage, func(t *testing.T) {
			wantErr := errors.New(stage + " failed")
			calls := 0
			var source daemon.Source = sequenceSource{events: []routing.Event{
				routing.NewEvent("first"), routing.NewEvent("second"),
			}}
			var router routing.Router = routing.NewFunctionalRouter(func(context.Context, routing.Event) ([]routing.Action, error) {
				calls++
				if stage == "routing" {
					return nil, wantErr
				}
				return []routing.Action{routing.NewAction("review")}, nil
			})
			var executor daemon.ActionExecutor = failingExecutor{failure: wantErr}
			ctx := t.Context()
			err := source.Receive(ctx, func(event routing.Event) error {
				actions, err := router.Route(ctx, event)
				if err != nil {
					return err
				}
				for _, action := range actions {
					if err := executor.Execute(ctx, action, event); err != nil {
						return err
					}
				}
				return nil
			})
			if !errors.Is(err, wantErr) || calls != 1 {
				t.Fatalf("expected one routing decision and %s error, got %d decisions and %v", stage, calls, err)
			}
		})
	}
}
