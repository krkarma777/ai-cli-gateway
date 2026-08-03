//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"strconv"
)

func defaultRuntimeRoot() string {
	return filepath.Join(
		os.TempDir(),
		"ai-cli-gateway-"+strconv.Itoa(os.Geteuid()),
	)
}
