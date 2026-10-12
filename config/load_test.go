package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Koe-eigh/inletd/ntfy"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndDefaults(t *testing.T) {
	path := writeConfig(t, `{
  "sources": {"alerts": {"server_url": "https://ntfy.sh", "topic": "alerts", "token_env": "NTFY_TOKEN"}},
  "routes": {"review.requested": ["review"]},
  "commands": {"review": {"executable": "/usr/bin/printf", "argv": ["%s", "ready"], "working_directory": "/tmp", "environment": {"MODE": "test"}}}
}`)
	cfg, err := Load[ntfy.SourceConfig](path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources["alerts"].Topic != "alerts" || cfg.Sources["alerts"].TokenEnv != "NTFY_TOKEN" {
		t.Fatalf("source not parsed: %+v", cfg.Sources["alerts"])
	}
	if len(cfg.Routes["review.requested"]) != 1 || cfg.Routes["review.requested"][0] != "review" {
		t.Fatalf("route not parsed: %+v", cfg.Routes)
	}
	command := cfg.Commands["review"]
	if command.Executable != "/usr/bin/printf" || command.WorkingDir != "/tmp" || command.Environment["MODE"] != "test" || len(command.Argv) != 2 {
		t.Fatalf("command not parsed: %+v", command)
	}
	if cfg.EffectiveQueueCapacity() != DefaultQueueCapacity || cfg.EffectiveMaxConcurrent() != DefaultMaxConcurrent || command.EffectiveTimeout() != DefaultTimeout {
		t.Fatalf("defaults not applied: queue=%d concurrency=%d timeout=%s", cfg.EffectiveQueueCapacity(), cfg.EffectiveMaxConcurrent(), command.EffectiveTimeout())
	}
	if cfg.Queue.Capacity != nil || cfg.Concurrency.Max != nil || command.TimeoutSeconds != nil {
		t.Fatal("omitted settings should remain distinguishable from explicit values")
	}
}

func TestLoadUsesCallerSourceSchema(t *testing.T) {
	type source struct {
		Endpoint string `json:"endpoint"`
	}
	path := writeConfig(t, `{"sources":{"custom":{"endpoint":"local"}}}`)
	cfg, err := Load[source](path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources["custom"].Endpoint != "local" {
		t.Fatalf("custom source not parsed: %+v", cfg.Sources["custom"])
	}

	_, err = Load[source](writeConfig(t, `{"sources":{"custom":{"topic":"alerts"}}}`))
	if err == nil || !strings.Contains(err.Error(), `unknown field "topic"`) {
		t.Fatalf("want unknown source field error, got %v", err)
	}
}

func TestExplicitSettingsRemainAvailableForValidation(t *testing.T) {
	path := writeConfig(t, `{"queue":{"capacity":0},"concurrency":{"max":-2},"commands":{"run":{"timeout_seconds":0}}}`)
	cfg, err := Load[ntfy.SourceConfig](path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Queue.Capacity == nil || cfg.Concurrency.Max == nil || cfg.Commands["run"].TimeoutSeconds == nil {
		t.Fatal("explicit settings must remain present")
	}
	if cfg.EffectiveQueueCapacity() != 0 || cfg.EffectiveMaxConcurrent() != -2 || cfg.Commands["run"].EffectiveTimeout() != 0 {
		t.Fatal("explicit invalid values must not be replaced by defaults")
	}

	path = writeConfig(t, `{"queue":{"capacity":8},"concurrency":{"max":3},"commands":{"run":{"timeout_seconds":12}}}`)
	cfg, err = Load[ntfy.SourceConfig](path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EffectiveQueueCapacity() != 8 || cfg.EffectiveMaxConcurrent() != 3 || cfg.Commands["run"].EffectiveTimeout() != 12*time.Second {
		t.Fatal("explicit valid settings not applied")
	}
}

func TestLoadErrors(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"empty", "", "expected a JSON object"},
		{"root array", "[]", "expected a JSON object"},
		{"syntax", "{\n  \"queue\": @secret", "line 2"},
		{"truncated", `{"queue":`, "incomplete JSON"},
		{"unknown root", `{"surprise": true}`, `unknown field "surprise"`},
		{"unknown nested", `{"commands":{"run":{"secret":"value"}}}`, `unknown field "secret"`},
		{"unknown source field", `{"sources":{"alerts":{"topic":"alerts","secret":"hidden-value"}}}`, `unknown field "secret"`},
		{"wrong type", `{"queue":{"capacity":"hidden-value"}}`, `field "queue.capacity"`},
		{"null setting", `{"queue":{"capacity":null}}`, `field "queue.capacity"`},
		{"null route action", `{"routes":{"event":[null]}}`, `field "routes.event[0]"`},
		{"extra value", `{} {"secret":"hidden-value"}`, "multiple JSON values"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load[ntfy.SourceConfig](writeConfig(t, tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "hidden-value") || strings.Contains(err.Error(), "@secret") {
				t.Fatalf("error exposed a JSON value: %v", err)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	_, err := Load[ntfy.SourceConfig](path)
	if err == nil || !strings.Contains(err.Error(), "read config") || !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("want file/read context, got %v", err)
	}
}

func TestExampleLoads(t *testing.T) {
	if _, err := Load[ntfy.SourceConfig]("example.json"); err != nil {
		t.Fatalf("example configuration is invalid: %v", err)
	}
}
