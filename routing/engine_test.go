package routing_test

import (
	"context"
	"errors"
	"testing"

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
