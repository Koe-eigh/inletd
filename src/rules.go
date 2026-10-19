package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type details struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	Issue  struct {
		Number      int              `json:"number"`
		HTMLURL     string           `json:"html_url"`
		Title       string           `json:"title"`
		PullRequest *json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
	} `json:"pull_request"`
}

type eventValues struct {
	action    string
	number    int
	url       string
	title     string
	requester string
}

// DeclarativeRuleRouter selects the first eligible configured rule.
type DeclarativeRuleRouter struct {
	Rules []Rule
}

func (r DeclarativeRuleRouter) Route(e Event) (*ExecutionSpec, error) {
	rule, values, err := matchRule(e, r.Rules)
	if err != nil || rule == nil {
		return nil, err
	}
	return &ExecutionSpec{Rule: *rule, Values: values}, nil
}

func matchRule(e Event, rules []Rule) (*Rule, eventValues, error) {
	var d details
	if err := json.Unmarshal(e.Payload, &d); err != nil {
		return nil, eventValues{}, err
	}
	values := eventValues{action: d.Action, number: d.Issue.Number, url: d.Issue.HTMLURL, title: d.Issue.Title, requester: d.Comment.User.Login}
	if e.Type == "PullRequestEvent" || e.Type == "PullRequestReviewEvent" {
		values.number, values.url, values.title = d.PullRequest.Number, d.PullRequest.HTMLURL, d.PullRequest.Title
	}
	if values.number == 0 {
		values.number = d.Number
	}
	if values.url == "" && values.number > 0 {
		switch e.Type {
		case "PullRequestEvent", "PullRequestReviewEvent":
			values.url = fmt.Sprintf("https://github.com/%s/pull/%d", e.Repo.Name, values.number)
		case "IssuesEvent", "IssueCommentEvent":
			values.url = fmt.Sprintf("https://github.com/%s/issues/%d", e.Repo.Name, values.number)
		}
	}
	for i := range rules {
		rule := &rules[i]
		if rule.Event != e.Type || (len(rule.Actions) > 0 && !slices.Contains(rule.Actions, d.Action)) {
			continue
		}
		if rule.CommentCommand != "" {
			if d.Action != "created" || values.number <= 0 || strings.TrimSpace(d.Comment.Body) != rule.CommentCommand || !containsFold(rule.AllowedActors, values.requester) {
				continue
			}
			target := rule.CommentTarget
			if target == "" {
				target = "pull_request"
			}
			if (target == "pull_request") != (d.Issue.PullRequest != nil) {
				continue
			}
			if target == "pull_request" {
				values.url = fmt.Sprintf("https://github.com/%s/pull/%d", e.Repo.Name, values.number)
			}
		}
		return rule, values, nil
	}
	return nil, values, nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
