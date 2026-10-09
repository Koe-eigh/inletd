package routing

import (
	"context"
	"slices"
)

type Router interface {
	Route(context.Context, Event) ([]Action, error)
}

type DeclarativeRouter struct {
	routeMap map[EventName][]Action
}

func (router *DeclarativeRouter) Route(ctx context.Context, event Event) ([]Action, error) {
	actions, found := router.routeMap[event.name]
	if !found {
		return []Action{}, nil
	}
	return slices.Clone(actions), nil
}

func NewDeclarativeRouter(routeConfig map[string][]string) *DeclarativeRouter {
	routeMap := make(map[EventName][]Action)
	for eventName, actionNames := range routeConfig {
		actions := []Action{}
		for _, actionName := range actionNames {
			actions = append(actions, NewAction(actionName))
		}
		routeMap[EventName(eventName)] = actions
	}

	return &DeclarativeRouter{routeMap: routeMap}
}

// FunctionalRouter delegates routing decisions to a supplied function.
// The function selects actions; it does not execute them.
type FunctionalRouter struct {
	route func(context.Context, Event) ([]Action, error)
}

// NewFunctionalRouter creates a router using route, which must not be nil.
// The function can return an empty action slice to ignore an event.
func NewFunctionalRouter(route func(context.Context, Event) ([]Action, error)) *FunctionalRouter {
	return &FunctionalRouter{route: route}
}

func (router *FunctionalRouter) Route(ctx context.Context, event Event) ([]Action, error) {
	return router.route(ctx, event)
}
