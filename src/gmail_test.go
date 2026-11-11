package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const testGmailKey = "a-32-byte-secret-for-gmail-bridge-test"

func signedGmailMessage(t *testing.T, id string) []byte {
	return signedGmailDelivery(t, id, "delivery-"+id, "acme/app")
}

func signedGmailDelivery(t *testing.T, id, deliveryID, repo string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"delivery_id": deliveryID, "event_type": "IssuesEvent", "repository": repo, "actor": "owner", "payload": map[string]any{"action": "opened", "issue": map[string]any{"number": 7, "title": "Test"}}})
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil || e.ID != "gmail:acme/app:delivery-one" || e.Type != "IssuesEvent" || e.Actor.Login != "owner" {
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

func TestGmailDeliveryIDValidationAndRepositoryCanonicalization(t *testing.T) {
	for _, id := range []string{"", "bad:id", "bad/id", "bad id", "bad\nline", strings.Repeat("a", 129)} {
		var msg gmailMessage
		if err := json.Unmarshal(signedGmailDelivery(t, "mail", id, "acme/app"), &msg); err != nil {
			t.Fatal(err)
		}
		if _, err := parseGmailMessage(msg, []byte(testGmailKey), "acme/app"); err == nil {
			t.Errorf("accepted delivery ID %q", id)
		}
	}
	var msg gmailMessage
	if err := json.Unmarshal(signedGmailDelivery(t, "mail", "run_12.3", "Acme/App"), &msg); err != nil {
		t.Fatal(err)
	}
	event, err := parseGmailMessage(msg, []byte(testGmailKey), "acme/app")
	if err != nil || event.ID != "gmail:acme/app:run_12.3" {
		t.Fatalf("event=%+v, err=%v", event, err)
	}
}

func TestGmailSignedDeliveryReplayAcrossMessagesAndRestart(t *testing.T) {
	t.Setenv("INLETD_GMAIL_SIGNING_KEY", testGmailKey)
	mailboxes := []string{"mail-a"}
	getCalls := map[string]int{}
	cli := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		var params map[string]any
		if err := json.Unmarshal([]byte(args[len(args)-1]), &params); err != nil {
			return nil, err
		}
		if args[3] == "list" {
			messages := make([]map[string]string, 0, len(mailboxes))
			for _, id := range mailboxes {
				messages = append(messages, map[string]string{"id": id})
			}
			return json.Marshal(map[string]any{"messages": messages})
		}
		id := params["id"].(string)
		getCalls[id]++
		delivery := "same-delivery"
		if id == "mail-c" {
			delivery = "distinct-delivery"
		}
		return signedGmailDelivery(t, id, delivery, "acme/app"), nil
	}
	cfg := Config{Provider: "gmail", Repository: "acme/app", StateFile: filepath.Join(t.TempDir(), "state.json"), Gmail: GmailConfig{CLIPath: "gws", UserID: "me", Query: "label:events", SigningKeyEnv: "INLETD_GMAIL_SIGNING_KEY"}, Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"cat"}, Prompt: "{title}"}}}
	runs := 0
	makeRunner := func() *Runner {
		return &Runner{Config: cfg, Source: NewGmailEventSource(cfg, cli), Router: DeclarativeRuleRouter{Rules: cfg.Rules}, AgentRunner: fakeAgentRunner(func(context.Context, Event, Rule, eventValues) error { runs++; return nil }), Store: JSONFileStateStore{Path: cfg.StateFile}, Logger: log.New(io.Discard, "", 0)}
	}
	r := makeRunner()
	if err := r.Poll(context.Background(), true); err != nil || runs != 1 {
		t.Fatalf("first delivery: runs=%d err=%v", runs, err)
	}
	mailboxes = []string{"mail-b", "mail-a"}
	if err := r.Poll(context.Background(), false); err != nil || runs != 1 || getCalls["mail-a"] != 1 {
		t.Fatalf("replay: runs=%d calls=%v err=%v", runs, getCalls, err)
	}
	mailboxes = []string{"mail-d", "mail-b", "mail-c"} // Original message has been deleted; mail-d is another copy.
	r = makeRunner()
	r.State, _ = r.Store.Load()
	if err := r.Poll(context.Background(), false); err != nil || runs != 2 || getCalls["mail-b"] != 1 || getCalls["mail-d"] != 1 {
		t.Fatalf("restart and distinct delivery: runs=%d calls=%v err=%v", runs, getCalls, err)
	}
	if len(r.State.Seen) != 2 || len(r.State.GmailMessages) != 4 {
		t.Fatalf("state=%+v", r.State)
	}
	// After retention expires, a fresh mailbox copy intentionally becomes eligible.
	r.State.GmailRetainedAt["gmail:acme/app:same-delivery"] = time.Now().Add(-31 * 24 * time.Hour).Unix()
	if err := r.Store.Save(r.State); err != nil {
		t.Fatal(err)
	}
	mailboxes = []string{"mail-e"}
	r = makeRunner()
	r.State, _ = r.Store.Load()
	if err := r.Poll(context.Background(), false); err != nil || runs != 3 {
		t.Fatalf("expired replay: runs=%d err=%v", runs, err)
	}
}

