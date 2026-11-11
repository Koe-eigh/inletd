package main

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

type State struct {
	GmailCheckpoint int64            `json:"gmail_checkpoint,omitempty"`
	GmailRetainedAt map[string]int64 `json:"gmail_retained_at,omitempty"`
	Source          string           `json:"source,omitempty"`
	Initialized     bool             `json:"initialized"`
	Seen            []string         `json:"seen"`
	GmailMessages   []string         `json:"gmail_messages,omitempty"`
	Cursor          string           `json:"cursor,omitempty"`
	Gap             bool             `json:"gap,omitempty"`
	Jobs            map[string]Job   `json:"jobs,omitempty"`
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
	jobRejected  = "rejected"
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

const (
	gmailBackfill          = 7 * 24 * time.Hour
	gmailOverlap           = time.Hour
	gmailDeliveryRetention = 30 * 24 * time.Hour
)

// compactGmail migrates undated legacy IDs conservatively: retain them for a
// full window from upgrade. Active jobs never expire. Clone all changed data
// so a failed Save cannot mutate the runner's last durable state.
func (s State) compactGmail(now time.Time) State {
	retained := make(map[string]int64)
	jobs := make(map[string]Job, len(s.Jobs))
	for id, job := range s.Jobs {
		jobs[id] = job
	}
	keep := func(key string, window time.Duration) bool {
		at := s.GmailRetainedAt[key]
		if at == 0 {
			at = now.Unix()
		}
		if at < now.Add(-window).Unix() {
			return false
		}
		retained[key] = at
		return true
	}
	seen := make([]string, 0, len(s.Seen))
	for _, id := range s.Seen {
		if keep(id, gmailDeliveryRetention) {
			seen = append(seen, id)
		} else if job := jobs[id]; job.Status == jobCompleted || job.Status == jobRejected {
			delete(jobs, id)
		}
	}
	// Completed jobs from old/manual resolution paths need not be in Seen.
	for id, job := range jobs {
		if job.Status == jobCompleted || job.Status == jobRejected {
			if !keep(id, gmailDeliveryRetention) {
				delete(jobs, id)
			}
		}
	}
	messages := make([]string, 0, len(s.GmailMessages))
	for _, id := range s.GmailMessages {
		if keep("message:"+id, gmailBackfill+gmailOverlap) {
			messages = append(messages, id)
		}
	}
	s.Seen, s.Jobs, s.GmailMessages, s.GmailRetainedAt = seen, jobs, messages, retained
	return s
}
