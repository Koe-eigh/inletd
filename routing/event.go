package routing

type Event struct {
	name string
}

func NewEvent(name string) Event {
	return Event{name: name}
}
