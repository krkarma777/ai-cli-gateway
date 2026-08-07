//go:build !windows

package config

import "testing"

func TestDefaultPath(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr error
	}{
		{
			name: "absolute XDG_CONFIG_HOME wins",
			env:  map[string]string{"XDG_CONFIG_HOME": "/srv/alice/config", "HOME": "/home/alice"},
			want: "/srv/alice/config/ai-cli-gateway/config.toml",
		},
		{
			name: "cleans absolute XDG_CONFIG_HOME",
			env:  map[string]string{"XDG_CONFIG_HOME": "/srv/alice/../config", "HOME": "/home/alice"},
			want: "/srv/config/ai-cli-gateway/config.toml",
		},
		{
			name: "empty XDG_CONFIG_HOME falls back to HOME",
			env:  map[string]string{"XDG_CONFIG_HOME": "", "HOME": "/home/alice"},
			want: "/home/alice/.config/ai-cli-gateway/config.toml",
		},
		{
			name: "relative XDG_CONFIG_HOME falls back to HOME",
			env:  map[string]string{"XDG_CONFIG_HOME": "config", "HOME": "/home/alice"},
			want: "/home/alice/.config/ai-cli-gateway/config.toml",
		},
		{
			name: "missing XDG_CONFIG_HOME falls back to HOME",
			env:  map[string]string{"HOME": "/home/alice"},
			want: "/home/alice/.config/ai-cli-gateway/config.toml",
		},
		{
			name: "NUL-containing XDG_CONFIG_HOME falls back to HOME",
			env:  map[string]string{"XDG_CONFIG_HOME": "/srv/alice\x00config", "HOME": "/home/alice"},
			want: "/home/alice/.config/ai-cli-gateway/config.toml",
		},
		{
			name:    "no absolute config location returns sentinel",
			env:     map[string]string{"XDG_CONFIG_HOME": "config", "HOME": "alice"},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "NUL-containing HOME returns sentinel",
			env:     map[string]string{"HOME": "/home/alice\x00"},
			wantErr: ErrDefaultPath,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := defaultPath(lookupEnv(test.env))
			if err != test.wantErr {
				t.Fatalf("defaultPath() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("defaultPath() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDefaultInitRuntimeRoot(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr error
	}{
		{
			name: "absolute XDG_STATE_HOME wins",
			env:  map[string]string{"XDG_STATE_HOME": "/srv/alice/state", "HOME": "/home/alice"},
			want: "/srv/alice/state/ai-cli-gateway/runtime",
		},
		{
			name: "cleans absolute XDG_STATE_HOME",
			env:  map[string]string{"XDG_STATE_HOME": "/srv/alice/../state", "HOME": "/home/alice"},
			want: "/srv/state/ai-cli-gateway/runtime",
		},
		{
			name: "empty XDG_STATE_HOME falls back to HOME",
			env:  map[string]string{"XDG_STATE_HOME": "", "HOME": "/home/alice"},
			want: "/home/alice/.local/state/ai-cli-gateway/runtime",
		},
		{
			name: "relative XDG_STATE_HOME falls back to HOME",
			env:  map[string]string{"XDG_STATE_HOME": "state", "HOME": "/home/alice"},
			want: "/home/alice/.local/state/ai-cli-gateway/runtime",
		},
		{
			name: "missing XDG_STATE_HOME falls back to HOME",
			env:  map[string]string{"HOME": "/home/alice"},
			want: "/home/alice/.local/state/ai-cli-gateway/runtime",
		},
		{
			name: "NUL-containing XDG_STATE_HOME falls back to HOME",
			env:  map[string]string{"XDG_STATE_HOME": "/srv/alice\x00state", "HOME": "/home/alice"},
			want: "/home/alice/.local/state/ai-cli-gateway/runtime",
		},
		{
			name:    "no absolute state location returns sentinel",
			env:     map[string]string{"XDG_STATE_HOME": "state", "HOME": "alice"},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "missing values never use the working or temporary directory",
			env:     map[string]string{},
			wantErr: ErrDefaultPath,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := defaultInitRuntimeRoot(lookupEnv(test.env))
			if err != test.wantErr {
				t.Fatalf("defaultInitRuntimeRoot() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("defaultInitRuntimeRoot() = %q, want %q", got, test.want)
			}
		})
	}
}

func lookupEnv(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
