package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitLabProjectEventsAndRecovery(t *testing.T) {
	fixture, err := os.ReadFile("testdata/gitlab_events.json")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "secret" {
			t.Errorf("missing token")
		}
		if strings.HasSuffix(r.URL.Path, "/events") {
			calls++
			w.Header().Set("Content-Type", "application/json")
			if calls == 4 {
				w.Write([]byte(`[]`))
				return
			}
			w.Write(fixture)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/9"):
			json.NewEncoder(w).Encode(gitLabResource{IID: 9, Title: "Fix bug", WebURL: "https://git.example/group/sub/app/-/issues/9"})
		case strings.HasSuffix(r.URL.Path, "/merge_requests/8"):
			json.NewEncoder(w).Encode(gitLabResource{IID: 8, Title: "Refactor", WebURL: "https://git.example/group/sub/app/-/merge_requests/8"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("GITLAB_TOKEN", "secret")
	c := Config{Provider: "gitlab", Repository: "group/sub/app", APIURL: server.URL + "/api/v4", TokenEnv: "GITLAB_TOKEN", StateFile: filepath.Join(t.TempDir(), "state.json")}
	rules := []Rule{
		{Event: "GitLabMergeRequestEvent", Actions: []string{"opened"}, Prompt: "review"},
		{Event: "GitLabNoteEvent", Actions: []string{"created"}, CommentCommand: "/inletd implement", CommentTarget: "issue", AllowedActors: []string{"owner"}, Prompt: "implement"},
		{Event: "GitLabNoteEvent", Actions: []string{"created"}, CommentCommand: "/inletd fix", CommentTarget: "pull_request", AllowedActors: []string{"owner"}, Prompt: "fix"},
	}
	c.Rules = rules
	source := NewGitLabEventSource(c, server.Client())
	batch, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 6 {
		t.Fatalf("events = %d", len(batch.Events))
	}
	if !strings.HasPrefix(batch.Events[0].ID, "gitlab:") {
		t.Fatal(batch.Events[0].ID)
	}
	for _, e := range batch.Events {
		spec, err := (DeclarativeRuleRouter{Rules: rules}).Route(e)
		if err != nil {
			t.Fatal(err)
		}
		want := e.ID == batch.Events[0].ID || strings.HasSuffix(e.ID, ":104") || strings.HasSuffix(e.ID, ":101")
		if (spec != nil) != want {
			t.Errorf("route %s: got %v want %v", e.ID, spec, want)
		}
		if spec != nil && (!strings.HasPrefix(spec.Values.url, "https://git.example/") || spec.Values.number <= 0 || spec.Values.requester == "") {
			t.Errorf("values: %+v", spec.Values)
		}
		if spec != nil && e.Type == "GitLabNoteEvent" && spec.Values.title == "" {
			t.Errorf("missing note target title: %+v", spec.Values)
		}
	}
	runs := 0
	store := JSONFileStateStore{Path: c.StateFile}
	runner := &Runner{Config: c, Source: source, Router: DeclarativeRuleRouter{Rules: rules}, Store: store, AgentRunner: fakeAgentRunner(func(_ context.Context, _ Event, _ Rule, _ eventValues) error { runs++; return nil }), Logger: testLogger(t)}
	if err := runner.Poll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if runs != 3 {
		t.Fatalf("runs = %d", runs)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Runner{Config: c, Source: NewGitLabEventSource(c, server.Client()), Router: runner.Router, Store: store, State: state, AgentRunner: runner.AgentRunner, Logger: testLogger(t)}
	if err := restarted.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if runs != 3 || calls != 3 {
		t.Fatalf("duplicate delivery: runs=%d calls=%d", runs, calls)
	}
	// A durable pending job is retried even when the API no longer returns it.
	pending := batch.Events[0]
	state.Jobs[pending.ID] = Job{Status: jobPending, EventType: pending.Type, Event: &pending}
	state.Seen = nil
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	restarted.State = state
	if err := restarted.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if runs != 4 {
		t.Fatalf("pending replay runs=%d", runs)
	}
}

func testLogger(t *testing.T) *log.Logger { return log.New(io.Discard, "", 0) }

func TestGitLabPaginationAndRateLimit(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "secret")
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/events") {
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		pages++
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
		}
		if r.URL.Query().Get("page") == "2" {
			w.Header().Set("Retry-After", "90")
		}
		if r.URL.Query().Get("page") == "3" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`[{"id":1,"project_id":77,"target_type":"Push"}]`))
	}))
	defer server.Close()
	c := Config{Provider: "gitlab", Repository: "group/app", APIURL: server.URL + "/api/v4", TokenEnv: "GITLAB_TOKEN"}
	batch, err := NewGitLabEventSource(c, server.Client()).Fetch(context.Background())
	if err != nil || pages != 2 || len(batch.Events) != 2 || batch.PollInterval.Seconds() != 90 {
		t.Fatalf("batch=%+v pages=%d err=%v", batch, pages, err)
	}
}

func TestGitLabConfigDefaultsAndNoteGuard(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "secret")
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"provider":"gitlab","repository":"group/sub/app","workdir":".","state_file":"state.json","rules":[{"event":"GitLabNoteEvent","command":["cat"],"prompt":"{url}","comment_command":"/inletd fix","comment_target":"pull_request","allowed_actors":["owner"]}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIURL != "https://gitlab.com/api/v4" || c.TokenEnv != "GITLAB_TOKEN" {
		t.Fatalf("config=%+v", c)
	}
	invalid := strings.Replace(data, `"comment_target":"pull_request",`, "", 1)
	if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("GitLab note without target accepted")
	}
}
