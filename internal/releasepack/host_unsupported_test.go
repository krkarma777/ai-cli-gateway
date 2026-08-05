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

func TestWriteSBOMAndChecksumsRejectUnsupportedHostBeforeFilesystemInspection(t *testing.T) {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	repository := filepath.Join(root, "releasepack-nonexistent-repository")
	staging := filepath.Join(root, "releasepack-nonexistent-staging")
	output := filepath.Join(root, "releasepack-nonexistent-output")
	raw := filepath.Join(root, "releasepack-nonexistent-raw.spdx.json")

	asset, err := WriteSBOM(SBOMOptions{
		RepositoryRoot: repository,
		StagingRoot:    staging,
		OutputRoot:     output,
		RawPath:        raw,
		Tag:            "v0.1.0",
		SourceTime:     time.Date(2026, 8, 4, 6, 29, 53, 0, time.UTC),
	})
	if asset != (Asset{}) {
		t.Fatalf("SBOM asset = %#v, want zero", asset)
	}
	assertUnsupportedCategory(t, err)

	asset, err = WriteChecksums(ChecksumOptions{
		RepositoryRoot: repository,
		StagingRoot:    staging,
		OutputRoot:     output,
		Tag:            "v0.1.0",
	})
	if asset != (Asset{}) {
		t.Fatalf("checksum asset = %#v, want zero", asset)
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
