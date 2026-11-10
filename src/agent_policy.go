package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func ingestEnv(c Config, name string) bool {
	for _, forbidden := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "INLETD_GMAIL_SIGNING_KEY", c.TokenEnv, c.Gmail.SigningKeyEnv} {
		if forbidden != "" && strings.EqualFold(name, forbidden) {
			return true
		}
	}
	return false
}

func validateAgentPolicy(c Config, rule Rule) error {
	switch rule.Execution.Profile {
	case "", "container":
		if rule.Execution.Image == "" || strings.HasPrefix(rule.Execution.Image, "-") || containsEventPlaceholder(rule.Execution.Image) {
			return fmt.Errorf("container execution requires a fixed image; host execution requires explicit profile unrestricted")
		}
		if rule.Execution.Network != "" && rule.Execution.Network != "none" && rule.Execution.Network != "bridge" {
			return fmt.Errorf("container network must be none or bridge")
		}
	case "unrestricted":
		if rule.Execution.Image != "" || rule.Execution.Network != "" {
			return fmt.Errorf("image and network require container execution")
		}
	default:
		return fmt.Errorf("execution profile must be container or unrestricted")
	}
	for target, source := range rule.Env {
		if !validEnvName(target) || !validEnvName(source) {
			return fmt.Errorf("env must map valid variable names to source variable names")
		}
		if ingestEnv(c, source) {
			return fmt.Errorf("env cannot forward ingest credential %s; use a separate action credential", source)
		}
	}
	return nil
}

// Only these non-credential settings and explicitly mapped action variables reach
// the child. No HOME, SSH agent, Git config, proxy, or CLI auth config is inherited.
func agentEnvironment(c Config, rule Rule) ([]string, error) {
	env := map[string]string{}
	for _, name := range []string{"PATH", "LANG", "LC_ALL", "TERM", "TZ"} {
		if value, ok := os.LookupEnv(name); ok && !ingestEnv(c, name) {
			env[name] = value
		}
	}
	for target, source := range rule.Env {
		value, ok := os.LookupEnv(source)
		if !ok || value == "" {
			return nil, fmt.Errorf("action environment variable %s is empty", source)
		}
		env[target] = value
	}
	keys := make([]string, 0, len(env))
	for name := range env {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, name := range keys {
		result = append(result, name+"="+env[name])
	}
	return result, nil
}

func containerCommand(c Config, rule Rule, command []string) ([]string, string, error) {
	dir, err := filepath.Abs(c.Workdir)
	if err != nil {
		return nil, "", err
	}
	if strings.ContainsAny(dir, ",\n\r") {
		return nil, "", fmt.Errorf("container workdir cannot contain commas or newlines")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, "", err
	}
	name := fmt.Sprintf("inletd-%x", id)
	network := rule.Execution.Network
	if network == "" {
		network = "none"
	}
	// Never run as container root, even when the daemon itself is root.
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 65534, 65534
	}
	args := []string{"run", "--rm", "--pull=never", "--name", name, "--interactive", "--init",
		"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--network", network,
		"--user", fmt.Sprintf("%d:%d", uid, gid), "--pids-limit=256",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m", "--mount", "type=bind,src=" + dir + ",dst=/workspace",
		"--workdir", "/workspace", "--env", "HOME=/tmp"}
	keys := make([]string, 0, len(rule.Env))
	for name := range rule.Env {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		args = append(args, "--env", name)
	}
	// Ignore image ENTRYPOINT: only the configured fixed command may run.
	args = append(args, "--entrypoint", command[0], rule.Execution.Image)
	args = append(args, command[1:]...)
	return args, name, nil
}

func removeAgentContainer(name string) {
	// The Docker CLI can exit before its container. Use a fresh bounded context
	// on success, cancellation, and failure, without passing any action secrets.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "rm", "--force", name)
	cmd.Env = []string{}
	_ = cmd.Run()
}
