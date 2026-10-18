package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"
)

type Runner struct {
	Config       Config
	Client       *http.Client
	State        State
	Logger       *log.Logger
	PollInterval time.Duration
	ETag         string
	nextETag     string
	save         func(string, State) error
}

func (r *Runner) persist(s State) error {
	save := r.save
	if save == nil {
		save = saveState
	}
	if err := save(r.Config.StateFile, s); err != nil {
		return err
	}
	r.State = s
	return nil
}

func (r *Runner) setJob(id string, job Job, completed bool) error {
	next := r.State
	next.Jobs = make(map[string]Job, len(r.State.Jobs)+1)
	for key, value := range r.State.Jobs {
		next.Jobs[key] = value
	}
	next.Jobs[id] = job
	if completed {
		next.Seen = append(slices.Clone(r.State.Seen), id)
		if len(next.Seen) > 1000 {
			for _, old := range next.Seen[:len(next.Seen)-1000] {
				if next.Jobs[old].Status == jobCompleted {
					delete(next.Jobs, old)
				}
			}
			next.Seen = next.Seen[len(next.Seen)-1000:]
		}
	}
	return r.persist(next)
}

func (r *Runner) Poll(ctx context.Context, replayExisting bool) error {
	events, err := r.fetch(ctx)
	if err != nil {
		return err
	}
	if !r.State.Initialized && !replayExisting {
		next := r.State
		next.Seen = slices.Clone(r.State.Seen)
		for _, e := range events {
			next.Seen = append(next.Seen, e.ID)
		}
		next.Initialized = true
		if err := r.persist(next); err != nil {
			return err
		}
		r.ETag = r.nextETag
		r.Logger.Printf("initialized with %d existing events; waiting for new events", len(events))
		return nil
	}
	if !r.State.Initialized {
		next := r.State
		next.Initialized = true
		if err := r.persist(next); err != nil {
			return err
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.ID == "" || !strings.EqualFold(e.Repo.Name, r.Config.Repository) || slices.Contains(r.State.Seen, e.ID) {
			continue
		}
		job, exists := r.State.Jobs[e.ID]
		if exists {
			switch job.Status {
			case jobCompleted:
				continue
			case jobRunning:
				r.Logger.Printf("event %s may have completed; inspect %s and resolve the running job before retrying", e.ID, job.URL)
				continue
			case jobPending:
			default:
				return fmt.Errorf("event %s has unknown job status %q", e.ID, job.Status)
			}
		}
		rule, values, err := matchRule(e, r.Config.Rules)
		if err != nil {
			return fmt.Errorf("event %s: %w", e.ID, err)
		}
		if rule != nil {
			if !exists {
				job = Job{Status: jobPending, EventType: e.Type, URL: values.url}
				if err := r.setJob(e.ID, job, false); err != nil {
					return err
				}
			}
			job.Status = jobRunning
			if err := r.setJob(e.ID, job, false); err != nil {
				return err
			}
			if err := r.runAgent(ctx, e, *rule, values); err != nil {
				return fmt.Errorf("event %s: %w (job remains running until reconciled)", e.ID, err)
			}
			job.Status = jobCompleted
			if err := r.setJob(e.ID, job, true); err != nil {
				return fmt.Errorf("event %s completed but could not persist completion: %w", e.ID, err)
			}
			continue
		}
		next := r.State
		next.Seen = append(slices.Clone(r.State.Seen), e.ID)
		if len(next.Seen) > 1000 {
			next.Seen = next.Seen[len(next.Seen)-1000:]
		}
		if err := r.persist(next); err != nil {
			return err
		}
	}
	if len(events) == 0 {
		if err := r.persist(r.State); err != nil {
			return err
		}
	}
	r.ETag = r.nextETag
	return nil
}
