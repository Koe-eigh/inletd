package routing_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/Koe-eigh/inletd/routing"
)

func TestEventEnvelopeAndPayloadOwnership(t *testing.T) {
	input := []byte(`{"number":42}`)
	eventTime := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	event := routing.NewEvent("pull_request.opened",
		routing.WithSource("github"),
		routing.WithSourceEventID("delivery-42"),
		routing.WithSourceEventTime(eventTime),
		routing.WithPayload(input),
	)
	input[0] = 'x'

	if event.Name() != "pull_request.opened" || event.Source() != "github" || event.SourceEventID() != "delivery-42" || !event.SourceEventTime().Equal(eventTime) {
		t.Fatalf("unexpected event metadata: %q, %q, %q, %v", event.Name(), event.Source(), event.SourceEventID(), event.SourceEventTime())
	}
	if got := event.Payload(); !bytes.Equal(got, []byte(`{"number":42}`)) {
		t.Fatalf("input mutation changed payload: %q", got)
	}

	output := event.Payload()
	output[0] = 'x'
	if got := event.Payload(); !bytes.Equal(got, []byte(`{"number":42}`)) {
		t.Fatalf("returned slice mutation changed payload: %q", got)
	}
}

func TestEventOptionalFields(t *testing.T) {
	legacy := routing.NewEvent("ping")
	if legacy.Name() != "ping" || legacy.Source() != "" || legacy.SourceEventID() != "" || !legacy.SourceEventTime().IsZero() || legacy.Payload() != nil {
		t.Fatalf("unexpected legacy event: %+v", legacy)
	}

	empty := routing.NewEvent("ping", routing.WithPayload([]byte{}))
	if payload := empty.Payload(); payload == nil || len(payload) != 0 {
		t.Fatalf("expected an empty non-nil payload, got %#v", payload)
	}
}

func TestEnrichedEventReachesFunctionalRouter(t *testing.T) {
	eventTime := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	event := routing.NewEvent("pull_request.opened",
		routing.WithSource("github"),
		routing.WithSourceEventID("delivery-42"),
		routing.WithSourceEventTime(eventTime),
		routing.WithPayload([]byte(`{"number":42}`)),
	)
	ctx := t.Context()
	router := routing.NewFunctionalRouter(func(gotCtx context.Context, got routing.Event) ([]routing.Action, error) {
		if gotCtx != ctx {
			t.Fatal("router received a different context")
		}
		if got.Name() != event.Name() || got.Source() != event.Source() || got.SourceEventID() != event.SourceEventID() || !got.SourceEventTime().Equal(eventTime) || !bytes.Equal(got.Payload(), event.Payload()) {
			t.Fatalf("router received a different event: %+v", got)
		}
		return []routing.Action{routing.NewAction("review")}, nil
	})

	actions, err := router.Route(ctx, event)
	if err != nil || len(actions) != 1 || actions[0].Name() != "review" {
		t.Fatalf("unexpected routing result: %v, %v", actions, err)
	}
}

func TestDeclarativeRouterMatchesEnrichedEventByName(t *testing.T) {
	router := routing.NewDeclarativeRouter(map[string][]string{"pull_request.opened": {"review"}})
	event := routing.NewEvent("pull_request.opened", routing.WithSource("github"), routing.WithPayload([]byte(`{"number":42}`)))

	actions, err := router.Route(t.Context(), event)
	if err != nil || len(actions) != 1 || actions[0].Name() != "review" {
		t.Fatalf("unexpected routing result: %v, %v", actions, err)
	}
}