func TestGmailLegacyMessageIDMigratesWithoutDispatch(t *testing.T) {
	t.Setenv("INLETD_GMAIL_SIGNING_KEY", testGmailKey)
	cfg := Config{Provider: "gmail", Repository: "acme/app", StateFile: filepath.Join(t.TempDir(), "state.json"), Gmail: GmailConfig{CLIPath: "gws", UserID: "me", SigningKeyEnv: "INLETD_GMAIL_SIGNING_KEY"}, Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}, Command: []string{"cat"}, Prompt: "{title}"}}}
	cli := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[3] == "list" {
			return []byte(`{"messages":[{"id":"mail-b"},{"id":"mail-a"}]}`), nil
		}
		var params map[string]any
		if err := json.Unmarshal([]byte(args[len(args)-1]), &params); err != nil {
			return nil, err
		}
		return signedGmailDelivery(t, params["id"].(string), "old-delivery", "acme/app"), nil
	}
	runs := 0
	r := &Runner{Config: cfg, Source: NewGmailEventSource(cfg, cli), Router: DeclarativeRuleRouter{Rules: cfg.Rules}, AgentRunner: fakeAgentRunner(func(context.Context, Event, Rule, eventValues) error { runs++; return nil }), Store: JSONFileStateStore{Path: cfg.StateFile}, State: State{Initialized: true, Seen: []string{"gmail:mail-a"}}, Logger: log.New(io.Discard, "", 0)}
	if err := r.Poll(context.Background(), false); err != nil || runs != 0 || !slices.Contains(r.State.Seen, "gmail:acme/app:old-delivery") {
		t.Fatalf("migration: runs=%d state=%+v err=%v", runs, r.State, err)
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
	previousCheckpoint := next.State.GmailCheckpoint
	source := NewGmailEventSource(cfg, cli)
	next.Source = source
	if err := next.Poll(context.Background(), false); err == nil || next.PollInterval != time.Minute || len(next.State.Seen) != 2 || next.State.GmailCheckpoint != previousCheckpoint {
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

// The mock mailbox represents >1000 historical pages. A temporal constraint
// must be sent even on first startup, so those pages are never enumerated.
func TestGmailBoundedSearchAndCheckpoint(t *testing.T) {
	now := time.Unix(1800000000, 0)
	cfg := Config{Gmail: GmailConfig{Query: "label:events OR subject:bridge"}}
	source := NewGmailEventSource(cfg, nil)
	source.now = func() time.Time { return now }
	for _, checkpoint := range []int64{0, now.Add(-30 * 24 * time.Hour).Unix(), now.Add(-10 * time.Minute).Unix()} {
		source.checkpoint = checkpoint
		want := now.Add(-gmailBackfill).Unix()
		if checkpoint > now.Add(-gmailBackfill).Unix() {
			want = checkpoint - int64(gmailOverlap/time.Second)
		}
		calls := 0
		source.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
			calls++
			var params map[string]any
			if err := json.Unmarshal([]byte(args[len(args)-1]), &params); err != nil {
				return nil, err
			}
			query := params["q"].(string)
			if query != fmt.Sprintf("(%s) after:%d", cfg.Gmail.Query, want) {
				t.Fatalf("unbounded query: %s", query)
			}
			return []byte(`{}`), nil
		}
		batch, err := source.Fetch(context.Background())
		if err != nil || calls != 1 || batch.GmailCheckpoint != now.Unix() || source.checkpoint != checkpoint {
			t.Fatalf("batch=%+v checkpoint=%d calls=%d err=%v", batch, source.checkpoint, calls, err)
		}
	}
}

func TestGmailRetentionMigrationExpirationAndActiveJobs(t *testing.T) {
	now := time.Unix(1800000000, 0)
	old := now.Add(-31 * 24 * time.Hour).Unix()
	s := State{Seen: []string{"gmail:legacy", "gmail:repo:old", "gmail:repo:recent"}, GmailMessages: []string{"legacy", "old", "recent"}, GmailRetainedAt: map[string]int64{"gmail:repo:old": old, "gmail:repo:recent": now.Add(-time.Hour).Unix(), "message:old": old, "message:recent": now.Add(-time.Hour).Unix()}, Jobs: map[string]Job{"gmail:repo:old": {Status: jobCompleted}, "pending": {Status: jobPending}, "running": {Status: jobRunning}, "orphan": {Status: jobRejected}}}
	next := s.compactGmail(now)
	if slices.Contains(next.Seen, "gmail:repo:old") || slices.Contains(next.GmailMessages, "old") || len(next.Jobs) != 3 {
		t.Fatalf("expiration: %+v", next)
	}
	if next.GmailRetainedAt["gmail:legacy"] != now.Unix() || next.GmailRetainedAt["message:legacy"] != now.Unix() {
		t.Fatalf("migration: %+v", next)
	}
	// A failed write must not mutate the prior in-memory/durable snapshot.
	if len(s.Seen) != 3 || len(s.Jobs) != 4 || s.GmailRetainedAt["gmail:legacy"] != 0 {
		t.Fatal("mutated input")
	}
	later := next.compactGmail(now.Add(32 * 24 * time.Hour))
	if len(later.Seen) != 0 || len(later.GmailMessages) != 0 || len(later.GmailRetainedAt) != 0 || len(later.Jobs) != 2 {
		t.Fatalf("unbounded history: %+v", later)
	}
}

func TestGmailCheckpointRestartAndSaveFailure(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cfg := Config{Provider: "gmail", Gmail: GmailConfig{Query: "label:events"}}
	store := &memoryStateStore{}
	makeRunner := func() *Runner {
		source := NewGmailEventSource(cfg, func(_ context.Context, _ string, args ...string) ([]byte, error) { return []byte(`{}`), nil })
		source.now = func() time.Time { return now }
		return &Runner{Config: cfg, Source: source, Store: store, State: store.state, Logger: log.New(io.Discard, "", 0)}
	}
	first := makeRunner()
	if err := first.Poll(context.Background(), false); err != nil || store.state.GmailCheckpoint != now.Unix() {
		t.Fatalf("initial empty poll: state=%+v err=%v", store.state, err)
	}
	previous := store.state.GmailCheckpoint
	now = now.Add(10 * time.Minute)
	restarted := makeRunner()
	store.fail = func(s State) error {
		if s.GmailCheckpoint != previous {
			return errors.New("disk failed at checkpoint")
		}
		return nil
	}
	if err := restarted.Poll(context.Background(), false); err == nil || store.state.GmailCheckpoint != previous || restarted.State.GmailCheckpoint != previous {
		t.Fatalf("advanced failed checkpoint: %+v err=%v", restarted.State, err)
	}
	source := restarted.Source.(*GmailEventSource)
	if source.checkpoint != previous {
		t.Fatal("restart did not restore checkpoint")
	}
	store.fail = nil
	if err := restarted.Poll(context.Background(), false); err != nil || store.state.GmailCheckpoint != now.Unix() {
		t.Fatalf("retry: %+v err=%v", store.state, err)
	}
}

func TestGmailPageLimitLeavesCheckpointUncommitted(t *testing.T) {
	source := NewGmailEventSource(Config{}, nil)
	source.checkpoint = time.Now().Add(-time.Minute).Unix()
	previous := source.checkpoint
	pages := 0
	source.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		pages++
		return json.Marshal(map[string]any{"nextPageToken": fmt.Sprint(pages)})
	}
	batch, err := source.Fetch(context.Background())
	if err == nil || pages != 1000 || batch.GmailCheckpoint != 0 || source.checkpoint != previous {
		t.Fatalf("pages=%d batch=%+v err=%v", pages, batch, err)
	}
}

