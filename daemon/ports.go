package daemon

import (
	"context"

	"github.com/Koe-eigh/inletd/routing"
)

// Source delivers events until it finishes, encounters an error, or ctx is
// cancelled. Receive calls deliver synchronously for each event and waits for
// it to return before delivering another. A nil return means the source ended
// normally. A source failure returns an error. If deliver returns an error,
// Receive stops and returns that error (possibly wrapped so errors.Is can
// identify it). On cancellation, Receive stops and returns an error matching
// ctx.Err(). The caller's deliver function should also honor ctx cancellation.
// Receive must not call deliver after returning.
type Source interface {
	Receive(ctx context.Context, deliver func(routing.Event) error) error
}

// ActionExecutor runs a selected action with the event that selected it.
// Execute returns nil on success or an error on failure. Implementations must
// honor ctx cancellation and return an error matching ctx.Err() when cancelled.
// The Event is read-only; its Payload method returns a copy of the bytes.
type ActionExecutor interface {
	Execute(ctx context.Context, action routing.Action, event routing.Event) error
}
