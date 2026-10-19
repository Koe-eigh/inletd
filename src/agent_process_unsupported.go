//go:build !linux && !darwin

package main

import (
	"errors"
	"os/exec"
)

func configureAgentProcess(_ *exec.Cmd) error {
	return errors.New("agent process tree cleanup is supported only on Linux and macOS")
}

func signalAgentProcess(_ *exec.Cmd, _ bool) error {
	return nil
}
