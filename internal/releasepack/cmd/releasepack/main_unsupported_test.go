//go:build !linux && !darwin

package main

import (
	"bytes"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunCommandsReachUnsupportedHostGuard(t *testing.T) {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	repository := filepath.Join(root, "releasepack-nonexistent-repository")
	staging := filepath.Join(root, "releasepack-nonexistent-staging")
	output := filepath.Join(root, "releasepack-nonexistent-output")
	raw := filepath.Join(root, "releasepack-nonexistent-raw.spdx.json")
	tests := [][]string{{"archives", "--repository-root", repository, "--staging-root", staging, "--output-root", output, "--tag", "v0.1.0", "--source-epoch", "1785805793"}, {"sbom", "--repository-root", repository, "--staging-root", staging, "--output-root", output, "--raw-sbom", raw, "--tag", "v0.1.0", "--source-epoch", "1785805793"}, {"checksums", "--repository-root", repository, "--staging-root", staging, "--output-root", output, "--tag", "v0.1.0"}}
	for _, args := range tests {
		var stderr bytes.Buffer
		if code := run(args, &stderr); code == 0 {
			t.Fatal("run exit = 0")
		}
		if got := stderr.String(); got != "releasepack: invalid_usage\n" {
			t.Fatalf("stderr = %q", got)
		}
	}
}
