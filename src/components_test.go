package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleConfigsLoad(t *testing.T) {
	for _, path := range []string{
		"inletd.example.json",
		"inletd.pr-review.example.json",
		"inletd.implement.example.json",
		"inletd.fix-review.example.json",
	} {
		t.Run(path, func(t *testing.T) {
			config, err := loadConfig(filepath.Join("..", path))
			if err != nil {
				t.Fatal(err)
			}
			if !filepath.IsAbs(config.Workdir) || !filepath.IsAbs(config.StateFile) {
				t.Fatalf("paths were not resolved: %+v", config)
			}
			if config.APIURL != "https://api.github.com" || config.PollSeconds < 60 || config.AgentTimeoutSec <= 0 {
				t.Fatalf("defaults were not applied: %+v", config)
			}
		})
	}
}

func TestLoadConfigRejectsTrailingData(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trailer string
		wantErr bool
	}{
		{"whitespace", " \n\t", false},
		{"second value", `{"rules":[]}`, true},
		{"non-JSON data", " trailing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			data := `{"repository":"acme/app","workdir":".","state_file":"state.json"}` + tc.trailer
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadConfig(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("loadConfig error = %v, want error %t", err, tc.wantErr)
			}
		})
	}
}

func TestMatchRuleKeepsFirstEligibleRuleAndCorrectTarget(t *testing.T) {
	rules := []Rule{
		{Event: "IssueCommentEvent", CommentCommand: "/inletd review", AllowedActors: []string{"owner"}},
		{Event: "IssueCommentEvent", CommentCommand: "/inletd implement", CommentTarget: "issue", AllowedActors: []string{"owner"}, Prompt: "first match"},
		{Event: "IssueCommentEvent", CommentCommand: "/inletd implement", CommentTarget: "issue", AllowedActors: []string{"owner"}, Prompt: "later match"},
	}
	e := commentEvent("1", "OWNER", " /inletd implement\n", false)
	spec, err := (DeclarativeRuleRouter{Rules: rules}).Route(e)
	if err != nil {
		t.Fatal(err)
	}
	if spec == nil || spec.Rule.Prompt != "first match" || spec.Values.number != 30 || spec.Values.url != "https://github.com/acme/app/issues/30" || spec.Values.requester != "OWNER" {
		t.Fatalf("wrong match: %+v", spec)
	}
	e.Payload = json.RawMessage("{")
	if _, err := (DeclarativeRuleRouter{Rules: rules}).Route(e); err == nil {
		t.Fatal("malformed payload was accepted")
	}
}

func TestFetchUsesETagAndPollInterval(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if req.URL.Path != "/repos/acme/app/events" || req.URL.Query().Get("page") != "1" {
			t.Errorf("unexpected request: %s", req.URL)
		}
		w.Header().Set("X-Poll-Interval", "120")
		if req.Header.Get("If-None-Match") == `"current"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"current"`)
		json.NewEncoder(w).Encode([]Event{event("1", "IssuesEvent", "opened", 1, "Title")})
	}))
	defer server.Close()

	dir := t.TempDir()
	r := &Runner{Config: Config{Repository: "acme/app", APIURL: server.URL, StateFile: filepath.Join(dir, "state.json")}, Store: JSONFileStateStore{Path: filepath.Join(dir, "state.json")}, Source: NewGitHubEventSource(Config{Repository: "acme/app", APIURL: server.URL}, server.Client()), Logger: log.New(os.Stderr, "", 0)}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if r.Source.(*GitHubEventSource).etag != `"current"` || r.PollInterval.Seconds() != 120 || len(r.State.Seen) != 1 {
		t.Fatalf("first poll: ETag=%q interval=%s state=%+v", r.Source.(*GitHubEventSource).etag, r.PollInterval, r.State)
	}
	if err := r.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(r.State.Seen) != 1 {
		t.Fatalf("conditional poll: requests=%d state=%+v", requests, r.State)
	}
}

func TestGitHubEventSourceCommitsETagAfterHandling(t *testing.T) {
	conditional := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conditional <- req.Header.Get("If-None-Match")
		if req.Header.Get("If-None-Match") == `"current"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"current"`)
		json.NewEncoder(w).Encode([]Event{event("1", "IssuesEvent", "opened", 1, "Title")})
	}))
	defer server.Close()

	source := NewGitHubEventSource(Config{Repository: "acme/app", APIURL: server.URL}, server.Client())
	for range 2 {
		batch, err := source.Fetch(context.Background())
		if err != nil || len(batch.Events) != 1 {
			t.Fatalf("uncommitted fetch: batch=%+v, err=%v", batch, err)
		}
	}
	source.Commit()
	batch, err := source.Fetch(context.Background())
	if err != nil || len(batch.Events) != 0 || source.etag != `"current"` {
		t.Fatalf("conditional fetch: batch=%+v, etag=%q, err=%v", batch, source.etag, err)
	}
	headers := []string{<-conditional, <-conditional, <-conditional}
	if headers[0] != "" || headers[1] != "" || headers[2] != `"current"` {
		t.Fatalf("conditional headers: %v", headers)
	}
}

func TestAgentRendersPromptIntoArgumentsAndStdin(t *testing.T) {
	dir := t.TempDir()
	r := CommandAgentRunner{Config: Config{Workdir: dir, AgentTimeoutSec: 5}}
	rule := Rule{Command: []string{"sh", "-c", "printf '%s\\n' \"$1\" > args.txt; cat > stdin.txt", "sh", "{prompt}"}, Prompt: "Issue {number}: {title}"}
	if err := r.Run(context.Background(), event("1", "IssuesEvent", "opened", 1, "Title"), rule, eventValues{number: 1, title: "Title"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"args.txt", "stdin.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || strings.TrimSpace(string(b)) != "Issue 1: Title" {
			t.Fatalf("%s: %q, %v", name, b, err)
		}
	}
}
