# Core Contracts

This document describes the event envelope and the boundaries between sources, routing, and execution.

## Event Envelopes

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
were not provided. A nil payload means none was supplied; an empty, non-nil
payload remains distinct from nil.

The source owns its input byte slice. `WithPayload` copies that slice when
`NewEvent` applies the option, so the source may reuse or change its buffer
after constructing the event. The event owns its stored bytes; `Payload()`
returns a fresh copy owned by the caller. Changing that copy cannot change the
event passed to a router or executor. Call `Payload()` once and retain the
returned slice if a handler needs to read it repeatedly. No extra copy is
needed to pass the event between components. The payload is opaque to the core;
its encoding and interpretation belong to the source and consumers.

The core does not impose a payload size limit. Each source adapter is
responsible for setting and enforcing an appropriate input limit before it
buffers or constructs an event. An oversized input must be rejected, not
truncated or delivered as a partial event; the source reports a descriptive
error from `Receive`. Any executor with its own input limit reports an
execution error if the selected event exceeds it. Callers should account for
the memory used by the source buffer, the event's copy, and any `Payload()`
copies when choosing limits and concurrency.

Declarative routing uses only the event name to select actions. Source identity,
source event ID, source event time, and payload do not change a declarative match.
An unmatched event returns an empty action slice. The router only selects actions;
the caller handles subscriptions and execution.

## Source and Executor Contracts

`daemon.Source` receives events with `Receive(ctx, deliver)`. It invokes the
delivery callback synchronously, waiting for each call to finish before sending
another event. This gives the caller a place to route and execute each event and
provides backpressure to the source. `Receive` returns `nil` when the source
finishes normally, or an error if the source fails. A delivery error stops intake
and is returned to the caller; wrapping it is allowed if `errors.Is` still finds
the original error. Cancellation stops intake and returns an error matching
`ctx.Err()`. A source must not deliver events after `Receive` returns. The
delivery callback should honor the same context so cancellation can interrupt
in-progress handling.

`daemon.ActionExecutor` runs `Execute(ctx, action, event)` for each selected
action. It receives the entire event envelope, including source metadata and
payload, so an action can use the same context the router inspected. Execution
returns `nil` on success, an error on failure, and an error matching `ctx.Err()`
on cancellation. Both contracts depend only on `context` and the routing types;
transport subscriptions and workload processes belong in their adapters.

The caller invokes `Router.Route(ctx, event)` inside the delivery callback and
passes each selected action and the same event to `Execute`. Declarative routing
matches only the event name and returns an empty slice for no match. Functional
routing returns its function's actions and error unchanged; its function should
honor cancellation when it performs work. A routing error means no actions
should be executed for that event. The caller returns routing or execution
errors from the delivery callback to stop the source. The contracts do not
define retries, acknowledgements, or deduplication; adapters and the caller
must decide those policies for their transport and workload.

For example, a functional router can inspect JSON payload bytes without
importing a transport package:

```go
import (
    "context"
    "encoding/json"
    "fmt"

    "github.com/Koe-eigh/inletd/routing"
)

func newRouter() routing.Router {
    return routing.NewFunctionalRouter(func(ctx context.Context, event routing.Event) ([]routing.Action, error) {
        if err := ctx.Err(); err != nil {
            return nil, err
        }
        if event.Name() != "pull_request.opened" || event.Source() != "github" {
            return nil, nil
        }

        var payload struct {
            Number int `json:"number"`
        }
        if err := json.Unmarshal(event.Payload(), &payload); err != nil {
            return nil, fmt.Errorf("decode pull request event: %w", err)
        }
        if payload.Number <= 0 {
            return nil, fmt.Errorf("pull request number must be positive")
        }
        return []routing.Action{routing.NewAction("review")}, nil
    })
}
```

Here a malformed payload returns a routing error; an unrelated event returns no
actions. The caller can return the error from its delivery callback to stop
intake, as required by `Source.Receive`.
