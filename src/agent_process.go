package main

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

const agentStopGrace = time.Second

// runAgentCommand owns the process until it exits or its entire process group
// has been asked to stop. Wait runs separately so context cancellation can
// interrupt a command that does not exit on its own.
func runAgentCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := configureAgentProcess(cmd); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	select {
	case err := <-done:
		if ctx.Err() == nil {
			return err
		}
		waited = true
	case <-ctx.Done():
	}

	// The leader can exit before its children. Keep the group alive through
	// the grace period and send the final kill even if Wait has returned.
	stopErr := signalAgentProcess(cmd, false)
	timer := time.NewTimer(agentStopGrace)
	<-timer.C
	stopErr = errors.Join(stopErr, signalAgentProcess(cmd, true))
	if !waited {
		select {
		case <-done:
		case <-time.After(agentStopGrace):
			stopErr = errors.Join(stopErr, errors.New("agent did not exit after forced termination"))
		}
	}
	return errors.Join(ctx.Err(), stopErr)
}
