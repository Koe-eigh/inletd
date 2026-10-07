package routing

import (
	"context"
)

type Router interface {
	Route(context.Context, Event) ([]Action, error)
}

type DeclarativeRouter struct {
	routeMap map[Event][]Action
}

func (router *DeclarativeRouter) Route(ctx context.Context, event Event) ([]Action, error) {
	actions, found := router.routeMap[event];
	if !found {
		return []Action{}, nil
	}
	return actions, nil
}

func NewDeclarativeRouter(routeConfig map[string][]string) *DeclarativeRouter {
	routeMap := make(map[Event][]Action)
	for eventName, actionNames := range routeConfig {
		actions := []Action{}
		for _, actionName := range actionNames {
			actions = append(actions, NewAction(actionName))
		}
		routeMap[NewEvent(eventName)] = actions
	}

	return &DeclarativeRouter{routeMap: routeMap}
}
