package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStateLockAgentHelper(t *testing.T) {
	if os.Getenv("INLETD_LOCK_TEST_AGENT") == "" {
		return
	}
	dir := os.Getenv("INLETD_LOCK_TEST_AGENT")
	f, err := os.OpenFile(filepath.Join(dir, "dispatches"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("run\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("agent was not released")
}

func TestStateLockRejectsOtherProcessesDuringDispatch(t *testing.T) {
	dir := t.TempDir()
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]Event{event("1", "IssuesEvent", "opened", 1, "Test")})
	}))
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 15,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{testBinary, "-test.run=^TestStateLockAgentHelper$"}, Prompt: "test"}}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "inletd")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build inletd: %v\n%s", err, out)
	}
	first := exec.Command(bin, "-config", configPath, "-once", "-replay-existing")
	first.Env = append(os.Environ(), "INLETD_LOCK_TEST_AGENT="+dir)
	var firstOutput bytes.Buffer
	first.Stdout, first.Stderr = &firstOutput, &firstOutput
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer first.Process.Kill()
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "dispatches")); err == nil {
			break
		}
		select {
		case err := <-firstDone:
			t.Fatalf("first daemon exited before dispatch: %v\n%s", err, firstOutput.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("first daemon did not start dispatch")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, args := range [][]string{
		{"-config", configPath, "-once", "-replay-existing"},
		{"-config", configPath, "-resolve-event", "1", "-resolution", "completed"},
		{"-config", configPath, "-acknowledge-gap"},
	} {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "already owned") {
			t.Fatalf("competing process %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first daemon: %v\n%s", err, firstOutput.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first daemon did not finish")
	}
	if out, err := exec.Command(bin, "-config", configPath, "-once").CombinedOutput(); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	dispatches, err := os.ReadFile(filepath.Join(dir, "dispatches"))
	if err != nil || string(dispatches) != "run\n" {
		t.Fatalf("dispatches = %q, %v; want exactly one", dispatches, err)
	}
}
