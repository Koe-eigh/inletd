package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
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
	if c.Repository == "" || len(strings.Split(c.Repository, "/")) != 2 {
		return c, errors.New("repository must be owner/name")
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
		c.APIURL = "https://api.github.com"
	}
	for _, rule := range c.Rules {
		if rule.Event == "" || len(rule.Command) == 0 || rule.Prompt == "" {
			return c, errors.New("each rule needs event, command and prompt")
		}
		if rule.CommentCommand != "" && (rule.Event != "IssueCommentEvent" || len(rule.AllowedActors) == 0) {
			return c, errors.New("comment_command requires IssueCommentEvent and allowed_actors")
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
