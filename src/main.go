package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "inletd.json", "configuration file")
	once := flag.Bool("once", false, "poll once and exit")
	replay := flag.Bool("replay-existing", false, "process current events on first run")
	resolveEvent := flag.String("resolve-event", "", "event ID of a running job to reconcile")
	resolution := flag.String("resolution", "", "completed or retry (requires -resolve-event)")
	acknowledgeGap := flag.Bool("acknowledge-gap", false, "clear an event gap after checking for missed requests")
	flag.Parse()
	if (*resolveEvent == "") != (*resolution == "") {
		log.Fatal("-resolve-event and -resolution must be used together")
	}
	if *acknowledgeGap && (*resolveEvent != "" || *replay) {
		log.Fatal("-acknowledge-gap cannot be combined with -resolve-event or -replay-existing")
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	var source EventSource = NewGitHubEventSource(cfg, &http.Client{Timeout: 20 * time.Second})
	if cfg.Provider == "gitlab" {
		source = NewGitLabEventSource(cfg, &http.Client{Timeout: 20 * time.Second})
	} else if cfg.Provider == "gmail" {
		source = NewGmailEventSource(cfg, nil)
	}
	r := &Runner{Config: cfg, Source: source, Router: DeclarativeRuleRouter{Rules: cfg.Rules}, AgentRunner: CommandAgentRunner{Config: cfg}, Store: JSONFileStateStore{Path: cfg.StateFile}, Logger: log.Default()}
	r.State, err = r.Store.Load()
	if err != nil {
		log.Fatal(err)
	}
	sourceID := cfg.Provider + ":" + cfg.APIURL + ":" + cfg.Repository
	if cfg.Provider == "gmail" {
		sourceID = "gmail:" + cfg.Gmail.UserID + ":" + cfg.Gmail.Query + ":" + cfg.Repository
	}
	if r.State.Source != "" && r.State.Source != sourceID {
		log.Fatal("state file belongs to another provider or project; use a separate state_file")
	}
	if r.State.Source == "" && cfg.Provider != "github" && (r.State.Initialized || len(r.State.Jobs) > 0 || len(r.State.Seen) > 0) {
		log.Fatal("existing state file has no provider identity; use a separate state_file for this provider")
	}
	r.State.Source = sourceID
	if *resolveEvent != "" {
		next, err := r.State.resolveJob(*resolveEvent, *resolution)
		if err != nil {
			log.Fatal(err)
		}
		if err := r.persist(next); err != nil {
			log.Fatal(err)
		}
		log.Printf("event %s resolved as %s", *resolveEvent, *resolution)
		return
	}
	if *acknowledgeGap {
		if !r.State.Gap {
			log.Fatal("no event gap to acknowledge")
		}
		next := r.State
		next.Gap = false
		if err := r.persist(next); err != nil {
			log.Fatal(err)
		}
		log.Print("event gap acknowledged")
		return
	}
	if *replay && r.State.Initialized {
		log.Fatal("--replay-existing only applies before the state file is initialized")
	}
	for id, job := range r.State.Jobs {
		if job.Status == jobRunning {
			log.Printf("event %s has an unresolved running job (%s); inspect external effects before resolving it", id, job.URL)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := r.Poll(ctx, *replay); err != nil {
			if ctx.Err() != nil {
				return
			}
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
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
