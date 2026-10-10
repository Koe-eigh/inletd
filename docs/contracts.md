# Core Contracts

`routing.Event` is the transport-independent envelope passed from a source to
routing and execution. It carries a name, optional source identity and event
ID/time, and opaque payload bytes.

## Ownership and limits

The source owns its input bytes. `NewEvent(..., WithPayload(data))` copies them,
so the source may reuse its buffer afterward. `Event.Payload()` returns a copy
owned by the caller; passing the event between components needs no extra copy.

The core has no payload size limit. A source adapter sets and enforces its input
limit before constructing an event. It rejects oversized input without
delivering a truncated event and returns a descriptive error from `Receive`.
An executor with its own limit returns an execution error when that limit is
exceeded.

## Delivery and errors

`daemon.Source.Receive(ctx, deliver)` calls `deliver` synchronously and waits
for it before delivering another event. It returns nil on normal completion, a
source error on source failure, or the delivery error (possibly wrapped while
preserving `errors.Is`). Cancellation stops delivery and returns an error
matching `ctx.Err()`; the delivery callback should honor the same context.
No events may be delivered after `Receive` returns.

The caller routes each delivered event and executes the selected actions with
the same event. Declarative routing matches only the event name. A functional
router may inspect payload and should honor cancellation when it performs work.
A routing error stops execution for that event; the caller returns routing or
execution errors from `deliver` to stop intake. `ActionExecutor.Execute`
returns nil on success, an error on failure, and an error matching `ctx.Err()`
on cancellation. Retry, acknowledgement, and deduplication policies belong to
the caller and adapters.

## Payload-aware routing

A functional router can inspect JSON using `context`, `encoding/json`, and
`routing` without importing a transport package:

```go
router := routing.NewFunctionalRouter(func(ctx context.Context, event routing.Event) ([]routing.Action, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    if event.Name() != "pull_request.opened" {
        return nil, nil
    }
    var payload struct {
        Number int `json:"number"`
    }
    if err := json.Unmarshal(event.Payload(), &payload); err != nil {
        return nil, err
    }
    if payload.Number <= 0 {
        return nil, nil
    }
    return []routing.Action{routing.NewAction("review")}, nil
})
```
