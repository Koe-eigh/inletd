package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerRejectsDaemonPathsInCheckout(t *testing.T) {
	for _, mode := range []string{"outside", "config inside", "state inside", "new nested state inside", "state equals checkout", "state parent symlink", "state dangling symlink", "state inside symlink out", "state directory symlink out", "state traverses checkout", "config symlink in", "config inside symlink out", "workdir symlink", "lock symlink", "sibling prefix", "config file chain", "config directory chain", "state file chain", "state dangling chain", "lock chain", "outside chain", "unrestricted"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			checkout := filepath.Join(root, "checkout")
			daemon := filepath.Join(root, "daemon")
			for _, dir := range []string{checkout, daemon, checkout + "-sibling"} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(daemon, "config.json")
			state := filepath.Join(daemon, "state.json")
			workdir := checkout
			profile := "container"
			valid := mode == "outside" || mode == "sibling prefix" || mode == "unrestricted" || mode == "outside chain"
			link := func(target, path string) {
				t.Helper()
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "config inside":
				path = filepath.Join(checkout, "config.json")
			case "state inside":
				state = filepath.Join(checkout, "state.json")
			case "new nested state inside":
				state = filepath.Join(checkout, "new", "nested", "state.json")
			case "state equals checkout":
				state = checkout
			case "state parent symlink":
				link(checkout, filepath.Join(daemon, "alias"))
				state = filepath.Join(daemon, "alias", "new", "state.json")
			case "state dangling symlink":
				link("../checkout/missing/state.json", state)
			case "state inside symlink out":
				state = filepath.Join(checkout, "state.json")
				link(filepath.Join(daemon, "new-state.json"), state)
			case "state directory symlink out":
				link(daemon, filepath.Join(checkout, "out"))
				state = filepath.Join(checkout, "out", "state.json")
			case "state traverses checkout":
				link(checkout, filepath.Join(daemon, "in"))
				link(daemon, filepath.Join(checkout, "out"))
				state = filepath.Join(daemon, "in", "out", "state.json")
			case "config file chain":
				link("../checkout/config-link", path)
				link("../daemon/actual.json", filepath.Join(checkout, "config-link"))
			case "config directory chain":
				actual := filepath.Join(daemon, "actual")
				if err := os.Mkdir(actual, 0700); err != nil {
					t.Fatal(err)
				}
				link("../checkout/config-dir", filepath.Join(daemon, "config-dir"))
				link("../daemon/actual", filepath.Join(checkout, "config-dir"))
				path = filepath.Join(daemon, "config-dir", "config.json")
			case "state file chain", "state dangling chain":
				link("../checkout/state-link", state)
				link("../daemon/actual-state.json", filepath.Join(checkout, "state-link"))
				if mode == "state file chain" {
					if err := os.WriteFile(filepath.Join(daemon, "actual-state.json"), []byte("{}"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "lock chain":
				link("../checkout/lock-link", state+".lock")
				link("../daemon/actual.lock", filepath.Join(checkout, "lock-link"))
			case "outside chain":
				link("../checkout-sibling/config-link", path)
				link("../daemon/actual.json", filepath.Join(checkout+"-sibling", "config-link"))
			case "config symlink in":
				link(filepath.Join(checkout, "config.json"), path)
			case "config inside symlink out":
				path = filepath.Join(checkout, "config.json")
				link(filepath.Join(daemon, "actual.json"), path)
			case "workdir symlink":
				workdir = filepath.Join(root, "checkout-alias")
				link(checkout, workdir)
				state = filepath.Join(checkout, "state.json")
			case "lock symlink":
				link(filepath.Join(checkout, "lock"), state+".lock")
			case "sibling prefix":
				state = filepath.Join(checkout+"-sibling", "new", "state.json")
			case "unrestricted":
				profile = "unrestricted"
				path = filepath.Join(checkout, "config.json")
				state = filepath.Join(checkout, "state.json")
			}
			execution := AgentExecution{Profile: profile}
			if profile == "container" {
				execution.Image = "agent"
			}
			c := Config{Repository: "acme/app", Workdir: workdir, StateFile: state, Rules: []Rule{{Event: "IssuesEvent", Command: []string{"agent"}, Prompt: "test", Execution: execution}}}
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			// Write through config aliases after their target is available.
			if mode == "config symlink in" {
				if err := os.WriteFile(filepath.Join(checkout, "config.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = loadConfig(path)
			if (err == nil) != valid {
				t.Fatalf("valid=%t error=%v", valid, err)
			}
			if err != nil && !strings.Contains(err.Error(), "outside container workdir") {
				t.Fatalf("unexpected error=%v", err)
			}
		})
	}
}

func TestDockerConfigCannotBeCreatedInsideCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	if _, err := newAgentDockerClient(dir); err == nil {
		t.Fatal("Docker configuration created inside agent mount")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary Docker configuration not cleaned after rejection")
	}
}

func TestAgentPathExpandsLinksBeforeDotDot(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "dest/child"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	expected := filepath.Join(root, "dest", "file")
	if err := os.WriteFile(expected, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../dest/child", filepath.Join(root, "a", "link")); err != nil {
		t.Fatal(err)
	}
	start := filepath.Join(root, "start")
	if err := os.Symlink("a/link/../file", start); err != nil {
		t.Fatal(err)
	}
	got, _, err := resolveAgentPath(start)
	want, evalErr := filepath.EvalSymlinks(start)
	if err != nil || evalErr != nil || got != want {
		t.Fatalf("resolved=%q expected=%q err=%v evalErr=%v", got, want, err, evalErr)
	}
	if err := outsideWorkdir(filepath.Join(root, "dest", "child"), start); err == nil {
		t.Fatal("missed a directory traversed before '..'")
	}
}

func TestAgentPathRejectsSymlinkLoops(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("b", filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveAgentPath(filepath.Join(root, "a")); err == nil || !strings.Contains(err.Error(), "too many symbolic links") {
		t.Fatalf("error=%v", err)
	}
}
