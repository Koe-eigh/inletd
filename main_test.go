package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPollDispatchAndPersistence(t *testing.T) {
	dir := t.TempDir()
	var events []Event
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/repos/acme/app/events" {
			t.Errorf("path: %s", req.URL.Path)
		}
		json.NewEncoder(w).Encode(events)
	}))
	output := filepath.Join(dir, "agent.txt")
	// The shell is only a test agent. Production commands are launched without a shell.
	config := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "cat >> agent.txt"}, Prompt: "Implement {repo} issue #{number}: {title} {url}"}}}
	r := &Runner{Config: config, Client: http.DefaultClient, Logger: log.New(os.Stderr, "", 0)}
	old := event("1", "IssuesEvent", "opened", 1, "Old")
	events = []Event{old}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("initial event was dispatched")
	}
	fresh := event("2", "IssuesEvent", "opened", 2, "New")
	events = []Event{fresh, old}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	r2 := &Runner{Config: config, Client: http.DefaultClient, Logger: r.Logger}
	var err error
	r2.State, err = loadState(config.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "Implement acme/app issue #2: New https://example.test/issues/2" {
		t.Fatalf("agent input: %q", b)
	}
}

func TestFailedAgentRetries(t *testing.T) {
	dir := t.TempDir()
	events := []Event{event("1", "IssuesEvent", "opened", 1, "Test")}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(events) }))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "exit 7"}, Prompt: "{number}"}}}
	r := &Runner{Config: c, Client: http.DefaultClient, Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), true); err == nil {
		t.Fatal("expected agent error")
	}
	if len(r.State.Seen) != 0 {
		t.Fatal("failed event marked seen")
	}
	restarted := &Runner{Config: c, Client: http.DefaultClient, Logger: r.Logger}
	var err error
	restarted.State, err = loadState(c.StateFile)
	if err != nil || !restarted.State.Initialized || len(restarted.State.Seen) != 0 {
		t.Fatalf("state after failed replay: %+v, %v", restarted.State, err)
	}
	restarted.Config.Rules[0].Command = []string{"sh", "-c", "cat > retried.txt"}
	if err := restarted.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "retried.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("retry: %q, %v", b, err)
	}
}

func TestStateWriteFailureDoesNotMarkEventSeenInMemory(t *testing.T) {
	dir := t.TempDir()
	events := []Event{event("1", "IssuesEvent", "opened", 1, "Old")}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(events)
	}))
	statePath := filepath.Join(dir, "state.json")
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: statePath, APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "cat > agent.txt"}, Prompt: "{number}"}}}
	r := &Runner{Config: c, Client: http.DefaultClient, Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	events = []Event{event("2", "IssuesEvent", "opened", 2, "New"), events[0]}
	if err := r.Poll(context.Background(), false); err == nil {
		t.Fatal("expected state write failure")
	}
	if slices.Contains(r.State.Seen, "2") {
		t.Fatal("event marked seen after state write failure")
	}
}

func TestSparsePullRequestEventGetsURL(t *testing.T) {
	dir := t.TempDir()
	e := Event{ID: "29", Type: "PullRequestEvent"}
	e.Repo.Name = "acme/app"
	e.Payload = json.RawMessage(`{"action":"opened","number":29,"pull_request":{"number":29}}`)
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]Event{e})
	}))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "PullRequestEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "cat > prompt.txt"}, Prompt: "{url}"}}}
	r := &Runner{Config: c, Client: http.DefaultClient, Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "https://github.com/acme/app/pull/29" {
		t.Fatalf("prompt: %q", b)
	}
}

func TestCommentReviewRequestRequiresPRCommandAndAllowedActor(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		commentEvent("1", "Koe-eigh", "/inletd review", false),
		commentEvent("2", "stranger", "/inletd review", true),
		commentEvent("3", "Koe-eigh", "please /inletd review", true),
		commentEvent("4", "koe-EIGH", "  /inletd review\n", true),
	}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(events)
	}))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssueCommentEvent", Actions: []string{"created"}, CommentCommand: "/inletd review", AllowedActors: []string{"Koe-eigh"}, Command: []string{"sh", "-c", "cat > prompt.txt"}, Prompt: "{requester} {url}"}}}
	r := &Runner{Config: c, Client: http.DefaultClient, Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "koe-EIGH https://github.com/acme/app/pull/30" {
		t.Fatalf("prompt: %q", b)
	}
}

