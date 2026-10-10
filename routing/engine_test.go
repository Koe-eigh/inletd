package routing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Koe-eigh/inletd/routing"
)

func TestDeclarativeRouter(t *testing.T) {
	testRouteMap := map[string][]string{"test_event1": {"test_action1", "test_action2"}, "test_event2": {"test_action3"}}

	testDeclarativeRouter := routing.NewDeclarativeRouter(testRouteMap)

	ctx := t.Context()

	// known events
	actionsForEvent1, err1 := testDeclarativeRouter.Route(ctx, routing.NewEvent("test_event1"))
	actionsForEvent2, err2 := testDeclarativeRouter.Route(ctx, routing.NewEvent("test_event2"))

	if err1 != nil {
		t.Fatalf("Expected nil")
	}

	if err2 != nil {
		t.Fatalf("Expected nil")
	}

	if actionsForEvent1[0].Name() != testRouteMap["test_event1"][0] || actionsForEvent1[1].Name() != testRouteMap["test_event1"][1] {
		t.Errorf("Expected: test_action1, test_action2. Actual: %v, %v", actionsForEvent1[0].Name(), actionsForEvent1[1].Name())
	}

	if actionsForEvent2[0].Name() != testRouteMap["test_event2"][0] {
		t.Errorf("Expected: test_action3. Actual: %v", actionsForEvent2[0].Name())
	}

	// unknown events
	actions, err := testDeclarativeRouter.Route(ctx, routing.NewEvent("test_event3"))

	if len(actions) != 0 || err != nil {
		t.Fatalf("Expected: {events: []}. Actual: {events: %v}", actions)
	}
}

func TestDeclarativeRouterResultDoesNotChangeConfiguration(t *testing.T) {
	router := routing.NewDeclarativeRouter(map[string][]string{"event": {"review", "notify"}})
	ctx := t.Context()
	event := routing.NewEvent("event")

	actions, err := router.Route(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 {
		t.Fatalf("expected two actions, got %v", actions)
	}
	actions[0] = routing.NewAction("replacement")

	next, err := router.Route(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 2 || next[0].Name() != "review" || next[1].Name() != "notify" {
		t.Fatalf("expected configured review and notify actions after modifying a previous result, got %v", next)
	}
}

func TestDeclarativeRouterMatchesOnlyEventName(t *testing.T) {
	router := routing.NewDeclarativeRouter(map[string][]string{
		"pull_request.opened": {"review", "notify"},
	})
	eventTime := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		event routing.Event
		want  []string
	}{
		{"name only", routing.NewEvent("pull_request.opened"), []string{"review", "notify"}},
		{"source", routing.NewEvent("pull_request.opened", routing.WithSource("github")), []string{"review", "notify"}},
		{"source ID", routing.NewEvent("pull_request.opened", routing.WithSourceEventID("delivery-42")), []string{"review", "notify"}},
		{"source time", routing.NewEvent("pull_request.opened", routing.WithSourceEventTime(eventTime)), []string{"review", "notify"}},
		{"payload", routing.NewEvent("pull_request.opened", routing.WithPayload([]byte(`{"number":42}`))), []string{"review", "notify"}},
		{"all metadata", routing.NewEvent("pull_request.opened", routing.WithSource("github"), routing.WithSourceEventID("delivery-42"), routing.WithSourceEventTime(eventTime), routing.WithPayload([]byte(`{"number":42}`))), []string{"review", "notify"}},
		{"unknown name with metadata", routing.NewEvent("pull_request.closed", routing.WithSource("github"), routing.WithSourceEventID("delivery-42"), routing.WithSourceEventTime(eventTime), routing.WithPayload([]byte(`{"number":42}`))), nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actions, err := router.Route(t.Context(), test.event)
			if err != nil {
				t.Fatal(err)
			}
			if actions == nil {
				t.Fatal("declarative router returned a nil action slice")
			}
			if len(actions) != len(test.want) {
				t.Fatalf("expected %d actions, got %v", len(test.want), actions)
			}
			for i, want := range test.want {
				if actions[i].Name() != want {
					t.Errorf("action %d: expected %q, got %q", i, want, actions[i].Name())
				}
			}
		})
	}
}

func TestDeclarativeRouterEnrichedEventResultDoesNotChangeConfiguration(t *testing.T) {
	router := routing.NewDeclarativeRouter(map[string][]string{"event": {"review", "notify"}})
	event := routing.NewEvent("event", routing.WithSource("github"), routing.WithPayload([]byte(`{"number":42}`)))

	actions, err := router.Route(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	actions[0] = routing.NewAction("replacement")

	next, err := router.Route(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 2 || next[0].Name() != "review" || next[1].Name() != "notify" {
		t.Fatalf("expected configured actions after modifying a previous result, got %v", next)
	}
}

func TestFunctionalRouter(t *testing.T) {
	ctx := t.Context()
	calls := 0
	var router routing.Router = routing.NewFunctionalRouter(func(gotCtx context.Context, event routing.Event) ([]routing.Action, error) {
		calls++
		if gotCtx != ctx {
			t.Fatal("routing function did not receive the caller's context")
		}
		if event.Name() == "pull_request.opened" {
			return []routing.Action{routing.NewAction("review"), routing.NewAction("notify")}, nil
		}
		return nil, nil
	})

	actions, err := router.Route(ctx, routing.NewEvent("pull_request.opened"))
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].Name() != "review" || actions[1].Name() != "notify" {
		t.Fatalf("expected review and notify actions, got %v", actions)
	}

	actions, err = router.Route(ctx, routing.NewEvent("ignored"))
	if err != nil || len(actions) != 0 {
		t.Fatalf("expected no actions and no error, got %v, %v", actions, err)
	}
	if calls != 2 {
		t.Fatalf("expected a routing decision for each event, got %d calls", calls)
	}
}

func TestFunctionalRouterPropagatesError(t *testing.T) {
	wantErr := errors.New("routing decision failed")
	router := routing.NewFunctionalRouter(func(context.Context, routing.Event) ([]routing.Action, error) {
		return nil, wantErr
	})

	actions, err := router.Route(t.Context(), routing.NewEvent("event"))
	if actions != nil || err != wantErr {
		t.Fatalf("expected nil actions and original error, got %v, %v", actions, err)
	}
}

func TestFunctionalRouterPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	router := routing.NewFunctionalRouter(func(ctx context.Context, _ routing.Event) ([]routing.Action, error) {
		return nil, ctx.Err()
	})

	actions, err := router.Route(ctx, routing.NewEvent("event"))
	if actions != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected nil actions and cancellation, got %v, %v", actions, err)
	}
}
