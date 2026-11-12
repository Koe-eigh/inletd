package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestGitLabIgnoredActivitiesThroughRunner(t *testing.T) {
	for _, tc := range []struct{ name, activity string }{
		{"push", `"target_type":"Push","action_name":"pushed"`},
		{"unknown type", `"target_type":"Milestone","action_name":"opened"`},
		{"closed merge request", `"target_type":"MergeRequest","action_name":"closed","target_iid":8`},
		{"merged merge request", `"target_type":"MergeRequest","action_name":"merged","target_iid":8`},
		{"missing merge request IID", `"target_type":"merge_request","action_name":"opened"`},
		{"closed issue", `"target_type":"Issue","action_name":"closed","target_iid":9`},
		{"missing issue IID", `"target_type":"Issue","action_name":"opened"`},
		{"unsupported note action", `"target_type":"Note","action_name":"updated","note":{"id":4,"noteable_iid":9,"noteable_type":"Issue","author":{"username":"owner"}}`},
		{"missing note", `"target_type":"Note","action_name":"commented on"`},
		{"system note", `"target_type":"Note","action_name":"commented on","note":{"id":4,"system":true,"noteable_iid":9,"noteable_type":"Issue","author":{"username":"owner"}}`},
		{"missing note ID", `"target_type":"Note","action_name":"commented on","note":{"noteable_iid":9,"noteable_type":"Issue","author":{"username":"owner"}}`},
		{"missing note IID", `"target_type":"Note","action_name":"commented on","note":{"id":4,"noteable_type":"Issue","author":{"username":"owner"}}`},
		{"missing note type", `"target_type":"Note","action_name":"commented on","note":{"id":4,"noteable_iid":9,"author":{"username":"owner"}}`},
		{"unsupported note target", `"target_type":"Note","action_name":"commented on","note":{"id":4,"noteable_iid":9,"noteable_type":"Commit","author":{"username":"owner"}}`},
		{"missing note author", `"target_type":"Note","action_name":"commented on","note":{"id":4,"noteable_iid":9,"noteable_type":"Issue"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Ignored activity is both interleaved and newest, so it must also be
			// retained as the checkpoint. No ignored activity should fetch a resource.
			feed := fmt.Sprintf(`[{"id":4,"project_id":77,%s},{"id":3,"project_id":77,"target_type":"Issue","action_name":"opened","target_iid":3},{"id":2,"project_id":77,%s},{"id":1,"project_id":77,"target_type":"Issue","action_name":"opened","target_iid":1}]`, tc.activity, tc.activity)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if !strings.HasSuffix(req.URL.Path, "/events") {
					t.Errorf("unexpected resource lookup: %s", req.URL.Path)
					http.NotFound(w, req)
					return
				}
				fmt.Fprint(w, feed)
			}))
			defer server.Close()
			c := Config{Provider: "gitlab", Repository: "group/app", APIURL: server.URL + "/api/v4", StateFile: filepath.Join(t.TempDir(), "state.json"), Rules: []Rule{
				{Event: "GitLabIssueEvent", Prompt: "implement"},
				{Event: "GitLabMergeRequestEvent", Prompt: "review"},
				{Event: "GitLabNoteEvent", Prompt: "note"},
			}}
			var runs []int
			var routed []string
			router := DeclarativeRuleRouter{Rules: c.Rules}
			runner := &Runner{Config: c, Source: NewGitLabEventSource(c, server.Client()), Store: JSONFileStateStore{Path: c.StateFile}, Logger: testLogger(t),
				Router: fakeRouter(func(e Event) (*ExecutionSpec, error) {
					routed = append(routed, e.ID)
					return router.Route(e)
				}),
				AgentRunner: fakeAgentRunner(func(_ context.Context, _ Event, _ Rule, v eventValues) error {
					runs = append(runs, v.number)
					return nil
				}),
			}
			if err := runner.Poll(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(runs, []int{1, 3}) || len(routed) != 4 || len(runner.State.Seen) != 4 || len(runner.State.Jobs) != 2 || !strings.HasSuffix(runner.State.Cursor, ":77:4") {
				t.Fatalf("runs=%v routed=%v state=%+v", runs, routed, runner.State)
			}
			state, err := runner.Store.Load()
			if err != nil {
				t.Fatal(err)
			}
			runner.State = state
			runner.Source = NewGitLabEventSource(c, server.Client())
			if err := runner.Poll(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(runs, []int{1, 3}) || len(routed) != 4 {
				t.Fatalf("reprocessed activities: runs=%v routed=%v", runs, routed)
			}
		})
	}
}

func TestGitLabIgnoredActivitiesPreserveGapDetection(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%t", replay), func(t *testing.T) {
			firstPoll := true
			offset := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				page, _ := strconv.Atoi(req.URL.Query().Get("page"))
				if firstPoll {
					fmt.Fprint(w, `[{"id":1,"project_id":77,"target_type":"Push"}]`)
					return
				}
				if page < 3 {
					w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
				}
				activities := make([]gitLabActivity, 100)
				for i := range activities {
					activities[i] = gitLabActivity{ID: int64(offset + 300 - (page-1)*100 - i), ProjectID: 77, TargetType: "Push"}
				}
				json.NewEncoder(w).Encode(activities)
			}))
			defer server.Close()
			c := Config{Provider: "gitlab", Repository: "group/app", APIURL: server.URL + "/api/v4", Rules: []Rule{{Event: "GitLabIssueEvent"}}}
			store := JSONFileStateStore{Path: filepath.Join(t.TempDir(), "state.json")}
			runner := &Runner{Config: c, Source: NewGitLabEventSource(c, server.Client()), Router: DeclarativeRuleRouter{Rules: c.Rules}, Store: store, Logger: testLogger(t)}
			if err := runner.Poll(context.Background(), replay); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(runner.State.Cursor, ":77:1") {
				t.Fatalf("initial cursor=%q", runner.State.Cursor)
			}
			firstPoll = false
			if err := runner.Poll(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			if runner.State.Gap || !strings.HasSuffix(runner.State.Cursor, ":77:300") {
				t.Fatalf("overlapping window: %+v", runner.State)
			}
			// An ignored cursor that really falls outside the full window must still
			// produce a durable gap, even though none of this batch is actionable.
			offset = 300
			if err := runner.Poll(context.Background(), false); err == nil || !strings.Contains(err.Error(), "event gap") {
				t.Fatalf("missing gap error: %v", err)
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !state.Gap || !strings.HasSuffix(state.Cursor, ":77:600") {
				t.Fatalf("durable gap state: %+v", state)
			}
		})
	}
}
