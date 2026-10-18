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
}

func (r *Runner) Poll(ctx context.Context, replayExisting bool) error {
	events, err := r.fetch(ctx)
	if err != nil {
		return err
	}
	if !r.State.Initialized && !replayExisting {
		for _, e := range events {
			r.State.Seen = append(r.State.Seen, e.ID)
		}
		r.State.Initialized = true
		if err := saveState(r.Config.StateFile, r.State); err != nil {
			return err
		}
		r.ETag = r.nextETag
		r.Logger.Printf("initialized with %d existing events; waiting for new events", len(events))
		return nil
	}
	if !r.State.Initialized {
		r.State.Initialized = true
		if err := saveState(r.Config.StateFile, r.State); err != nil {
			r.State.Initialized = false
			return err
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.ID == "" || !strings.EqualFold(e.Repo.Name, r.Config.Repository) || slices.Contains(r.State.Seen, e.ID) {
			continue
		}
		if err := r.dispatch(ctx, e); err != nil {
			return fmt.Errorf("event %s: %w", e.ID, err)
		}
		previousSeen := slices.Clone(r.State.Seen)
		r.State.Seen = append(r.State.Seen, e.ID)
		if len(r.State.Seen) > 1000 {
			r.State.Seen = r.State.Seen[len(r.State.Seen)-1000:]
		}
		if err := saveState(r.Config.StateFile, r.State); err != nil {
			r.State.Seen = previousSeen
			return err
		}
	}
	if len(events) == 0 {
		if err := saveState(r.Config.StateFile, r.State); err != nil {
			return err
		}
	}
	r.ETag = r.nextETag
	return nil
}
