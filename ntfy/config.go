// Package ntfy defines configuration for the ntfy source adapter.
package ntfy

type SourceConfig struct {
	ServerURL string `json:"server_url"`
	Topic     string `json:"topic"`
	TokenEnv  string `json:"token_env"`
}
