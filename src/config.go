package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Provider        string `json:"provider"`
	TokenEnv        string `json:"token_env"`
	Repository      string `json:"repository"`
	Workdir         string `json:"workdir"`
	StateFile       string `json:"state_file"`
	PollSeconds     int    `json:"poll_seconds"`
	AgentTimeoutSec int    `json:"agent_timeout_seconds"`
	APIURL          string `json:"api_url"`
	Rules           []Rule `json:"rules"`
}

type Rule struct {
	Event          string   `json:"event"`
	Actions        []string `json:"actions"`
	Command        []string `json:"command"`
	Prompt         string   `json:"prompt"`
	CommentCommand string   `json:"comment_command"`
	CommentTarget  string   `json:"comment_target"`
	AllowedActors  []string `json:"allowed_actors"`
}

func loadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var trailing json.RawMessage
	if err := d.Decode(&trailing); err != io.EOF {
		if err != nil {
			return c, fmt.Errorf("trailing data after config: %w", err)
		}
		return c, errors.New("trailing JSON value after config")
	}
	if c.Provider == "" {
		c.Provider = "github"
	}
	if c.Provider != "github" && c.Provider != "gitlab" {
		return c, errors.New("provider must be github or gitlab")
	}
	if c.Repository == "" || strings.HasPrefix(c.Repository, "/") || strings.HasSuffix(c.Repository, "/") || strings.Contains(c.Repository, "//") || (c.Provider == "github" && len(strings.Split(c.Repository, "/")) != 2) {
		return c, errors.New("repository must be owner/name for GitHub or a GitLab project path")
	}
	if c.Workdir == "" || c.StateFile == "" {
		return c, errors.New("workdir and state_file are required")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return c, err
	}
	if !filepath.IsAbs(c.Workdir) {
		c.Workdir = filepath.Join(base, c.Workdir)
	}
	if !filepath.IsAbs(c.StateFile) {
		c.StateFile = filepath.Join(base, c.StateFile)
	}
	info, err := os.Stat(c.Workdir)
	if err != nil {
		return c, fmt.Errorf("workdir: %w", err)
	}
	if !info.IsDir() {
		return c, errors.New("workdir is not a directory")
	}
	if c.PollSeconds < 60 {
		c.PollSeconds = 60
	}
	if c.AgentTimeoutSec <= 0 {
		c.AgentTimeoutSec = 1800
	}
	if c.APIURL == "" {
		if c.Provider == "gitlab" {
			c.APIURL = "https://gitlab.com/api/v4"
		} else {
			c.APIURL = "https://api.github.com"
		}
	}
	if c.Provider == "gitlab" {
		if c.TokenEnv == "" {
			c.TokenEnv = "GITLAB_TOKEN"
		}
		u, err := url.Parse(c.APIURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/api/v4") || u.RawQuery != "" || u.Fragment != "" {
			return c, errors.New("GitLab api_url must be an absolute URL ending in /api/v4")
		}
		if os.Getenv(c.TokenEnv) == "" {
			return c, fmt.Errorf("GitLab token environment variable %s is empty", c.TokenEnv)
		}
	}
	for _, rule := range c.Rules {
		if rule.Event == "" || len(rule.Command) == 0 || rule.Prompt == "" {
			return c, errors.New("each rule needs event, command and prompt")
		}
		if err := validateCommandTemplate(rule.Command); err != nil {
			return c, fmt.Errorf("rule %q: %w", rule.Event, err)
		}
		commentEvent := rule.Event == "IssueCommentEvent" || rule.Event == "GitLabNoteEvent"
		if rule.CommentCommand != "" && (!commentEvent || len(rule.AllowedActors) == 0) {
			return c, errors.New("comment_command requires a comment event and allowed_actors")
		}
		if c.Provider == "gitlab" && rule.Event == "GitLabNoteEvent" && (rule.CommentCommand == "" || rule.CommentTarget == "") {
			return c, errors.New("GitLab note rules require comment_command and comment_target")
		}
		for _, actor := range rule.AllowedActors {
			if actor == "" || strings.TrimSpace(actor) != actor {
				return c, errors.New("allowed_actors entries must be nonempty usernames without surrounding whitespace")
			}
		}
		if rule.CommentTarget != "" && rule.CommentCommand == "" {
			return c, errors.New("comment_target requires comment_command")
		}
		if rule.CommentTarget != "" && rule.CommentTarget != "issue" && rule.CommentTarget != "pull_request" {
			return c, errors.New("comment_target must be issue or pull_request")
		}
	}
	return c, nil
}
