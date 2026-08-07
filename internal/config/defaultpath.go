package config

import (
	"errors"
	"os"
)

// ErrDefaultPath is returned when no safe per-user default path is available.
var ErrDefaultPath = errors.New("default per-user path is unavailable")

// DefaultPath returns the default per-user configuration file path.
func DefaultPath() (string, error) {
	return defaultPath(os.LookupEnv)
}

// DefaultInitRuntimeRoot returns the default per-user runtime root for init.
func DefaultInitRuntimeRoot() (string, error) {
	return defaultInitRuntimeRoot(os.LookupEnv)
}