func TestPRReviewWorkflowDispatchesOpeningAndAuthorizedRequest(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		prEvent("1", "opened", 42),
		prEvent("2", "reopened", 43),
		prEvent("3", "closed", 44),
		commentEvent("4", "stranger", "/inletd review", true),
		commentEvent("5", "owner", "/inletd review", false),
		commentEvent("6", "owner", "/inletd review", true),
	}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(events)
	}))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{
			{Event: "PullRequestEvent", Actions: []string{"opened", "reopened"}, Command: []string{"sh", "-c", "cat >> reviews.txt"}, Prompt: "{event} {action} #{number} {url}\n"},
			{Event: "IssueCommentEvent", Actions: []string{"created"}, CommentCommand: "/inletd review", AllowedActors: []string{"owner"}, Command: []string{"sh", "-c", "cat >> reviews.txt"}, Prompt: "{requester} #{number} {url}\n"},
		}}
	r := &Runner{Config: c, Client: http.DefaultClient, Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "reviews.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "owner #30 https://github.com/acme/app/pull/30\nPullRequestEvent reopened #43 https://github.com/acme/app/pull/43\nPullRequestEvent opened #42 https://github.com/acme/app/pull/42\n"
	if string(b) != want {
		t.Fatalf("dispatches: %q, want %q", b, want)
	}
}

func prEvent(id, action string, number int) Event {
	e := Event{ID: id, Type: "PullRequestEvent"}
	e.Repo.Name = "acme/app"
	e.Payload, _ = json.Marshal(map[string]any{"action": action, "number": number, "pull_request": map[string]any{"number": number}})
	return e
}

func TestCommentImplementationRequestRequiresIssueCommandAndAllowedActor(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		commentEvent("1", "owner", "/inletd implement", true),
		commentEvent("2", "stranger", "/inletd implement", false),
		commentEvent("3", "owner", "please /inletd implement", false),
		commentEvent("4", "OWNER", "  /inletd implement\n", false),
	}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(events)
	}))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssueCommentEvent", Actions: []string{"created"}, CommentCommand: "/inletd implement", CommentTarget: "issue", AllowedActors: []string{"owner"}, Command: []string{"sh", "-c", "cat >> prompt.txt"}, Prompt: "{requester} {repo} #{number} {url}"}}}
	r := &Runner{Config: c, Client: http.DefaultClient, Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "OWNER acme/app #30 https://github.com/acme/app/issues/30" {
		t.Fatalf("agent input or replay: %q", b)
	}
}

func TestCommentTargetValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rule    Rule
		wantErr bool
	}{
		{"issue", Rule{Event: "IssueCommentEvent", CommentCommand: "/inletd implement", CommentTarget: "issue", AllowedActors: []string{"owner"}}, false},
		{"legacy PR default", Rule{Event: "IssueCommentEvent", CommentCommand: "/inletd review", AllowedActors: []string{"owner"}}, false},
		{"invalid target", Rule{Event: "IssueCommentEvent", CommentCommand: "/inletd implement", CommentTarget: "discussion", AllowedActors: []string{"owner"}}, true},
		{"target without command", Rule{Event: "IssueCommentEvent", CommentTarget: "issue"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.rule.Command = []string{"echo"}
			tc.rule.Prompt = "test"
			config := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), Rules: []Rule{tc.rule}}
			b, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = loadConfig(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("loadConfig error = %v, want error %t", err, tc.wantErr)
			}
		})
	}
}

func TestCommentFixRequestRetriesAfterAgentFailure(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		commentEvent("1", "owner", "/inletd fix", false),
		commentEvent("2", "stranger", "/inletd fix", true),
		commentEvent("3", "owner", "/inletd fix", true),
	}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(events)
	}))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssueCommentEvent", Actions: []string{"created"}, CommentCommand: "/inletd fix", AllowedActors: []string{"owner"}, Command: []string{"sh", "-c", "exit 7"}, Prompt: "Fix {url}"}}}
	r := &Runner{Config: c, Client: http.DefaultClient, Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), true); err == nil {
		t.Fatal("expected agent error")
	}
	if slices.Contains(r.State.Seen, "3") {
		t.Fatal("failed request was marked seen")
	}
	r.Config.Rules[0].Command = []string{"sh", "-c", "cat >> fixed.txt"}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "fixed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "Fix https://github.com/acme/app/pull/30" {
		t.Fatalf("fix requests: %q", b)
	}
}

func commentEvent(id, actor, body string, pr bool) Event {
	e := Event{ID: id, Type: "IssueCommentEvent"}
	e.Repo.Name = "acme/app"
	issue := map[string]any{"number": 30}
	if pr {
		issue["pull_request"] = map[string]any{"url": "https://api.github.com/repos/acme/app/pulls/30"}
	}
	e.Payload, _ = json.Marshal(map[string]any{"action": "created", "issue": issue, "comment": map[string]any{"body": body, "user": map[string]any{"login": actor}}})
	return e
}

func event(id, kind, action string, number int, title string) Event {
	e := Event{ID: id, Type: kind}
	e.Repo.Name = "acme/app"
	e.Payload, _ = json.Marshal(map[string]any{"action": action, "issue": map[string]any{"number": number, "title": title, "html_url": "https://example.test/issues/" + id}})
	return e
}

func testServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return "http://" + listener.Addr().String()
}
