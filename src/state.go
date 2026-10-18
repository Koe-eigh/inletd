package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type State struct {
	Initialized bool           `json:"initialized"`
	Seen        []string       `json:"seen"`
	Jobs        map[string]Job `json:"jobs,omitempty"`
}

type Job struct {
	Status    string `json:"status"`
	EventType string `json:"event_type"`
	URL       string `json:"url,omitempty"`
}

const (
	jobPending   = "pending"
	jobRunning   = "running"
	jobCompleted = "completed"
)

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
		next.Seen = append(slices.Clone(s.Seen), id)
	}
	next.Jobs[id] = job
	return next, nil
}

func loadState(path string) (State, error) {
	var s State
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func saveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".inletd-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
