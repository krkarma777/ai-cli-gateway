//go:build !windows

package testutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TrustedTempDir creates a private fixture beside the repository so strict
// path-policy tests do not inherit an attacker-writable system temp ancestor.
func TrustedTempDir(t testing.TB) string {
	t.Helper()
	root := repositoryRoot(t)
	directory, err := os.MkdirTemp(
		filepath.Dir(root),
		".ai-cli-gateway-test-",
	)
	if err != nil {
		t.Fatalf("create trusted fixture directory: %v", err)
	}
	//nolint:gosec // This is the required owner-only directory mode, not a file mode.
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatalf("secure trusted fixture directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove trusted fixture directory: %v", err)
		}
	})
	return directory
}

// WriteTrustedFile creates a test file with the requested Unix permissions.
func WriteTrustedFile(
	t testing.TB,
	path string,
	payload []byte,
	mode fs.FileMode,
) {
	t.Helper()
	//nolint:gosec // The caller supplies an exact test-owned fixture path.
	if err := os.WriteFile(path, payload, mode); err != nil {
		t.Fatalf("write trusted fixture file: %v", err)
	}
}
