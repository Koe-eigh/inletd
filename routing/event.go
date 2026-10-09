package routing

import (
	"slices"
	"time"
)

// EventName identifies an event for declarative routing.
type EventName string

// Event is a transport-independent envelope for a named event.
// Its source is empty when unspecified. A zero source event time and an empty
// source event ID mean that the source did not provide those fields.
type Event struct {
	name            EventName
	source          string
	sourceEventID   string
	sourceEventTime time.Time
	payload         []byte
}

func (event Event) Name() string {
	return string(event.name)
}

func (event Event) Source() string {
	return event.source
}

func (event Event) SourceEventID() string {
	return event.sourceEventID
}

func (event Event) SourceEventTime() time.Time {
	return event.sourceEventTime
}

// Payload returns a copy of the event's bytes. A nil payload means that no
// payload was supplied; an empty non-nil payload remains empty and non-nil.
func (event Event) Payload() []byte {
	return slices.Clone(event.payload)
}

// EventOption sets an optional field when constructing an Event.
type EventOption func(*Event)

func WithSource(source string) EventOption {
	return func(event *Event) { event.source = source }
}

func WithSourceEventID(id string) EventOption {
	return func(event *Event) { event.sourceEventID = id }
}

func WithSourceEventTime(eventTime time.Time) EventOption {
	return func(event *Event) { event.sourceEventTime = eventTime }
}

// WithPayload sets opaque payload bytes. NewEvent copies the bytes when it
// applies this option, so later changes to the input cannot change the event.
func WithPayload(payload []byte) EventOption {
	return func(event *Event) { event.payload = slices.Clone(payload) }
}

// NewEvent constructs an event. Calls with only a name remain valid.
func NewEvent(name string, options ...EventOption) Event {
	event := Event{name: EventName(name)}
	for _, option := range options {
		option(&event)
	}
	return event
}
