// Package config loads and validates the gateway's trusted TOML configuration.
package config

import (
	"errors"
	"time"
)

// Config is the normalized gateway configuration.
type Config struct {
	Server    Server              `toml:"server"`
	Runtime   Runtime             `toml:"runtime"`
	Providers map[string]Provider `toml:"providers"`
	Models    []Model             `toml:"models"`
}

// Server contains HTTP server limits and lifecycle settings.
type Server struct {
	Listen            string   `toml:"listen"`
	APIKeyEnv         string   `toml:"api_key_env"`
	APIKeyFile        string   `toml:"api_key_file"`
	HTTPBodyBytes     int64    `toml:"http_body_bytes"`
	InputBytes        int      `toml:"input_bytes"`
	InstructionsBytes int      `toml:"instructions_bytes"`
	SchemaBytes       int      `toml:"schema_bytes"`
	HandlerLimit      int      `toml:"handler_limit"`
	BodyReaderLimit   int      `toml:"body_reader_limit"`
	MaxHeaderBytes    int      `toml:"max_header_bytes"`
	ReadHeaderTimeout Duration `toml:"read_header_timeout"`
	BodyReadTimeout   Duration `toml:"body_read_timeout"`
	IdleTimeout       Duration `toml:"idle_timeout"`
	ShutdownTimeout   Duration `toml:"shutdown_timeout"`
}

// Runtime contains process containment and output limits.
type Runtime struct {
	Root           string   `toml:"root"`
	TermGrace      Duration `toml:"term_grace"`
	CleanupTimeout Duration `toml:"cleanup_timeout"`
	StdoutBytes    int64    `toml:"stdout_bytes"`
	StderrBytes    int64    `toml:"stderr_bytes"`
	FinalBytes     int64    `toml:"final_bytes"`
}

// Provider contains one provider CLI's structural and admission settings.
type Provider struct {
	Executable       string   `toml:"executable"`
	PrefixArgs       []string `toml:"prefix_args"`
	ConfigHome       string   `toml:"config_home"`
	CredentialEnv    []string `toml:"credential_env"`
	Concurrency      int      `toml:"concurrency"`
	QueueSize        int      `toml:"queue_size"`
	QueueBytes       int64    `toml:"queue_bytes"`
	QueueTimeout     Duration `toml:"queue_timeout"`
	ExecutionTimeout Duration `toml:"execution_timeout"`
}

// Model maps a public model alias to a provider model argument.
type Model struct {
	ID            string `toml:"id"`
	Provider      string `toml:"provider"`
	ProviderModel string `toml:"provider_model"`
	Created       int64  `toml:"created"`
}

// Duration is a positive Go duration decoded from an exact duration string.
type Duration time.Duration

var errInvalidDuration = errors.New("duration is invalid")

// UnmarshalText parses an exact Go duration without echoing untrusted input.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil || parsed <= 0 {
		return errInvalidDuration
	}
	*d = Duration(parsed)
	return nil
}
