//go:build !linux && !darwin

package releasepack

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteArchivesRejectsUnsupportedHostBeforeFilesystemInspection(t *testing.T) {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	options := ArchiveOptions{
		RepositoryRoot: filepath.Join(root, "releasepack-nonexistent-repository"),
		StagingRoot:    filepath.Join(root, "releasepack-nonexistent-staging"),
		OutputRoot:     filepath.Join(root, "releasepack-nonexistent-output"),
		Tag:            "v0.1.0",
		SourceTime:     time.Date(2026, 8, 4, 6, 29, 53, 0, time.UTC),
	}

	assets, err := WriteArchives(options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertUnsupportedCategory(t, err)
}

func assertUnsupportedCategory(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want invalid_usage")
	}
	if got := ErrorCategory(err); got != string(categoryInvalidUsage) {
		t.Fatalf("ErrorCategory(%v) = %q, want %q", err, got, categoryInvalidUsage)
	}
	if err.Error() != string(categoryInvalidUsage) {
		t.Fatalf("error text = %q, want %q", err.Error(), categoryInvalidUsage)
	}
}
