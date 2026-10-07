package routing

type Action struct {
	name string
}

func (action Action) Name() string {
	return action.name
}

func NewAction(name string) Action {
	return Action{name: name}
}
