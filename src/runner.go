package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"
)

// AgentRunner executes a matched rule. The kernel owns job state around this call.
type AgentRunner interface {
	Run(context.Context, Event, Rule, eventValues) error
}

// ExecutionSpec describes the command and event values selected for an event.
type ExecutionSpec struct {
	Rule   Rule
	Values eventValues
}

// Router maps a normalized event to an execution specification, or no match.
type Router interface {
	Route(Event) (*ExecutionSpec, error)
}

// EventBatch contains normalized events and source polling constraints.
type EventBatch struct {
	Events       []Event
	PollInterval time.Duration
	AtCapacity   bool
}

// EventSource fetches events and commits its polling checkpoint after the
// kernel has durably handled the batch.
type EventSource interface {
	Fetch(context.Context) (EventBatch, error)
	Commit()
}

type Runner struct {
	Config       Config
	Source       EventSource
	Router       Router
	AgentRunner  AgentRunner
	Store        StateStore
	State        State
	Logger       *log.Logger
	PollInterval time.Duration
}

func (r *Runner) persist(s State) error {
	if r.Store == nil {
		return fmt.Errorf("state store is not configured")
	}
	if err := r.Store.Save(s); err != nil {
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
	if r.State.Initialized {
		// Retry durable jobs even after their source events leave GitHub's feed.
		ids := make([]string, 0, len(r.State.Jobs))
		for id, job := range r.State.Jobs {
			if job.Status == jobPending && job.Event != nil {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err := r.dispatchJob(ctx, *r.State.Jobs[id].Event, r.State.Jobs[id]); err != nil {
				return err
			}
		}
	}
	if r.Source == nil {
		return fmt.Errorf("event source is not configured")
	}
	batch, err := r.Source.Fetch(ctx)
	if batch.PollInterval > r.PollInterval {
		r.PollInterval = batch.PollInterval
	}
	if err != nil {
		return err
	}
	events := batch.Events
	if !r.State.Initialized && !replayExisting {
		next := r.State
		next.Seen = slices.Clone(r.State.Seen)
		for _, e := range events {
			next.Seen = append(next.Seen, e.ID)
		}
		next.Initialized = true
		if len(events) > 0 {
			next.Cursor = events[0].ID
		}
		if err := r.persist(next); err != nil {
			return err
		}
		r.Source.Commit()
		r.Logger.Printf("initialized with %d existing events; waiting for new events", len(events))
		return nil
	}
	// The cursor is the newest event from the last completed poll. If it has
	// fallen out of a full feed, the source cannot show whether earlier requests
	// were lost. Keep that uncertainty in durable state until acknowledged.
	if r.State.Cursor != "" && batch.AtCapacity {
		found := false
		for _, e := range events {
			if e.ID == r.State.Cursor {
				found = true
				break
			}
		}
		if !found && !r.State.Gap {
			next := r.State
			next.Gap = true
			if err := r.persist(next); err != nil {
				return err
			}
			r.Logger.Printf("event gap: cursor %s is outside the latest 300 events; earlier requests may be missing", r.State.Cursor)
		}
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
		spec, err := r.route(e)
		if err != nil {
			return err
		}
		if spec != nil {
			if !exists {
				job = Job{Status: jobPending, EventType: e.Type, URL: spec.Values.url, Event: &e}
				if err := r.setJob(e.ID, job, false); err != nil {
					return err
				}
			} else if job.Event == nil {
				// Jobs written by older versions can still use the live event.
				job.Event = &e
				if err := r.setJob(e.ID, job, false); err != nil {
					return err
				}
			}
			if err := r.runJob(ctx, e, job, spec.Rule, spec.Values); err != nil {
				return err
			}
			continue
		}
		if exists && job.Status == jobPending {
			return fmt.Errorf("event %s is pending but no longer matches a configured rule", e.ID)
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
	for id, job := range r.State.Jobs {
		if job.Status == jobPending && job.Event == nil {
			return fmt.Errorf("event %s is pending without saved event data and is absent from the current feed; a new request is needed to retry it", id)
		}
	}
	if len(events) == 0 {
		if err := r.persist(r.State); err != nil {
			return err
		}
	}
	if len(events) > 0 && r.State.Cursor != events[0].ID {
		next := r.State
		next.Cursor = events[0].ID
		if err := r.persist(next); err != nil {
			return err
		}
	}
	r.Source.Commit()
	if r.State.Gap {
		return fmt.Errorf("event gap remains unresolved; inspect missed requests and run with -acknowledge-gap after recovery")
	}
	return nil
}

func (r *Runner) dispatchJob(ctx context.Context, e Event, job Job) error {
	spec, err := r.route(e)
	if err != nil {
		return err
	}
	if spec == nil {
		return fmt.Errorf("event %s is pending but no longer matches a configured rule", e.ID)
	}
	return r.runJob(ctx, e, job, spec.Rule, spec.Values)
}

func (r *Runner) route(e Event) (*ExecutionSpec, error) {
	if r.Router == nil {
		return nil, fmt.Errorf("event %s: router is not configured", e.ID)
	}
	spec, err := r.Router.Route(e)
	if err != nil {
		return nil, fmt.Errorf("event %s: %w", e.ID, err)
	}
	return spec, nil
}

func (r *Runner) runJob(ctx context.Context, e Event, job Job, rule Rule, values eventValues) error {
	if r.AgentRunner == nil {
		return fmt.Errorf("event %s: agent runner is not configured", e.ID)
	}
	job.Status = jobRunning
	if err := r.setJob(e.ID, job, false); err != nil {
		return err
	}
	r.Logger.Printf("dispatching %s %s #%d (event %s)", e.Type, values.action, values.number, e.ID)
	if err := r.AgentRunner.Run(ctx, e, rule, values); err != nil {
		err = fmt.Errorf("agent failed: %w", err)
		return fmt.Errorf("event %s: %w (job remains running until reconciled)", e.ID, err)
	}
	job.Status = jobCompleted
	job.Event = nil
	if err := r.setJob(e.ID, job, true); err != nil {
		return fmt.Errorf("event %s completed but could not persist completion: %w", e.ID, err)
	}
	return nil
}
