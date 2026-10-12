// Package ntfy defines the ntfy source configuration. The application selects
// this source schema when it loads a local configuration file.
package ntfy

type SourceConfig struct {
	ServerURL string `json:"server_url"`
	Topic     string `json:"topic"`
	TokenEnv  string `json:"token_env"`
}
