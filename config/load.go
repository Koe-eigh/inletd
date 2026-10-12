package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Load reads one JSON configuration file using the caller's source schema. It
// rejects unknown fields and extra JSON values, but leaves semantic validation
// to the caller.
func Load[Source any](path string) (Config[Source], error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config[Source]{}, fmt.Errorf("read config %q: %w", path, err)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Config[Source]{}, fmt.Errorf("parse config %q: expected a JSON object", path)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg Config[Source]
	if err := decoder.Decode(&cfg); err != nil {
		return Config[Source]{}, fmt.Errorf("parse config %q: %w", path, describeJSONError(data, err))
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config[Source]{}, fmt.Errorf("parse config %q: multiple JSON values at byte %d", path, decoder.InputOffset())
		}
		return Config[Source]{}, fmt.Errorf("parse config %q: %w", path, describeJSONError(data, err))
	}
	// encoding/json accepts null for non-pointer fields and silently supplies
	// their zero values. No field in this schema uses null, so reject it rather
	// than letting an explicit value look omitted.
	var values any
	if err := json.Unmarshal(data, &values); err != nil {
		return Config[Source]{}, fmt.Errorf("parse config %q: invalid JSON", path)
	}
	if field := nullField(values, ""); field != "" {
		return Config[Source]{}, fmt.Errorf("parse config %q: null value for field %q", path, field)
	}
	return cfg, nil
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
