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
	var commandErr error
	select {
	case err := <-done:
		if ctx.Err() == nil {
			commandErr = err
		}
		waited = true
	case <-ctx.Done():
	}

	// The leader can exit before its children. Stop the group on every exit
	// path, including a successful command and one that exits with an error.
	remaining, stopErr := signalAgentProcess(cmd, false)
	if remaining {
		timer := time.NewTimer(agentStopGrace)
		<-timer.C
		_, forceErr := signalAgentProcess(cmd, true)
		stopErr = errors.Join(stopErr, forceErr)
	}
	if !waited {
		select {
		case <-done:
		case <-time.After(agentStopGrace):
			stopErr = errors.Join(stopErr, errors.New("agent did not exit after forced termination"))
		}
	}
	return errors.Join(commandErr, ctx.Err(), stopErr)
}
