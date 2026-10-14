package daemon_test

import (
	"bytes"
	"context"
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
	var source daemon.Source = oneEventSource{event: event}
	executor := &recordingExecutor{}
	var actionExecutor daemon.ActionExecutor = executor
	router := routing.NewDeclarativeRouter(map[string][]string{"pull_request.opened": {"review"}})
	ctx := t.Context()

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
	got := executor.event
	if got.Name() != event.Name() || got.Source() != event.Source() || got.SourceEventID() != event.SourceEventID() || !got.SourceEventTime().Equal(eventTime) || !bytes.Equal(got.Payload(), event.Payload()) {
		t.Fatalf("executor received a different event: %+v", got)
	}
}
