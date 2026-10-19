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
	r := &Runner{Config: cfg, Source: NewGitHubEventSource(cfg, &http.Client{Timeout: 20 * time.Second}), AgentRunner: CommandAgentRunner{Config: cfg}, Logger: log.Default()}
	r.State, err = loadState(cfg.StateFile)
	if err != nil {
		log.Fatal(err)
	}
	if *resolveEvent != "" {
		next, err := r.State.resolveJob(*resolveEvent, *resolution)
		if err != nil {
			log.Fatal(err)
		}
		if err := saveState(cfg.StateFile, next); err != nil {
			log.Fatal(err)
		}
		log.Printf("event %s resolved as %s", *resolveEvent, *resolution)
		return
	}
	if *acknowledgeGap {
		if !r.State.Gap {
			log.Fatal("no event gap to acknowledge")
		}
		r.State.Gap = false
		if err := saveState(cfg.StateFile, r.State); err != nil {
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
