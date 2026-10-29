package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testGmailKey = "a-32-byte-secret-for-gmail-bridge-test"

func signedGmailMessage(t *testing.T, id string) []byte {
	t.Helper()
	body := []byte(`{"delivery_id":"delivery-` + id + `","event_type":"IssuesEvent","repository":"acme/app","actor":"owner","payload":{"action":"opened","issue":{"number":7,"title":"Test"}}}`)
	mac := hmac.New(sha256.New, []byte(testGmailKey))
	mac.Write(body)
	b, _ := json.Marshal(map[string]any{"id": id, "payload": map[string]any{"mimeType": "multipart/alternative", "headers": []map[string]string{{"name": "X-Inletd-Signature-SHA256", "value": "sha256=" + hex.EncodeToString(mac.Sum(nil))}}, "parts": []any{map[string]any{"mimeType": "text/plain", "body": map[string]string{"data": base64.RawURLEncoding.EncodeToString(body)}}}}})
	return b
}

func TestGmailParseRejectsUntrustedAndMalformedMessages(t *testing.T) {
	var msg gmailMessage
	if err := json.Unmarshal(signedGmailMessage(t, "one"), &msg); err != nil {
		t.Fatal(err)
	}
	e, err := parseGmailMessage(msg, []byte(testGmailKey), "acme/app")
	if err != nil || e.ID != "gmail:one" || e.Type != "IssuesEvent" || e.Actor.Login != "owner" {
		t.Fatalf("event=%+v, err=%v", e, err)
	}
	if _, err := parseGmailMessage(msg, []byte(testGmailKey), "other/repo"); err == nil {
		t.Fatal("accepted wrong repository")
	}
	msg.Payload.Parts[0].Body.Data = base64.RawURLEncoding.EncodeToString([]byte(`{"event_type":"IssuesEvent"}`))
	if _, err := parseGmailMessage(msg, []byte(testGmailKey), "acme/app"); err == nil {
		t.Fatal("accepted tampered body")
	}
	msg.Payload.Parts[0].Body.Data = "not-base64!"
	if _, err := parseGmailMessage(msg, []byte(testGmailKey), "acme/app"); err == nil {
		t.Fatal("accepted malformed body")
	}
}

func TestGmailPaginationRetryAndRestartDedup(t *testing.T) {
	t.Setenv("INLETD_GMAIL_SIGNING_KEY", testGmailKey)
	calls := map[string]int{}
	failGet := false
	cli := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		var params map[string]any
		if err := json.Unmarshal([]byte(args[len(args)-1]), &params); err != nil {
			return nil, err
		}
		method := args[3]
		if method == "list" {
			if params["pageToken"] == nil {
				return []byte(`{"messages":[{"id":"two"}],"nextPageToken":"page2"}`), nil
			}
			return []byte(`{"messages":[{"id":"one"}]}`), nil
		}
		id := params["id"].(string)
		calls[id]++
		if id == "three" && failGet {
			return nil, errors.New("429 rate limit")
		}
		return signedGmailMessage(t, id), nil
	}
	cfg := Config{Provider: "gmail", Repository: "acme/app", StateFile: filepath.Join(t.TempDir(), "state.json"), Gmail: GmailConfig{CLIPath: "gws", UserID: "me", Query: "label:github-events", SigningKeyEnv: "INLETD_GMAIL_SIGNING_KEY"}, Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"cat"}, Prompt: "{title}"}}}
	runs := 0
	makeRunner := func() *Runner {
		return &Runner{Config: cfg, Source: NewGmailEventSource(cfg, cli), Router: DeclarativeRuleRouter{Rules: cfg.Rules}, AgentRunner: fakeAgentRunner(func(context.Context, Event, Rule, eventValues) error { runs++; return nil }), Store: JSONFileStateStore{Path: cfg.StateFile}, Logger: log.New(io.Discard, "", 0)}
	}
	first := makeRunner()
	if err := first.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || len(first.State.Seen) != 2 {
		t.Fatalf("first poll runs=%d state=%+v", runs, first.State)
	}
	next := makeRunner()
	next.State, _ = next.Store.Load()
	if err := next.Poll(context.Background(), false); err != nil || calls["one"] != 1 || calls["two"] != 1 {
		t.Fatalf("restart err=%v calls=%v", err, calls)
	}
	// New message arrives on page one, then a transient failure must leave it unprocessed.
	oldCLI := cli
	cli = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if args[3] == "list" && !strings.Contains(args[len(args)-1], "pageToken") {
			return []byte(`{"messages":[{"id":"three"},{"id":"two"}],"nextPageToken":"page2"}`), nil
		}
		return oldCLI(ctx, path, args...)
	}
	failGet = true
	source := NewGmailEventSource(cfg, cli)
	next.Source = source
	if err := next.Poll(context.Background(), false); err == nil || next.PollInterval != time.Minute || len(next.State.Seen) != 2 {
		t.Fatalf("retry state=%+v interval=%v err=%v", next.State, next.PollInterval, err)
	}
	failGet = false
	if err := next.Poll(context.Background(), false); err != nil || runs != 1 || len(next.State.Seen) != 3 {
		t.Fatalf("ingest runs=%d state=%+v err=%v", runs, next.State, err)
	}
	restarted := makeRunner()
	restarted.State, _ = restarted.Store.Load()
	if err := restarted.Poll(context.Background(), false); err != nil || runs != 1 {
		t.Fatalf("duplicate after restart runs=%d err=%v", runs, err)
	}
}

func TestGmailConfigRequiresSigningKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":"gmail","repository":"acme/app","workdir":".","state_file":"state.json","gmail":{"query":"label:github-events"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("accepted missing key")
	}
	t.Setenv("INLETD_GMAIL_SIGNING_KEY", testGmailKey)
	cfg, err := loadConfig(path)
	if err != nil || cfg.Gmail.CLIPath != "gws" || cfg.Gmail.UserID != "me" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}

func TestGmailBackoffAndAuthFailure(t *testing.T) {
	source := NewGmailEventSource(Config{Gmail: GmailConfig{CLIPath: "gws", UserID: "me", Query: "label:events"}}, func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("429 rate limit")
	})
	for _, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		batch, err := source.Fetch(context.Background())
		if err == nil || batch.PollInterval != delay {
			t.Fatalf("rate limit: interval=%v err=%v", batch.PollInterval, err)
		}
	}
	source.run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("401 unauthorized") }
	batch, err := source.Fetch(context.Background())
	if err == nil || batch.PollInterval != 15*time.Minute {
		t.Fatalf("auth failure: interval=%v err=%v", batch.PollInterval, err)
	}
}
