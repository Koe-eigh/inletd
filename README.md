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
