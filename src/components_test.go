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
	config, err := loadConfig(filepath.Join("..", "inletd.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(config.Workdir) || !filepath.IsAbs(config.StateFile) {
		t.Fatalf("paths were not resolved: %+v", config)
	}
	if config.APIURL != "https://api.github.com" || config.PollSeconds < 60 || config.AgentTimeoutSec <= 0 {
		t.Fatalf("defaults were not applied: %+v", config)
	}
	for _, tc := range []struct {
		name    string
		event   Event
		command string
	}{
		{"automatic review", pullRequestEvent("opened"), ""},
		{"requested review", commentEvent("1", "YOUR_LOGIN", "/inletd review", true), "/inletd review"},
		{"issue implementation", commentEvent("2", "YOUR_LOGIN", "/inletd implement", false), "/inletd implement"},
		{"review feedback", commentEvent("3", "YOUR_LOGIN", "/inletd fix", true), "/inletd fix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := (DeclarativeRuleRouter{Rules: config.Rules}).Route(tc.event)
			if err != nil || spec == nil || spec.Rule.CommentCommand != tc.command {
				t.Fatalf("route = %+v, error = %v", spec, err)
			}
		})
	}
	for _, e := range []Event{
		commentEvent("4", "OTHER_LOGIN", "/inletd review", true),
		commentEvent("5", "YOUR_LOGIN", "/inletd review", false),
		commentEvent("6", "YOUR_LOGIN", "/inletd implement", true),
		commentEvent("7", "YOUR_LOGIN", "/inletd fix", false),
	} {
		spec, err := (DeclarativeRuleRouter{Rules: config.Rules}).Route(e)
		if err != nil || spec != nil {
			t.Fatalf("unexpected route for event %s: %+v, error = %v", e.ID, spec, err)
		}
	}
}

func pullRequestEvent(action string) Event {
	e := Event{ID: "pr", Type: "PullRequestEvent"}
	e.Repo.Name = "acme/app"
	e.Payload, _ = json.Marshal(map[string]any{"action": action, "pull_request": map[string]any{"number": 30}})
	return e
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

func TestAllowedActorsApplyToEveryMatchingRule(t *testing.T) {
	for _, tc := range []struct {
		name          string
		eventType     string
		actor         string
		commentAuthor string
		command       string
		wantMatch     bool
	}{
		{"comment without command allowed", "IssueCommentEvent", "OWNER", "owner", "", true},
		{"comment without command denied", "IssueCommentEvent", "outsider", "outsider", "", false},
		{"comment without command missing actor", "IssueCommentEvent", "", "owner", "", false},
		{"comment without command conflicting author", "IssueCommentEvent", "owner", "outsider", "", false},
		{"comment without command missing author", "IssueCommentEvent", "owner", "", "", false},
		{"comment command allowed", "IssueCommentEvent", "owner", "OWNER", "/run", true},
		{"comment command denied", "IssueCommentEvent", "outsider", "outsider", "/run", false},
		{"comment command conflicting author", "IssueCommentEvent", "owner", "outsider", "/run", false},
		{"issue action allowed", "IssuesEvent", "owner", "", "", true},
		{"issue action denied", "IssuesEvent", "outsider", "", "", false},
		{"PR action allowed", "PullRequestEvent", "owner", "", "", true},
		{"PR action denied", "PullRequestEvent", "outsider", "", "", false},
		{"review action allowed", "PullRequestReviewEvent", "owner", "", "", true},
		{"review action denied", "PullRequestReviewEvent", "outsider", "", "", false},
		{"review action missing actor", "PullRequestReviewEvent", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Event{Type: tc.eventType}
			e.Actor.Login = tc.actor
			e.Repo.Name = "acme/app"
			payload := map[string]any{"action": "opened", "issue": map[string]any{"number": 30, "user": map[string]any{"login": "owner"}}, "pull_request": map[string]any{"number": 30, "user": map[string]any{"login": "owner"}}}
			if tc.eventType == "IssueCommentEvent" {
				payload["action"] = "created"
				payload["comment"] = map[string]any{"body": "/run", "user": map[string]any{"login": tc.commentAuthor}}
			}
			e.Payload, _ = json.Marshal(payload)
			rule := Rule{Event: tc.eventType, AllowedActors: []string{"owner"}, CommentCommand: tc.command}
			if tc.command != "" {
				rule.CommentTarget = "issue"
			}
			spec, err := (DeclarativeRuleRouter{Rules: []Rule{rule}}).Route(e)
			if err != nil || (spec != nil) != tc.wantMatch {
				t.Fatalf("route = %+v, error = %v, want match %t", spec, err, tc.wantMatch)
			}
			if spec != nil && spec.Values.requester != tc.actor {
				t.Fatalf("requester = %q, want %q", spec.Values.requester, tc.actor)
			}
		})
	}

	e := commentEvent("public", "", "any comment", false)
	rule := Rule{Event: "IssueCommentEvent"}
	if spec, err := (DeclarativeRuleRouter{Rules: []Rule{rule}}).Route(e); err != nil || spec == nil {
		t.Fatalf("unrestricted rule must remain eligible: %+v, %v", spec, err)
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
	title := "$(touch injected); 'quoted'"
	if err := r.Run(context.Background(), event("1", "IssuesEvent", "opened", 1, title), rule, eventValues{number: 1, title: title}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"args.txt", "stdin.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || strings.TrimSpace(string(b)) != "Issue 1: "+title {
			t.Fatalf("%s: %q, %v", name, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
		t.Fatalf("event title executed as shell code: %v", err)
	}
}

func TestAgentRejectsEventPlaceholdersInShellScripts(t *testing.T) {
	for _, command := range [][]string{
		{"sh", "-c", "echo {title}"},
		{"/bin/bash", "-lc", "echo '{prompt}'"},
		{"dash", "-ec", "echo {url}"},
		{"{title}", "safe"},
	} {
		dir := t.TempDir()
		rule := Rule{Event: "IssuesEvent", Command: command, Prompt: "Issue {title}"}
		config := Config{Repository: "acme/app", Workdir: dir, StateFile: filepath.Join(dir, "state.json"), Rules: []Rule{rule}}
		b, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Errorf("loadConfig accepted unsafe command %q", command)
		}
		r := CommandAgentRunner{Config: Config{Workdir: dir, AgentTimeoutSec: 5}}
		if err := r.Run(context.Background(), event("1", "IssuesEvent", "opened", 1, "$(touch injected)"), rule, eventValues{title: "$(touch injected)"}); err == nil {
			t.Errorf("Run accepted unsafe command %q", command)
		}
		if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
			t.Fatalf("unsafe command executed: %v", err)
		}
	}
}
