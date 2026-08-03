//go:build windows

package config

import (
	"os"
	"path/filepath"
)

func defaultRuntimeRoot() string {
	return filepath.Join(os.TempDir(), "ai-cli-gateway")
}
