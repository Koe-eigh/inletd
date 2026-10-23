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
	"strconv"
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
	r := &Runner{Config: config, Router: DeclarativeRuleRouter{Rules: config.Rules}, Store: JSONFileStateStore{Path: config.StateFile}, AgentRunner: CommandAgentRunner{Config: config}, Source: NewGitHubEventSource(config, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
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
	r2 := &Runner{Config: config, Router: DeclarativeRuleRouter{Rules: config.Rules}, Store: JSONFileStateStore{Path: config.StateFile}, AgentRunner: CommandAgentRunner{Config: config}, Source: NewGitHubEventSource(config, http.DefaultClient), Logger: r.Logger}
	var err error
	r2.State, err = (JSONFileStateStore{Path: config.StateFile}).Load()
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

func TestPollRecordsGapAfterBurstExceedsEventWindow(t *testing.T) {
	dir := t.TempDir()
	old := event("1", "IssuesEvent", "opened", 1, "Old")
	events := []Event{old}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("page"))
		start := (page - 1) * 100
		if start >= len(events) {
			json.NewEncoder(w).Encode([]Event{})
			return
		}
		end := min(start+100, len(events))
		json.NewEncoder(w).Encode(events[start:end])
	}))
	statePath := filepath.Join(dir, "state.json")
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: statePath, APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "cat >> dispatched.txt"}, Prompt: "{number}\n"}}}
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if r.State.Cursor != "1" {
		t.Fatalf("initial cursor: %q", r.State.Cursor)
	}
	// 301 newer events push the old cursor and request #2 outside the
	// API's 300-event window. Request #302 is still visible.
	for id := 302; id >= 2; id-- {
		e := Event{ID: strconv.Itoa(id), Type: "PushEvent"}
		e.Repo.Name = "acme/app"
		if id == 2 || id == 302 {
			e = event(strconv.Itoa(id), "IssuesEvent", "opened", id, "Request")
		}
		events = append(events, e)
	}
	events = events[1:]
	if err := r.Poll(context.Background(), false); err == nil || !strings.Contains(err.Error(), "event gap") {
		t.Fatalf("burst poll error = %v, want event gap", err)
	}
	if !r.State.Gap || r.State.Cursor != "302" {
		t.Fatalf("state after burst: %+v", r.State)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dispatched.txt"))
	if err != nil || string(data) != "302\n" {
		t.Fatalf("visible request dispatch = %q, %v", data, err)
	}
	restarted := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: r.Logger}
	restarted.State, err = (JSONFileStateStore{Path: statePath}).Load()
	if err != nil || !restarted.State.Gap {
		t.Fatalf("durable gap = %+v, %v", restarted.State, err)
	}
	if err := restarted.Poll(context.Background(), false); err == nil || !strings.Contains(err.Error(), "event gap") {
		t.Fatalf("restart poll error = %v, want unresolved gap", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "dispatched.txt"))
	if err != nil || string(data) != "302\n" {
		t.Fatalf("duplicate dispatch = %q, %v", data, err)
	}
}

func TestFullEventWindowWithCursorDoesNotReportGap(t *testing.T) {
	dir := t.TempDir()
	events := make([]Event, 300)
	for i := range events {
		events[i] = Event{ID: strconv.Itoa(300 - i), Type: "PushEvent"}
		events[i].Repo.Name = "acme/app"
	}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("page"))
		json.NewEncoder(w).Encode(events[(page-1)*100 : page*100])
	}))
	r := &Runner{Config: Config{Repository: "acme/app", StateFile: filepath.Join(dir, "state.json"), APIURL: server}, Router: DeclarativeRuleRouter{}, Store: JSONFileStateStore{Path: filepath.Join(dir, "state.json")}, Source: NewGitHubEventSource(Config{Repository: "acme/app", APIURL: server}, http.DefaultClient), Logger: log.New(os.Stderr, "", 0),
		State: State{Initialized: true, Cursor: "1"}}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if r.State.Gap || r.State.Cursor != "300" {
		t.Fatalf("full overlapping window: %+v", r.State)
	}
}

func TestPollMatchesRepositoryCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	matching := event("1", "IssuesEvent", "opened", 1, "Matching")
	matching.Repo.Name = "Acme/App"
	different := event("2", "IssuesEvent", "opened", 2, "Different")
	different.Repo.Name = "acme/other"
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]Event{matching, different})
	}))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "cat >> agent.txt"}, Prompt: "{number}\n"}}}
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
	for range 2 {
		if err := r.Poll(context.Background(), true); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "agent.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "1\n" {
		t.Fatalf("dispatched events: %q, want only the matching event once", b)
	}
	if !slices.Contains(r.State.Seen, matching.ID) || slices.Contains(r.State.Seen, different.ID) {
		t.Fatalf("seen events: %v", r.State.Seen)
	}
}

