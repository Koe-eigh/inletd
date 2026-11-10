package main

import (
	"context"
	"crypto/rand"
	"errors"
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
		if err := validateContainerPaths(c); err != nil {
			return err
		}
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
		if rule.Execution.Profile != "unrestricted" && strings.HasPrefix(strings.ToUpper(target), "DOCKER_") {
			return fmt.Errorf("container env cannot override Docker client settings: %s", target)
		}
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

// agentDockerClient owns a fresh config directory outside the mounted checkout.
// Flags take precedence over HOME fallback, saved contexts, and proxy settings.
type agentDockerClient struct{ configDir string }

const agentDockerHost = "unix:///var/run/docker.sock"

func newAgentDockerClient(workdir string) (agentDockerClient, error) {
	dir, err := os.MkdirTemp("", "inletd-docker-")
	if err != nil {
		return agentDockerClient{}, err
	}
	client := agentDockerClient{configDir: dir}
	if err := outsideWorkdir(workdir, dir); err != nil {
		os.RemoveAll(dir)
		return agentDockerClient{}, fmt.Errorf("Docker config: %w", err)
	}
	return client, nil
}

func (d agentDockerClient) args(args ...string) []string {
	return append([]string{"--config", d.configDir, "--host", agentDockerHost}, args...)
}

func (d agentDockerClient) removeContainer(name string) {
	// Cleanup uses the same isolated configuration and endpoint as the run command,
	// but no action secrets and a fresh context independent of agent cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", d.args("rm", "--force", name)...)
	cmd.Env = []string{}
	_ = cmd.Run()
}

// Resolve existing symlinks, including a dangling link's target and existing
// parent directories when the final state file has not been created yet.
func canonicalAgentPath(path string) (string, error) {
	return canonicalAgentPathDepth(path, 0)
}

func canonicalAgentPathDepth(path string, depth int) (string, error) {
	if depth > 255 {
		return "", fmt.Errorf("too many path components or symbolic links")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	info, statErr := os.Lstat(path)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return canonicalAgentPathDepth(target, depth+1)
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = canonicalAgentPathDepth(parent, depth+1)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func outsideWorkdir(workdir, path string) error {
	root, err := canonicalAgentPath(workdir)
	if err != nil {
		return err
	}
	// Check every traversed directory entry as well as the final target. A path
	// that enters the checkout and exits through another symlink is still mutable
	// by the agent, even when its current final target is outside the mount.
	target, err := canonicalAgentPath(path)
	if err != nil {
		return err
	}
	candidates := []string{target}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		parent := filepath.Dir(path)
		resolvedParent, err := canonicalAgentPath(parent)
		if err != nil {
			return err
		}
		candidates = append(candidates, filepath.Join(resolvedParent, filepath.Base(path)))
		if parent == path {
			break
		}
		path = parent
	}
	for _, candidate := range candidates {
		rel, err := filepath.Rel(root, candidate)
		if err != nil {
			return err
		}
		if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("path must be outside container workdir: %s", candidate)
		}
	}
	return nil
}

func validateContainerPaths(c Config) error {
	for _, path := range []string{c.ConfigPath, c.StateFile} {
		if path != "" {
			if err := outsideWorkdir(c.Workdir, path); err != nil {
				return err
			}
		}
	}
	if c.StateFile != "" {
		return outsideWorkdir(c.Workdir, c.StateFile+".lock")
	}
	return nil
}
