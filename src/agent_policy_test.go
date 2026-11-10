package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The subprocess records names only, so a failure never logs a credential value.
func TestAgentEnvironmentChild(t *testing.T) {
	if os.Getenv("INLETD_ENV_TEST_CHILD") != "1" {
		return
	}
	names := []string{}
	for _, entry := range os.Environ() {
		names = append(names, strings.SplitN(entry, "=", 2)[0])
	}
	if err := os.WriteFile(os.Getenv("INLETD_ENV_TEST_OUTPUT"), []byte(strings.Join(names, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentChildEnvironment(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "INLETD_GMAIL_SIGNING_KEY", "CUSTOM_INGEST", "CUSTOM_HMAC", "AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK", "HOME", "GH_TOKEN"} {
		t.Setenv(name, "withheld")
	}
	t.Setenv("ACTION_GITHUB_TOKEN", "separate-action-credential")
	t.Setenv("INLETD_ENV_TEST_CHILD", "1")
	output := filepath.Join(t.TempDir(), "names")
	t.Setenv("INLETD_ENV_TEST_OUTPUT", output)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Workdir: t.TempDir(), AgentTimeoutSec: 5, TokenEnv: "CUSTOM_INGEST", Gmail: GmailConfig{SigningKeyEnv: "CUSTOM_HMAC"}}
	rule := Rule{Execution: AgentExecution{Profile: "unrestricted"}, Command: []string{binary, "-test.run=^TestAgentEnvironmentChild$"}, Prompt: "test", Env: map[string]string{"INLETD_ENV_TEST_CHILD": "INLETD_ENV_TEST_CHILD", "INLETD_ENV_TEST_OUTPUT": "INLETD_ENV_TEST_OUTPUT", "GH_TOKEN": "ACTION_GITHUB_TOKEN"}}
	if err := (CommandAgentRunner{Config: cfg}).Run(context.Background(), Event{}, rule, eventValues{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Split(string(data), "\n")
	for _, name := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "INLETD_GMAIL_SIGNING_KEY", "CUSTOM_INGEST", "CUSTOM_HMAC", "AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK", "HOME", "ACTION_GITHUB_TOKEN"} {
		if slices.Contains(names, name) {
			t.Errorf("child inherited %s", name)
		}
	}
	if !slices.Contains(names, "GH_TOKEN") {
		t.Fatal("explicit action credential missing")
	}
}

func TestAgentPolicyConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		execution AgentExecution
		env       map[string]string
		valid     bool
	}{
		{name: "implicit host rejected"},
		{name: "container requires image", execution: AgentExecution{Profile: "container"}},
		{name: "container default", execution: AgentExecution{Image: "local/agent:tested"}, valid: true},
		{name: "explicit host", execution: AgentExecution{Profile: "unrestricted"}, valid: true},
		{name: "unknown profile", execution: AgentExecution{Profile: "sandbox"}},
		{name: "host network rejected", execution: AgentExecution{Image: "agent", Network: "host"}},
		{name: "bridge opt in", execution: AgentExecution{Image: "agent", Network: "bridge"}, valid: true},
		{name: "dynamic image", execution: AgentExecution{Image: "agent:{title}"}},
		{name: "option image", execution: AgentExecution{Image: "--privileged"}},
		{name: "host ignores image rejected", execution: AgentExecution{Profile: "unrestricted", Image: "agent"}},
		{name: "invalid env", execution: AgentExecution{Profile: "unrestricted"}, env: map[string]string{"X=Y": "ACTION"}},
		{name: "github intake", execution: AgentExecution{Profile: "unrestricted"}, env: map[string]string{"GH_TOKEN": "GITHUB_TOKEN"}},
		{name: "gitlab intake", execution: AgentExecution{Profile: "unrestricted"}, env: map[string]string{"TOKEN": "GITLAB_TOKEN"}},
		{name: "gmail intake", execution: AgentExecution{Profile: "unrestricted"}, env: map[string]string{"TOKEN": "INLETD_GMAIL_SIGNING_KEY"}},
		{name: "custom intake", execution: AgentExecution{Profile: "unrestricted"}, env: map[string]string{"TOKEN": "CUSTOM_INGEST"}},
		{name: "custom hmac", execution: AgentExecution{Profile: "unrestricted"}, env: map[string]string{"TOKEN": "CUSTOM_HMAC"}},
		{name: "separate action token", execution: AgentExecution{Profile: "unrestricted"}, env: map[string]string{"GITHUB_TOKEN": "ACTION_TOKEN"}, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state"), TokenEnv: "CUSTOM_INGEST", Gmail: GmailConfig{SigningKeyEnv: "CUSTOM_HMAC"}, Rules: []Rule{{Event: "IssuesEvent", Command: []string{"agent"}, Prompt: "test", Execution: tc.execution, Env: tc.env}}}
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = loadConfig(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
}

func TestAgentMissingActionCredentialFailsClosed(t *testing.T) {
	t.Setenv("INLETD_MISSING_ACTION", "")
	rule := Rule{Execution: AgentExecution{Profile: "unrestricted"}, Command: []string{"must-not-run"}, Env: map[string]string{"TOKEN": "INLETD_MISSING_ACTION"}}
	err := (CommandAgentRunner{Config: Config{AgentTimeoutSec: 5}}).Run(context.Background(), Event{}, rule, eventValues{})
	if err == nil || !strings.Contains(err.Error(), "INLETD_MISSING_ACTION") {
		t.Fatalf("error=%v", err)
	}
}

func TestRestrictedContainerCommand(t *testing.T) {
	dir := t.TempDir()
	rule := Rule{Execution: AgentExecution{Image: "local/agent:tested"}, Env: map[string]string{"GH_TOKEN": "ACTION_TOKEN"}}
	args, name, err := containerCommand(Config{Workdir: dir}, rule, []string{"agent", "--noninteractive", "hostile --privileged"})
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"--rm", "--pull=never", "--interactive", "--init", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "HOME=/tmp"} {
		if !slices.Contains(args, option) {
			t.Errorf("missing %s", option)
		}
	}
	for option, value := range map[string]string{"--name": name, "--network": "none", "--mount": "type=bind,src=" + dir + ",dst=/workspace", "--workdir": "/workspace", "--entrypoint": "agent"} {
		i := slices.Index(args, option)
		if i < 0 || args[i+1] != value {
			t.Errorf("incorrect %s", option)
		}
	}
	if !slices.Equal(args[len(args)-3:], []string{"local/agent:tested", "--noninteractive", "hostile --privileged"}) {
		t.Fatal("command arguments changed")
	}
	if !slices.Contains(args, "GH_TOKEN") || slices.Contains(args, "ACTION_TOKEN") {
		t.Fatal("credential mapping is incorrect")
	}
	rule.Execution.Network = "bridge"
	args, _, err = containerCommand(Config{Workdir: dir}, rule, []string{"agent"})
	if err != nil || args[slices.Index(args, "--network")+1] != "bridge" {
		t.Fatal("explicit bridge network missing")
	}
}

func TestContainerDispatchAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
base=${0%/*}
if [ "$1" = rm ]; then
 printf '%s\n' "$@" > "$base/cleanup"
 exit 0
fi
printf '%s\n' "$@" > "$base/args"
env | cut -d= -f1 > "$base/env"
cat > "$base/stdin"
`
			if mode == "failure" {
				script += "exit 7\n"
			}
			if mode == "timeout" {
				script += "sleep 30\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GITHUB_TOKEN", "withheld")
			t.Setenv("INLETD_ACTION_TOKEN", "separate-action-token")
			ctx := context.Background()
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
			}
			rule := Rule{Execution: AgentExecution{Image: "local/agent:tested"}, Command: []string{"agent", "{title}"}, Prompt: "Task {title}", Env: map[string]string{"GH_TOKEN": "INLETD_ACTION_TOKEN"}}
			err := (CommandAgentRunner{Config: Config{Workdir: dir, AgentTimeoutSec: 5}}).Run(ctx, Event{}, rule, eventValues{title: "untrusted --privileged"})
			if (err == nil) != (mode == "success") {
				t.Fatalf("error=%v", err)
			}
			data, readErr := os.ReadFile(filepath.Join(dir, "stdin"))
			if readErr != nil || string(data) != "Task untrusted --privileged" {
				t.Fatalf("stdin error=%v", readErr)
			}
			args, readErr := os.ReadFile(filepath.Join(dir, "args"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			names := strings.Split(string(args), "\n")
			i := slices.Index(names, "--name")
			cleanup, readErr := os.ReadFile(filepath.Join(dir, "cleanup"))
			if readErr != nil || i < 0 || string(cleanup) != "rm\n--force\n"+names[i+1]+"\n" {
				t.Fatalf("cleanup error=%v", readErr)
			}
			env, readErr := os.ReadFile(filepath.Join(dir, "env"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			vars := strings.Split(string(env), "\n")
			if slices.Contains(vars, "GITHUB_TOKEN") || !slices.Contains(vars, "GH_TOKEN") {
				t.Fatal("Docker client environment leaked intake token or lost action credential")
			}
		})
	}
}

// Opt in with a pre-pulled trusted image. CI uses Docker to check the actual
// isolation boundary in addition to the daemon-free command/cleanup tests.
func TestContainerIsolation(t *testing.T) {
	image := os.Getenv("INLETD_TEST_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set INLETD_TEST_DOCKER_IMAGE to run the live Docker isolation test")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_TOKEN", "intake-must-not-leak")
	t.Setenv("INLETD_GMAIL_SIGNING_KEY", "signing-must-not-leak")
	t.Setenv("INLETD_ISOLATION_ACTION", "isolated-action")
	script := `test "${GITHUB_TOKEN+x}" != x
test "${INLETD_GMAIL_SIGNING_KEY+x}" != x
test "$GH_TOKEN" = isolated-action
test "$HOME" = /tmp
test "$(id -u)" -ne 0
test "$(ls /sys/class/net)" = lo
if touch /inletd-write-probe 2>/dev/null; then exit 1; fi
cat > /workspace/prompt
printf isolated > /workspace/result
`
	rule := Rule{Execution: AgentExecution{Image: image}, Command: []string{"sh", "-ec", script}, Prompt: "untrusted test", Env: map[string]string{"GH_TOKEN": "INLETD_ISOLATION_ACTION"}}
	if err := (CommandAgentRunner{Config: Config{Workdir: dir, AgentTimeoutSec: 30}}).Run(context.Background(), Event{}, rule, eventValues{}); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{"prompt": "untrusted test", "result": "isolated"} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || string(data) != want {
			t.Fatalf("container output %s: error=%v", file, err)
		}
	}
}
