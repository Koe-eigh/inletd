package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerRejectsDaemonPathsInCheckout(t *testing.T) {
	for _, mode := range []string{"outside", "config inside", "state inside", "new nested state inside", "state equals checkout", "state parent symlink", "state dangling symlink", "state inside symlink out", "state directory symlink out", "state traverses checkout", "config symlink in", "config inside symlink out", "workdir symlink", "lock symlink", "sibling prefix", "unrestricted"} {
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
			valid := mode == "outside" || mode == "sibling prefix" || mode == "unrestricted"
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
