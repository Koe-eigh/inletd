package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
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

type Event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Repo struct {
		Name string `json:"name"`
	} `json:"repo"`
	Payload json.RawMessage `json:"payload"`
}

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

type State struct {
	Initialized bool     `json:"initialized"`
	Seen        []string `json:"seen"`
}

type Runner struct {
	Config       Config
	Client       *http.Client
	State        State
	Logger       *log.Logger
	PollInterval time.Duration
	ETag         string
	nextETag     string
}

func main() {
	configPath := flag.String("config", "inletd.json", "configuration file")
	once := flag.Bool("once", false, "poll once and exit")
	replay := flag.Bool("replay-existing", false, "process current events on first run")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	r := &Runner{Config: cfg, Client: &http.Client{Timeout: 20 * time.Second}, Logger: log.Default()}
	r.State, err = loadState(cfg.StateFile)
	if err != nil {
		log.Fatal(err)
	}
	if *replay && r.State.Initialized {
		log.Fatal("--replay-existing only applies before the state file is initialized")
	}
	for {
		if err := r.Poll(context.Background(), *replay); err != nil {
			log.Printf("poll failed: %v", err)
			if *once {
				os.Exit(1)
			}
		}
		if *once {
			return
		}
		*replay = false
		interval := time.Duration(cfg.PollSeconds) * time.Second
		if r.PollInterval > interval {
			interval = r.PollInterval
		}
		time.Sleep(interval)
	}
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

func loadState(path string) (State, error) {
	var s State
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func saveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".inletd-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
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
		if page == 3 {
			r.Logger.Print("warning: 300 event limit reached; events may be missed")
		}
	}
	return all, nil
}

func (r *Runner) Poll(ctx context.Context, replayExisting bool) error {
	events, err := r.fetch(ctx)
	if err != nil {
		return err
	}
	if !r.State.Initialized && !replayExisting {
		for _, e := range events {
			r.State.Seen = append(r.State.Seen, e.ID)
		}
		r.State.Initialized = true
		if err := saveState(r.Config.StateFile, r.State); err != nil {
			return err
		}
		r.ETag = r.nextETag
		r.Logger.Printf("initialized with %d existing events; waiting for new events", len(events))
		return nil
	}
	if !r.State.Initialized {
		r.State.Initialized = true
		if err := saveState(r.Config.StateFile, r.State); err != nil {
			r.State.Initialized = false
			return err
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.ID == "" || e.Repo.Name != r.Config.Repository || slices.Contains(r.State.Seen, e.ID) {
			continue
		}
		if err := r.dispatch(ctx, e); err != nil {
			return fmt.Errorf("event %s: %w", e.ID, err)
		}
		previousSeen := slices.Clone(r.State.Seen)
		r.State.Seen = append(r.State.Seen, e.ID)
		if len(r.State.Seen) > 1000 {
			r.State.Seen = r.State.Seen[len(r.State.Seen)-1000:]
		}
		if err := saveState(r.Config.StateFile, r.State); err != nil {
			r.State.Seen = previousSeen
			return err
		}
	}
	if len(events) == 0 {
		if err := saveState(r.Config.StateFile, r.State); err != nil {
			return err
		}
	}
	r.ETag = r.nextETag
	return nil
}

func (r *Runner) dispatch(ctx context.Context, e Event) error {
	var d details
	if err := json.Unmarshal(e.Payload, &d); err != nil {
		return err
	}
	number, url, title := d.Issue.Number, d.Issue.HTMLURL, d.Issue.Title
	if e.Type == "PullRequestEvent" || e.Type == "PullRequestReviewEvent" {
		number, url, title = d.PullRequest.Number, d.PullRequest.HTMLURL, d.PullRequest.Title
	}
	if number == 0 {
		number = d.Number
	}
	if url == "" && number > 0 {
		switch e.Type {
		case "PullRequestEvent", "PullRequestReviewEvent":
			url = fmt.Sprintf("https://github.com/%s/pull/%d", e.Repo.Name, number)
		case "IssuesEvent", "IssueCommentEvent":
			url = fmt.Sprintf("https://github.com/%s/issues/%d", e.Repo.Name, number)
		}
	}
	for _, rule := range r.Config.Rules {
		if rule.Event != e.Type || (len(rule.Actions) > 0 && !slices.Contains(rule.Actions, d.Action)) {
			continue
		}
		if rule.CommentCommand != "" {
			if d.Action != "created" || number <= 0 || strings.TrimSpace(d.Comment.Body) != rule.CommentCommand || !containsFold(rule.AllowedActors, d.Comment.User.Login) {
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
				url = fmt.Sprintf("https://github.com/%s/pull/%d", e.Repo.Name, number)
			}
		}
		values := strings.NewReplacer("{repo}", e.Repo.Name, "{event}", e.Type, "{action}", d.Action, "{number}", fmt.Sprint(number), "{url}", url, "{title}", title, "{requester}", d.Comment.User.Login)
		prompt := values.Replace(rule.Prompt)
		args := make([]string, len(rule.Command))
		for i, arg := range rule.Command {
			args[i] = values.Replace(strings.ReplaceAll(arg, "{prompt}", prompt))
		}
		cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(r.Config.AgentTimeoutSec)*time.Second)
		cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)
		cmd.Dir = r.Config.Workdir
		cmd.Stdin = strings.NewReader(prompt)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		r.Logger.Printf("dispatching %s %s #%d (event %s)", e.Type, d.Action, number, e.ID)
		err := cmd.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("agent failed: %w", err)
		}
		return nil // first matching rule wins
	}
	return nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
