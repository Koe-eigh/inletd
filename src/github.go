package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Repo struct {
		Name string `json:"name"`
	} `json:"repo"`
	Payload json.RawMessage `json:"payload"`
}

// GitHubEventSource owns GitHub HTTP polling and its conditional request state.
type GitHubEventSource struct {
	config   Config
	client   *http.Client
	etag     string
	nextETag string
}

func NewGitHubEventSource(config Config, client *http.Client) *GitHubEventSource {
	return &GitHubEventSource{config: config, client: client}
}

func (s *GitHubEventSource) Commit() {
	s.etag = s.nextETag
}

func (s *GitHubEventSource) Fetch(ctx context.Context) (EventBatch, error) {
	var result EventBatch
	s.nextETag = s.etag
	var all []Event
	for page := 1; page <= 3; page++ {
		url := fmt.Sprintf("%s/repos/%s/events?per_page=100&page=%d", strings.TrimRight(s.config.APIURL, "/"), s.config.Repository, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return result, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "inletd-mvp")
		if page == 1 && s.etag != "" {
			req.Header.Set("If-None-Match", s.etag)
		}
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return result, err
		}
		if seconds, err := strconv.Atoi(resp.Header.Get("X-Poll-Interval")); err == nil && seconds > 0 {
			interval := time.Duration(seconds) * time.Second
			if interval > result.PollInterval {
				result.PollInterval = interval
			}
		}
		if page == 1 {
			if resp.StatusCode == http.StatusNotModified {
				resp.Body.Close()
				return result, nil
			}
			s.nextETag = resp.Header.Get("ETag")
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return result, err
		}
		if resp.StatusCode != http.StatusOK {
			return result, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var batch []Event
		if err := json.Unmarshal(body, &batch); err != nil {
			return result, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	result.Events = all
	result.AtCapacity = len(all) == 300
	return result, nil
}
