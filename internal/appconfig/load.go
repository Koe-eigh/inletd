// Package appconfig loads the application's local configuration file.
package appconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Koe-eigh/inletd/config"
	"github.com/Koe-eigh/inletd/ntfy"
)

// Config combines the shared runtime settings with ntfy source settings at the
// application boundary.
type Config struct {
	config.Config
	Sources map[string]ntfy.SourceConfig
}

type fileConfig struct {
	Sources  map[string]ntfy.SourceConfig `json:"sources"`
	Routes   map[string][]string          `json:"routes"`
	Commands map[string]fileCommand       `json:"commands"`
	Queue    struct {
		Capacity *int `json:"capacity"`
	} `json:"queue"`
	Concurrency struct {
		Max *int `json:"max"`
	} `json:"concurrency"`
}

type fileCommand struct {
	Executable     string            `json:"executable"`
	Argv           []string          `json:"argv"`
	WorkingDir     string            `json:"working_directory"`
	Environment    map[string]string `json:"environment"`
	TimeoutSeconds *int              `json:"timeout_seconds"`
}

func (file fileConfig) runtimeConfig() Config {
	cfg := Config{
		Config: config.Config{
			Routes:      file.Routes,
			Queue:       config.QueueSettings{Capacity: file.Queue.Capacity},
			Concurrency: config.ConcurrencySettings{Max: file.Concurrency.Max},
		},
		Sources: file.Sources,
	}
	if file.Commands != nil {
		cfg.Commands = make(map[string]config.Command, len(file.Commands))
		for name, command := range file.Commands {
			cfg.Commands[name] = config.Command{
				Executable:     command.Executable,
				Argv:           command.Argv,
				WorkingDir:     command.WorkingDir,
				Environment:    command.Environment,
				TimeoutSeconds: command.TimeoutSeconds,
			}
		}
	}
	return cfg
}

// Load reads one JSON configuration file. It rejects unknown fields and extra
// JSON values, but leaves semantic validation to the caller.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Config{}, fmt.Errorf("parse config %q: expected a JSON object", path)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file fileConfig
	if err := decoder.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, describeJSONError(data, err))
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("parse config %q: multiple JSON values at byte %d", path, decoder.InputOffset())
		}
		return Config{}, fmt.Errorf("parse config %q: %w", path, describeJSONError(data, err))
	}
	if err := rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(data)), ""); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	// encoding/json accepts null for non-pointer fields and silently supplies
	// their zero values. No field in this schema uses null, so reject it rather
	// than letting an explicit value look omitted.
	var values any
	if err := json.Unmarshal(data, &values); err != nil {
		return Config{}, fmt.Errorf("parse config %q: invalid JSON", path)
	}
	if field := nullField(values, ""); field != "" {
		return Config{}, fmt.Errorf("parse config %q: null value for field %q", path, field)
	}
	return file.runtimeConfig(), nil
}

// rejectDuplicateKeys walks the original token stream because decoding into
// maps discards earlier members with the same name.
func rejectDuplicateKeys(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON")
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("invalid JSON")
			}
			key := keyToken.(string)
			field := key
			if path != "" {
				field = path + "." + key
			}
			if seen[key] {
				return fmt.Errorf("duplicate field %q", field)
			}
			seen[key] = true
			if err := rejectDuplicateKeys(decoder, field); err != nil {
				return err
			}
		}
		_, err = decoder.Token() // closing brace
	case json.Delim('['):
		for i := 0; decoder.More(); i++ {
			if err := rejectDuplicateKeys(decoder, path+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
		_, err = decoder.Token() // closing bracket
	}
	if err != nil {
		return fmt.Errorf("invalid JSON")
	}
	return nil
}

func nullField(value any, path string) string {
	switch value := value.(type) {
	case nil:
		return path
	case map[string]any:
		for key, child := range value {
			field := key
			if path != "" {
				field = path + "." + key
			}
			if found := nullField(child, field); found != "" {
				return found
			}
		}
	case []any:
		for i, child := range value {
			if found := nullField(child, path+"["+strconv.Itoa(i)+"]"); found != "" {
				return found
			}
		}
	}
	return ""
}

func describeJSONError(data []byte, err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		line, column := position(data, syntax.Offset)
		return fmt.Errorf("invalid JSON at line %d, column %d", line, column)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		line, column := position(data, typeErr.Offset)
		if typeErr.Field != "" {
			return fmt.Errorf("invalid type for field %q at line %d, column %d", typeErr.Field, line, column)
		}
		return fmt.Errorf("invalid type at line %d, column %d", line, column)
	}
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return fmt.Errorf("unknown field %s", field)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("incomplete JSON at end of file")
	}
	return fmt.Errorf("invalid JSON")
}

func position(data []byte, offset int64) (line, column int) {
	line, column = 1, 1
	for _, b := range data[:min(int(offset), len(data))] {
		if b == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}
	return line, column
}
