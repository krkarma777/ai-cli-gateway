//go:build windows

package config

import (
	"path/filepath"
	"strings"
)

func defaultPath(lookupEnv func(string) (string, bool)) (string, error) {
	base, ok := localAppData(lookupEnv)
	if !ok {
		return "", ErrDefaultPath
	}
	return filepath.Join(base, "AI CLI Gateway", "config", "config.toml"), nil
}

func defaultInitRuntimeRoot(lookupEnv func(string) (string, bool)) (string, error) {
	base, ok := localAppData(lookupEnv)
	if !ok {
		return "", ErrDefaultPath
	}
	return filepath.Join(base, "AI CLI Gateway", "runtime"), nil
}

func localAppData(lookupEnv func(string) (string, bool)) (string, bool) {
	value, ok := lookupEnv("LOCALAPPDATA")
	if !ok || value == "" || strings.IndexByte(value, 0) >= 0 || !filepath.IsAbs(value) {
		return "", false
	}
	if len(value) < 3 || !isDriveLetter(value[0]) || value[1] != ':' || !isPathSeparator(value[2]) {
		return "", false
	}
	return filepath.Clean(value), true
}

func isDriveLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isPathSeparator(value byte) bool {
	return value == '\\' || value == '/'
}
