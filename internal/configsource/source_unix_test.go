//go:build !windows

package configsource

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLoadRejectsUnixSymlinkAndNonRegularSources(t *testing.T) {
	regular := writeSourceConfig(t, "SOURCE_KEY")
	symlink := filepath.Join(t.TempDir(), "config-link.toml")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}
	fifo := filepath.Join(t.TempDir(), "config.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("create source FIFO: %v", err)
	}

	for _, path := range []string{symlink, filepath.Dir(regular), fifo} {
		snapshot, err := Load(path)
		if snapshot != nil {
			_ = snapshot.Close()
			t.Fatalf("Load(%q) snapshot = %#v, want nil", path, snapshot)
		}
		assertSourceUnavailable(t, err)
	}
}
