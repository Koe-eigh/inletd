package main

import (
	"errors"
	"fmt"
	"slices"
)

type State struct {
	Initialized bool           `json:"initialized"`
	Seen        []string       `json:"seen"`
	Cursor      string         `json:"cursor,omitempty"`
	Gap         bool           `json:"gap,omitempty"`
	Jobs        map[string]Job `json:"jobs,omitempty"`
}

type Job struct {
	Status    string `json:"status"`
	EventType string `json:"event_type"`
	URL       string `json:"url,omitempty"`
	Event     *Event `json:"event,omitempty"`
}

const (
	jobPending   = "pending"
	jobRunning   = "running"
	jobCompleted = "completed"
)

// StateStore loads state and saves each transition durably before it is used.
type StateStore interface {
	Load() (State, error)
	Save(State) error
}

func (s State) resolveJob(id, resolution string) (State, error) {
	job, ok := s.Jobs[id]
	if !ok || job.Status != jobRunning {
		return s, fmt.Errorf("event %s is not a running job", id)
	}
	if resolution != jobCompleted && resolution != "retry" {
		return s, errors.New("resolution must be completed or retry")
	}
	next := s
	next.Jobs = make(map[string]Job, len(s.Jobs))
	for key, value := range s.Jobs {
		next.Jobs[key] = value
	}
	if resolution == "retry" {
		job.Status = jobPending
	} else {
		job.Status = jobCompleted
		job.Event = nil
		next.Seen = append(slices.Clone(s.Seen), id)
	}
	next.Jobs[id] = job
	return next, nil
}