func TestGmailOverlapFindsOutOfOrderArrivalAfterRestart(t *testing.T) {
	t.Setenv("INLETD_GMAIL_SIGNING_KEY", testGmailKey)
	now := time.Now().Truncate(time.Second)
	lateMessageTime := now.Add(-30 * time.Minute)
	visible := false
	cfg := Config{Provider: "gmail", Repository: "acme/app", Gmail: GmailConfig{Query: "label:events", SigningKeyEnv: "INLETD_GMAIL_SIGNING_KEY"}, Rules: []Rule{{Event: "IssuesEvent", Actions: []string{"opened"}}}}
	store := &memoryStateStore{}
	runs := 0
	makeRunner := func() *Runner {
		source := NewGmailEventSource(cfg, func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[3] == "get" {
				return signedGmailMessage(t, "late"), nil
			}
			var params struct {
				Query string `json:"q"`
			}
			if err := json.Unmarshal([]byte(args[len(args)-1]), &params); err != nil {
				return nil, err
			}
			var after int64
			parts := strings.Split(params.Query, " after:")
			if len(parts) != 2 {
				t.Fatalf("missing time bound: %s", params.Query)
			}
			if _, err := fmt.Sscan(parts[1], &after); err != nil {
				return nil, err
			}
			if visible && after < lateMessageTime.Unix() {
				return []byte(`{"messages":[{"id":"late"}]}`), nil
			}
			return []byte(`{}`), nil
		})
		source.now = func() time.Time { return now }
		return &Runner{Config: cfg, Source: source, Router: DeclarativeRuleRouter{Rules: cfg.Rules}, AgentRunner: fakeAgentRunner(func(context.Context, Event, Rule, eventValues) error { runs++; return nil }), Store: store, State: store.state, Logger: log.New(io.Discard, "", 0)}
	}
	first := makeRunner()
	if err := first.Poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	visible = true // Search indexing exposes mail older than the saved checkpoint.
	restarted := makeRunner()
	if err := restarted.Poll(context.Background(), false); err != nil || runs != 1 {
		t.Fatalf("missed delayed mail: runs=%d err=%v", runs, err)
	}
}