func TestFailedAgentRequiresReconciliationBeforeRetry(t *testing.T) {
	dir := t.TempDir()
	events := []Event{event("1", "IssuesEvent", "opened", 1, "Test")}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(events) }))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "exit 7"}, Prompt: "{number}"}}}
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), true); err == nil {
		t.Fatal("expected agent error")
	}
	if len(r.State.Seen) != 0 {
		t.Fatal("failed event marked seen")
	}
	restarted := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: r.Logger}
	var err error
	restarted.State, err = (JSONFileStateStore{Path: c.StateFile}).Load()
	if err != nil || !restarted.State.Initialized || len(restarted.State.Seen) != 0 {
		t.Fatalf("state after failed replay: %+v, %v", restarted.State, err)
	}
	if restarted.State.Jobs["1"].Status != jobRunning {
		t.Fatalf("failed job status: %+v", restarted.State.Jobs["1"])
	}
	restarted.Config.Rules[0].Command = []string{"sh", "-c", "cat > retried.txt"}
	if err := restarted.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "retried.txt")); !os.IsNotExist(err) {
		t.Fatal("running job was retried without reconciliation")
	}
	restarted.State, err = restarted.State.resolveJob("1", "retry")
	if err != nil {
		t.Fatal(err)
	}
	if err := (JSONFileStateStore{Path: c.StateFile}).Save(restarted.State); err != nil {
		t.Fatal(err)
	}
	events = nil // The original event is no longer available from GitHub.
	restarted.State, err = (JSONFileStateStore{Path: c.StateFile}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "retried.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("retry: %q, %v", b, err)
	}
}

func TestCompletedAgentWithFailedStateWriteIsNotRepeated(t *testing.T) {
	dir := t.TempDir()
	events := []Event{event("1", "IssuesEvent", "opened", 1, "Test")}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(events) }))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "echo run >> runs.txt"}, Prompt: "{number}"}}}
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
	r.Store = failingStateStore{base: r.Store, fail: func(state State) error {
		if state.Jobs["1"].Status == jobCompleted {
			return os.ErrPermission
		}
		return nil
	}}
	if err := r.Poll(context.Background(), true); err == nil {
		t.Fatal("expected completion write error")
	}
	if r.State.Jobs["1"].Status != jobRunning {
		t.Fatalf("in-memory job: %+v", r.State.Jobs["1"])
	}
	restarted := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: r.Logger}
	var err error
	restarted.State, err = (JSONFileStateStore{Path: c.StateFile}).Load()
	if err != nil || restarted.State.Jobs["1"].Status != jobRunning {
		t.Fatalf("durable job: %+v, %v", restarted.State.Jobs["1"], err)
	}
	if err := restarted.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "runs.txt"))
	if err != nil || string(data) != "run\n" {
		t.Fatalf("agent executions: %q, %v", data, err)
	}
	restarted.State, err = restarted.State.resolveJob("1", jobCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := (JSONFileStateStore{Path: c.StateFile}).Save(restarted.State); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "runs.txt"))
	if err != nil || string(data) != "run\n" {
		t.Fatalf("agent repeated after resolution: %q, %v", data, err)
	}
}

func TestRunningStateMustPersistBeforeAgentStarts(t *testing.T) {
	dir := t.TempDir()
	events := []Event{event("1", "IssuesEvent", "opened", 1, "Test")}
	server := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(events) }))
	c := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), APIURL: server, AgentTimeoutSec: 5,
		Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"sh", "-c", "echo run > runs.txt"}, Prompt: "{number}"}}}
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
	r.Store = failingStateStore{base: r.Store, fail: func(state State) error {
		if state.Jobs["1"].Status == jobRunning {
			return os.ErrPermission
		}
		return nil
	}}
	if err := r.Poll(context.Background(), true); err == nil {
		t.Fatal("expected running state write error")
	}
	if _, err := os.Stat(filepath.Join(dir, "runs.txt")); !os.IsNotExist(err) {
		t.Fatal("agent started before running state was durable")
	}
	state, err := (JSONFileStateStore{Path: c.StateFile}).Load()
	if err != nil || state.Jobs["1"].Status != jobPending {
		t.Fatalf("durable job: %+v, %v", state.Jobs["1"], err)
	}
	events = nil // A later poll must use the saved event even when the feed has moved on.
	restarted := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: r.Logger, State: state}
	if err := restarted.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs.txt")); err != nil {
		t.Fatalf("pending job was not retried: %v", err)
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
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
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
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
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
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
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
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
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
	e.Actor.Login = "owner"
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
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
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
		{"allowlist without command", Rule{Event: "IssueCommentEvent", AllowedActors: []string{"owner"}}, false},
		{"blank actor", Rule{Event: "IssueCommentEvent", AllowedActors: []string{""}}, true},
		{"actor with whitespace", Rule{Event: "IssuesEvent", AllowedActors: []string{" owner"}}, true},
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

func TestCommentFixRequestRetriesAfterReconciliation(t *testing.T) {
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
	r := &Runner{Config: c, Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: JSONFileStateStore{Path: c.StateFile}, AgentRunner: CommandAgentRunner{Config: c}, Source: NewGitHubEventSource(c, http.DefaultClient), Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), true); err == nil {
		t.Fatal("expected agent error")
	}
	if slices.Contains(r.State.Seen, "3") {
		t.Fatal("failed request was marked seen")
	}
	r.Config.Rules[0].Command = []string{"sh", "-c", "cat >> fixed.txt"}
	var err error
	r.State, err = r.State.resolveJob("3", "retry")
	if err != nil {
		t.Fatal(err)
	}
	if err := (JSONFileStateStore{Path: c.StateFile}).Save(r.State); err != nil {
		t.Fatal(err)
	}
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
	e.Actor.Login = actor
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
