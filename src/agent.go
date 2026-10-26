package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandAgentRunner runs configured CLI commands with the rendered prompt on stdin.
type CommandAgentRunner struct {
	Config Config
}

func (r CommandAgentRunner) Run(ctx context.Context, e Event, rule Rule, values eventValues) error {
	if err := validateCommandTemplate(rule.Command); err != nil {
		return err
	}
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

var eventPlaceholders = []string{"{repo}", "{event}", "{action}", "{number}", "{url}", "{title}", "{requester}", "{prompt}"}

// Command templates are administrator controlled. Event data may be passed as an
// argument or on stdin, but must not select the executable or form shell code.
func validateCommandTemplate(command []string) error {
	if len(command) == 0 || command[0] == "" {
		return fmt.Errorf("command needs an executable")
	}
	if containsEventPlaceholder(command[0]) {
		return fmt.Errorf("command executable cannot contain event placeholders")
	}
	if len(command) < 3 || !isShell(command[0]) {
		return nil
	}
	for i := 1; i < len(command)-1; i++ {
		arg := command[i]
		if arg == "--" {
			break
		}
		if len(arg) > 1 && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg[1:], "c") {
			if containsEventPlaceholder(command[i+1]) {
				return fmt.Errorf("shell -c script cannot contain event placeholders; pass event data on stdin or as a positional argument")
			}
			break
		}
	}
	return nil
}

func containsEventPlaceholder(s string) bool {
	for _, placeholder := range eventPlaceholders {
		if strings.Contains(s, placeholder) {
			return true
		}
	}
	return false
}

func isShell(command string) bool {
	switch filepath.Base(command) {
	case "sh", "bash", "dash", "ksh", "zsh", "ash":
		return true
	default:
		return false
	}
}
