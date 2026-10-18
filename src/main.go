package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"
)

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
