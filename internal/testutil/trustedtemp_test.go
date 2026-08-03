package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTrustedTempDirCreatesAbsolutePrivateFixture(t *testing.T) {
	directory := TrustedTempDir(t)
	if !filepath.IsAbs(directory) {
		t.Fatalf("TrustedTempDir() = %q, want absolute path", directory)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("trusted fixture mode = %v, want directory", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("trusted fixture permissions = %v, want 0700", info.Mode().Perm())
	}
}
