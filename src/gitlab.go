package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type gitLabActivity struct {
	ID             int64  `json:"id"`
	ProjectID      int64  `json:"project_id"`
	Action         string `json:"action_name"`
	TargetIID      int    `json:"target_iid"`
	TargetType     string `json:"target_type"`
	TargetTitle    string `json:"target_title"`
	AuthorUsername string `json:"author_username"`
	Author         struct {
		Username string `json:"username"`
	} `json:"author"`
	Note struct {
		ID           int64  `json:"id"`
		Body         string `json:"body"`
		System       bool   `json:"system"`
		NoteableIID  int    `json:"noteable_iid"`
		NoteableType string `json:"noteable_type"`
		Author       struct {
			Username string `json:"username"`
		} `json:"author"`
	} `json:"note"`
}

type gitLabResource struct {
	IID    int    `json:"iid"`
	Title  string `json:"title"`
	WebURL string `json:"web_url"`
}

// GitLabEventSource polls the project activity feed. Its event IDs include the
// API instance and project, so they cannot collide with GitHub or other projects.
type GitLabEventSource struct {
	config   Config
	client   *http.Client
	interval time.Duration
}

func NewGitLabEventSource(config Config, client *http.Client) *GitLabEventSource {
	return &GitLabEventSource{config: config, client: client}
}

func (s *GitLabEventSource) Commit() {}

func (s *GitLabEventSource) get(ctx context.Context, path string, dst any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.config.APIURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", os.Getenv(s.config.TokenEnv))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "inletd")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		d := time.Duration(seconds) * time.Second
		if d > s.interval {
			s.interval = d
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitLab API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return nil, err
	}
	return resp.Header, nil
}

func (s *GitLabEventSource) Fetch(ctx context.Context) (EventBatch, error) {
	result := EventBatch{}
	project := url.PathEscape(s.config.Repository)
	all := make([]gitLabActivity, 0)
	for page := 1; page <= 3; page++ {
		var batch []gitLabActivity
		header, err := s.get(ctx, fmt.Sprintf("/projects/%s/events?per_page=100&page=%d&sort=desc", project, page), &batch)
		result.PollInterval = s.interval
		if err != nil {
			return result, err
		}
		all = append(all, batch...)
		if header.Get("X-Next-Page") == "" {
			break
		}
	}
	result.AtCapacity = len(all) == 300
	// The API returns newest activity first. Runner dispatches in reverse order.
	for _, a := range all {
		e, ok, err := s.normalize(ctx, a)
		result.PollInterval = s.interval
		if err != nil {
			return result, err
		}
		if ok {
			result.Events = append(result.Events, e)
		}
	}
	return result, nil
}

func (s *GitLabEventSource) normalize(ctx context.Context, a gitLabActivity) (Event, bool, error) {
	var e Event
	if a.ID <= 0 || a.ProjectID <= 0 {
		return e, false, nil
	}
	e.ID = fmt.Sprintf("gitlab:%s:%d:%d", strings.TrimRight(s.config.APIURL, "/"), a.ProjectID, a.ID)
	e.Provider = "gitlab"
	e.Repo.Name = s.config.Repository
	e.Actor.Login = a.Author.Username
	if e.Actor.Login == "" {
		e.Actor.Login = a.AuthorUsername
	}
	base := strings.TrimSuffix(strings.TrimRight(s.config.APIURL, "/"), "/api/v4")
	webProject := base + "/" + s.config.Repository
	payload := details{}
	switch strings.ToLower(a.TargetType) {
	case "mergerequest", "merge_request":
		if a.TargetIID <= 0 || (a.Action != "opened" && a.Action != "reopened") {
			return e, true, nil
		}
		e.Type = "GitLabMergeRequestEvent"
		payload.Action = a.Action
		payload.PullRequest.Number = a.TargetIID
		payload.PullRequest.Title = a.TargetTitle
		payload.PullRequest.HTMLURL = fmt.Sprintf("%s/-/merge_requests/%d", webProject, a.TargetIID)
		var resource gitLabResource
		_, err := s.get(ctx, fmt.Sprintf("/projects/%s/merge_requests/%d", url.PathEscape(s.config.Repository), a.TargetIID), &resource)
		if err != nil {
			return e, false, err
		}
		if resource.IID != a.TargetIID {
			return e, false, fmt.Errorf("GitLab merge request IID mismatch")
		}
		if resource.Title != "" {
			payload.PullRequest.Title = resource.Title
		}
		if resource.WebURL != "" {
			payload.PullRequest.HTMLURL = resource.WebURL
		}
	case "issue":
		if a.TargetIID <= 0 || (a.Action != "opened" && a.Action != "reopened") {
			return e, true, nil
		}
		e.Type = "GitLabIssueEvent"
		payload.Action = a.Action
		payload.Issue.Number = a.TargetIID
		payload.Issue.Title = a.TargetTitle
		payload.Issue.HTMLURL = fmt.Sprintf("%s/-/issues/%d", webProject, a.TargetIID)
	case "note":
		if a.Action != "commented on" || a.Note.System || a.Note.NoteableIID <= 0 || a.Note.ID <= 0 {
			return e, true, nil
		}
		kind := strings.ToLower(a.Note.NoteableType)
		if kind != "issue" && kind != "mergerequest" && kind != "merge_request" {
			return e, true, nil
		}
		e.Type = "GitLabNoteEvent"
		payload.Action = "created"
		payload.Issue.Number = a.Note.NoteableIID
		payload.Comment.Body = a.Note.Body
		payload.Comment.User.Login = a.Note.Author.Username
		if payload.Comment.User.Login == "" {
			return e, true, nil
		}
		resourceType, webSegment := "issues", "issues"
		if kind != "issue" {
			resourceType, webSegment = "merge_requests", "merge_requests"
			marker := json.RawMessage(`{}`)
			payload.Issue.PullRequest = &marker
		}
		payload.Issue.HTMLURL = fmt.Sprintf("%s/-/%s/%d", webProject, webSegment, a.Note.NoteableIID)
		var resource gitLabResource
		project := url.PathEscape(s.config.Repository)
		_, err := s.get(ctx, fmt.Sprintf("/projects/%s/%s/%d", project, resourceType, a.Note.NoteableIID), &resource)
		if err != nil {
			return e, false, err
		}
		if resource.IID != a.Note.NoteableIID {
			return e, false, fmt.Errorf("GitLab %s IID mismatch", resourceType)
		}
		payload.Issue.Title = resource.Title
		if resource.WebURL != "" {
			payload.Issue.HTMLURL = resource.WebURL
		}
	default:
		return e, true, nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return e, false, err
	}
	e.Payload = b
	return e, true, nil
}
