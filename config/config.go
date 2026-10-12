// Package config defines the local JSON configuration for inletd.
package config

import "time"

const (
	DefaultQueueCapacity = 100
	DefaultMaxConcurrent = 1
	DefaultTimeout       = 5 * time.Minute
)

// Config describes sources, event routes, and trusted local commands. The
// caller supplies the source schema when loading; config does not depend on a
// particular transport. Loading does not validate relationships between these
// definitions.
type Config[Source any] struct {
	Sources     map[string]Source   `json:"sources"`
	Routes      map[string][]string `json:"routes"`
	Commands    map[string]Command  `json:"commands"`
	Queue       QueueSettings       `json:"queue"`
	Concurrency ConcurrencySettings `json:"concurrency"`
}

// Command is defined only by local configuration, never by an incoming event.
// TimeoutSeconds is optional; a present zero or negative value is left intact
// for validation rather than being replaced by the default.
type Command struct {
	Executable     string            `json:"executable"`
	Argv           []string          `json:"argv"`
	WorkingDir     string            `json:"working_directory"`
	Environment    map[string]string `json:"environment"`
	TimeoutSeconds *int              `json:"timeout_seconds"`
}

type QueueSettings struct {
	Capacity *int `json:"capacity"`
}

type ConcurrencySettings struct {
	Max *int `json:"max"`
}

func (c Config[Source]) EffectiveQueueCapacity() int {
	if c.Queue.Capacity != nil {
		return *c.Queue.Capacity
	}
	return DefaultQueueCapacity
}

func (c Config[Source]) EffectiveMaxConcurrent() int {
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
