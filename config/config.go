// Package config defines shared runtime configuration for inletd.
package config

import "time"

const (
	DefaultQueueCapacity = 100
	DefaultMaxConcurrent = 1
	DefaultTimeout       = 5 * time.Minute
)

// Config describes event routes, trusted local commands, and runtime limits.
// It does not depend on a particular event transport or file format.
type Config struct {
	Routes      map[string][]string
	Commands    map[string]Command
	Queue       QueueSettings
	Concurrency ConcurrencySettings
}

// Command is defined only by local configuration, never by an incoming event.
// TimeoutSeconds is optional; a present zero or negative value is left intact
// for validation rather than being replaced by the default.
type Command struct {
	Executable     string
	Argv           []string
	WorkingDir     string
	Environment    map[string]string
	TimeoutSeconds *int
}

type QueueSettings struct {
	Capacity *int
}

type ConcurrencySettings struct {
	Max *int
}

func (c Config) EffectiveQueueCapacity() int {
	if c.Queue.Capacity != nil {
		return *c.Queue.Capacity
	}
	return DefaultQueueCapacity
}

func (c Config) EffectiveMaxConcurrent() int {
	if c.Concurrency.Max != nil {
		return *c.Concurrency.Max
	}
	return DefaultMaxConcurrent
}

func (c Command) EffectiveTimeout() time.Duration {
	if c.TimeoutSeconds != nil {
		return time.Duration(*c.TimeoutSeconds) * time.Second
	}
	return DefaultTimeout
}
