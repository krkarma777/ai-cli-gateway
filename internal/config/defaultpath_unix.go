//go:build !windows

package config

import (
	"path/filepath"
	"strings"
)

func defaultPath(lookupEnv func(string) (string, bool)) (string, error) {
	base, ok := absoluteEnv(lookupEnv, "XDG_CONFIG_HOME")
	if !ok {
		home, ok := absoluteEnv(lookupEnv, "HOME")
		if !ok {
			return "", ErrDefaultPath
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ai-cli-gateway", "config.toml"), nil
}

func defaultInitRuntimeRoot(lookupEnv func(string) (string, bool)) (string, error) {
	base, ok := absoluteEnv(lookupEnv, "XDG_STATE_HOME")
	if !ok {
		home, ok := absoluteEnv(lookupEnv, "HOME")
		if !ok {
			return "", ErrDefaultPath
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ai-cli-gateway", "runtime"), nil
}

func absoluteEnv(lookupEnv func(string) (string, bool), name string) (string, bool) {
	value, ok := lookupEnv(name)
	if !ok || value == "" || strings.IndexByte(value, 0) >= 0 || !filepath.IsAbs(value) {
		return "", false
	}
	value = filepath.Clean(value)
	if !filepath.IsAbs(value) {
		return "", false
	}
	return value, true
}
