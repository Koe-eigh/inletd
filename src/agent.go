package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CommandAgentRunner runs configured CLI commands with the rendered prompt on stdin.
type CommandAgentRunner struct {
	Config Config
}

func (r CommandAgentRunner) Run(ctx context.Context, e Event, rule Rule, values eventValues) error {
	replacements := strings.NewReplacer("{repo}", e.Repo.Name, "{event}", e.Type, "{action}", values.action, "{number}", fmt.Sprint(values.number), "{url}", values.url, "{title}", values.title, "{requester}", values.requester)
	prompt := replacements.Replace(rule.Prompt)
	args := make([]string, len(rule.Command))
	for i, arg := range rule.Command {
		args[i] = replacements.Replace(strings.ReplaceAll(arg, "{prompt}", prompt))
	}
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(r.Config.AgentTimeoutSec)*time.Second)
	defer cancel()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = r.Config.Workdir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return runAgentCommand(cmdCtx, cmd)
}
