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

func (r *Runner) fetch(ctx context.Context) ([]Event, error) {
	var all []Event
	for page := 1; page <= 3; page++ {
		url := fmt.Sprintf("%s/repos/%s/events?per_page=100&page=%d", strings.TrimRight(r.Config.APIURL, "/"), r.Config.Repository, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "inletd-mvp")
		if page == 1 && r.ETag != "" {
			req.Header.Set("If-None-Match", r.ETag)
		}
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := r.Client.Do(req)
		if err != nil {
			return nil, err
		}
		if seconds, err := strconv.Atoi(resp.Header.Get("X-Poll-Interval")); err == nil && seconds > 0 {
			interval := time.Duration(seconds) * time.Second
			if interval > r.PollInterval {
				r.PollInterval = interval
			}
		}
		if page == 1 {
			if resp.StatusCode == http.StatusNotModified {
				resp.Body.Close()
				return nil, nil
			}
			r.nextETag = resp.Header.Get("ETag")
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var batch []Event
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}
