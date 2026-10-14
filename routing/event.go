package routing

type Event struct {
	name string
}

func (event Event) Name() string {
	return event.name
}

func NewEvent(name string) Event {
	return Event{name: name}
}
