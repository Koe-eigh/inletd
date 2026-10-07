package routing_test

import (
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

