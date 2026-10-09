# Inletd

A demon for routing remote events into local workloads.

## The Idea

Recent development workflows increasingly involve AI agents and tools that need to react to events happening in remote systems. However, heavy agentic workloads in a cloud environment are so expensive. In this context, detecting remote changes and running heavy workloads in a local environment is an attractive prospect. Another problem is that a local machine sits behind NAT and a firewall and has no public endpoint. Instead of exposing the local machine to the internet, `inletd` maintains an authenticated outbound subscription to an event transport such as [ntfy](https://ntfy.sh/).

## Architecture Overview

`inletd` is centered around a routing engine that connects remote events to trusted local actions.

```text

Event Source -> Routing Engine -> Local Actions

```

The system has three user-facing abstractions.

- **Event Sources** provide events.
- **Routing Engine** determine whice action should handle each event.
- **Local Actions** represent trusted workloads that can run on the local machine.

The routing engine is the core of `inletd`. Event sources and local actions are adapters around it, keeping ther router independent of any particular transport or workloads.

### Routing Strategies

- **Declarative Routing** lets the user directly define associations between events and actions.
- **Functional Routing** letes the user define a function that determines the destination at runtime. The function may be deterministic code, a classifier, or an LLM-based router such as [Jev](https://typesafe.ai/blog/introducing-system-one-models-and-jev).

Both strategies expose the same fouting abstraction: `Event -> Action`

The difference is only how that decision is produced.

`routing.NewFunctionalRouter` accepts a function with the same signature as
`Router.Route`. It calls the function for every event, passing through the caller's
context and returning the selected actions and error unchanged:

```go
router := routing.NewFunctionalRouter(func(ctx context.Context, event routing.Event) ([]routing.Action, error) {
    if event.Name() == "pull_request.opened" {
        return []routing.Action{routing.NewAction("review")}, nil
    }
    return nil, nil // Ignore this event.
})
```

The supplied function must be non-nil. It selects actions; execution belongs to
the caller. This is the core routing abstraction; integrations for user code in
other languages can supply their decisions through this function.

The overall mental model remains:

**Events enter, routing decides, actions run.**

### Event Envelopes

`routing.Event` carries a name, source identity, optional source event ID and
time, and opaque payload bytes. Sources can construct one without introducing
transport-specific types into the routing package:

```go
event := routing.NewEvent("pull_request.opened",
    routing.WithSource("github"),
    routing.WithSourceEventID("delivery-42"),
    routing.WithPayload(message),
)
```

`routing.NewEvent(name)` remains valid for events without source metadata.
An empty source or source event ID and a zero source event time mean those values
were not provided. `NewEvent` copies payload bytes when the option is applied,
and `Payload()` returns a fresh copy. The same event can therefore be handed to
the router and a later executor without either caller mutating its payload.

Declarative routing uses only the event name to select actions. Source identity,
source event ID, source event time, and payload do not change a declarative match.
An unmatched event returns an empty action slice. The router only selects actions;
the caller handles subscriptions and execution.

### Source and Executor Contracts

`routing.Source` receives events with `Receive(ctx, deliver)`. It invokes the
delivery callback synchronously, waiting for each call to finish before sending
another event. This gives the caller a place to route and execute each event and
provides backpressure to the source. `Receive` returns `nil` when the source
finishes normally, or an error if the source fails. A delivery error stops intake
and is returned to the caller; wrapping it is allowed if `errors.Is` still finds
the original error. Cancellation stops intake and returns an error matching
`ctx.Err()`. A source must not deliver events after `Receive` returns. The
delivery callback should honor the same context so cancellation can interrupt
in-progress handling.

`routing.ActionExecutor` runs `Execute(ctx, action, event)` for each selected
action. It receives the entire event envelope, including source metadata and
payload, so an action can use the same context the router inspected. Execution
returns `nil` on success, an error on failure, and an error matching `ctx.Err()`
on cancellation. Both contracts depend only on `context` and the routing types;
transport subscriptions and workload processes belong in their adapters.
