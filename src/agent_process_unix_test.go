//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentStopsBackgroundChildAfterLeaderExit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exitCode string
		wantErr  bool
	}{
		{"success", "0", false},
		{"failure", "7", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command("sh", "-c", "sh -c 'sleep 2; printf survived > marker' & exit "+tc.exitCode)
			cmd.Dir = dir
			err := runAgentCommand(context.Background(), cmd)
			if (err != nil) != tc.wantErr {
				t.Fatalf("runAgentCommand error = %v, want error = %t", err, tc.wantErr)
			}
			var exitErr *exec.ExitError
			if tc.wantErr && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 7) {
				t.Fatalf("runAgentCommand error = %v, want exit status 7", err)
			}
			time.Sleep(2200 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(dir, "marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("background child survived leader exit: marker stat error = %v", err)
			}
		})
	}
}

func TestAgentStopsDescendantsOnContextEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout bool
		wantErr error
	}{
		{"timeout", true, context.DeadlineExceeded},
		{"shutdown", false, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.timeout {
				ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
				defer cancel()
			}
			r := &Runner{Config: Config{Workdir: dir, AgentTimeoutSec: 5}, Logger: log.New(os.Stderr, "", 0)}
			rule := Rule{Command: []string{"sh", "-c", "sh -c 'sleep 2; printf survived > marker' & printf ready > ready; wait"}, Prompt: "test"}
			result := make(chan error, 1)
			go func() {
				result <- r.runAgent(ctx, Event{}, rule, eventValues{})
			}()
			ready := filepath.Join(dir, "ready")
			deadline := time.After(2 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case <-deadline:
					t.Fatal("agent did not start its child")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if !tc.timeout {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("runAgent error = %v, want %v", err, tc.wantErr)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("agent termination was not bounded")
			}
			// The child would write this file after two seconds if it survived.
			time.Sleep(2200 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(dir, "marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("descendant survived cancellation: marker stat error = %v", err)
			}
		})
	}
}

func TestAgentForceStopsProcessIgnoringTERM(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &Runner{Config: Config{Workdir: dir, AgentTimeoutSec: 5}, Logger: log.New(os.Stderr, "", 0)}
	rule := Rule{Command: []string{"sh", "-c", "trap '' TERM; printf ready > ready; while :; do sleep 1; done"}, Prompt: "test"}
	result := make(chan error, 1)
	go func() {
		result <- r.runAgent(ctx, Event{}, rule, eventValues{})
	}()
	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("agent did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runAgent error = %v, want cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent ignoring SIGTERM was not force stopped")
	}
}
