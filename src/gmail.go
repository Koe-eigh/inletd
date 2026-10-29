package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type gmailCLI func(context.Context, string, ...string) ([]byte, error)

type GmailEventSource struct {
	config    Config
	run       gmailCLI
	processed map[string]bool
	failures  int
}

func NewGmailEventSource(config Config, run gmailCLI) *GmailEventSource {
	if run == nil {
		run = runGmailCLI
	}
	return &GmailEventSource{config: config, run: run}
}

func runGmailCLI(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	var out limitedOutput
	cmd.Stdout = &out
	var stderr limitedOutput
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gws failed: %w: %s", err, strings.TrimSpace(string(stderr.Bytes())))
	}
	if out.overflow {
		return nil, errors.New("gws response exceeds 16 MiB")
	}
	return out.Bytes(), nil
}

type limitedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > (16<<20)-b.Len() {
		b.overflow = true
		return len(p), errors.New("gws output too large")
	}
	return b.Buffer.Write(p)
}

func (s *GmailEventSource) SetProcessed(seen []string, jobs map[string]Job) {
	s.processed = make(map[string]bool, len(seen)+len(jobs))
	for _, id := range seen {
		s.processed[id] = true
	}
	for id := range jobs {
		s.processed[id] = true
	}
}
func (s *GmailEventSource) Commit() {}

func (s *GmailEventSource) call(ctx context.Context, method string, params any, dst any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	out, err := s.run(ctx, s.config.Gmail.CLIPath, "gmail", "users", "messages", method, "--params", string(b))
	if err != nil {
		return err
	}
	if len(out) == 0 {
		return errors.New("empty gws response")
	}
	if err := json.Unmarshal(out, dst); err != nil {
		return fmt.Errorf("invalid gws response: %w", err)
	}
	return nil
}

func (s *GmailEventSource) failed(err error) (EventBatch, error) {
	s.failures++
	delay := time.Minute
	for i := 1; i < s.failures && delay < 15*time.Minute; i++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	if strings.Contains(strings.ToLower(err.Error()), "401") || strings.Contains(strings.ToLower(err.Error()), "unauthorized") || strings.Contains(strings.ToLower(err.Error()), "invalid_grant") {
		delay = 15 * time.Minute
	}
	return EventBatch{PollInterval: delay}, err
}

func (s *GmailEventSource) Fetch(ctx context.Context) (EventBatch, error) {
	var batch EventBatch
	token := ""
	visited := map[string]bool{}
	fetched := map[string]bool{}
	for page := 0; page < 1000; page++ {
		params := map[string]any{"userId": s.config.Gmail.UserID, "q": s.config.Gmail.Query, "maxResults": 100}
		if token != "" {
			params["pageToken"] = token
		}
		var listing struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := s.call(ctx, "list", params, &listing); err != nil {
			return s.failed(err)
		}
		for _, item := range listing.Messages {
			if item.ID == "" {
				continue
			}
			id := "gmail:" + item.ID
			if s.processed[id] || fetched[id] {
				continue
			}
			fetched[id] = true
			var msg gmailMessage
			if err := s.call(ctx, "get", map[string]any{"userId": s.config.Gmail.UserID, "id": item.ID, "format": "full"}, &msg); err != nil {
				return s.failed(err)
			}
			if msg.ID != item.ID {
				return s.failed(fmt.Errorf("Gmail message ID mismatch for %s", item.ID))
			}
			event, err := parseGmailMessage(msg, []byte(os.Getenv(s.config.Gmail.SigningKeyEnv)), s.config.Repository)
			if err != nil {
				// Invalid messages are recorded as unmatched events so they are not fetched forever.
				event = Event{ID: id}
				event.Repo.Name = s.config.Repository
				event.Payload = json.RawMessage(`{}`)
			}
			batch.Events = append(batch.Events, event)
		}
		if listing.NextPageToken == "" {
			s.failures = 0
			return batch, nil
		}
		if visited[listing.NextPageToken] {
			return s.failed(errors.New("repeated Gmail page token"))
		}
		visited[listing.NextPageToken] = true
		token = listing.NextPageToken
	}
	return s.failed(errors.New("Gmail result exceeds 1000 pages; narrow gmail.query"))
}

type gmailMessage struct {
	ID      string    `json:"id"`
	Payload gmailPart `json:"payload"`
}
type gmailPart struct {
	MimeType string `json:"mimeType"`
	Headers  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []gmailPart `json:"parts"`
}

func gmailPlainBodies(p gmailPart) ([][]byte, error) {
	var bodies [][]byte
	if strings.EqualFold(p.MimeType, "text/plain") && p.Body.Data != "" {
		body, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p.Body.Data, "="))
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, body)
	}
	for _, child := range p.Parts {
		found, err := gmailPlainBodies(child)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, found...)
	}
	return bodies, nil
}

func parseGmailMessage(msg gmailMessage, key []byte, repo string) (Event, error) {
	var event Event
	if msg.ID == "" || len(key) < 32 {
		return event, errors.New("missing message ID or signing key")
	}
	bodies, err := gmailPlainBodies(msg.Payload)
	if err != nil || len(bodies) != 1 {
		return event, errors.New("message must have exactly one decodable text/plain body")
	}
	signature := ""
	for _, h := range msg.Payload.Headers {
		if strings.EqualFold(h.Name, "X-Inletd-Signature-SHA256") {
			if signature != "" {
				return event, errors.New("duplicate signature header")
			}
			signature = h.Value
		}
	}
	supplied, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || len(supplied) != sha256.Size {
		return event, errors.New("invalid signature")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(bodies[0])
	if !hmac.Equal(supplied, mac.Sum(nil)) {
		return event, errors.New("signature mismatch")
	}
	var envelope struct {
		DeliveryID string          `json:"delivery_id"`
		EventType  string          `json:"event_type"`
		Repository string          `json:"repository"`
		Actor      string          `json:"actor"`
		Payload    json.RawMessage `json:"payload"`
	}
	dec := json.NewDecoder(bytes.NewReader(bodies[0]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return event, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return event, errors.New("trailing envelope data")
	}
	if envelope.DeliveryID == "" || envelope.EventType == "" || !strings.EqualFold(envelope.Repository, repo) || envelope.Actor == "" || !json.Valid(envelope.Payload) {
		return event, errors.New("incomplete or wrong-repository event envelope")
	}
	event = Event{ID: "gmail:" + msg.ID, Type: envelope.EventType, Provider: "gmail", Payload: envelope.Payload}
	event.Repo.Name = envelope.Repository
	event.Actor.Login = envelope.Actor
	return event, nil
}
