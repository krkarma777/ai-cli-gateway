package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishReadinessStagesCompletePrivateRecordBeforeRename(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "process-tree.ready")
	payload := []byte("101 102 101\n")
	renameCalls := 0

	err := publishReadinessWith(path, payload, func(oldPath, newPath string) error {
		renameCalls++
		if newPath != path {
			t.Fatalf("rename destination = %q, want final readiness path", newPath)
		}
		if filepath.Dir(oldPath) != root || oldPath == path {
			t.Fatalf("rename source = %q, want a sibling staging file", oldPath)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("final readiness path exists before rename: %v", err)
		}
		contents, err := os.ReadFile(oldPath)
		if err != nil {
			t.Fatalf("read staged readiness: %v", err)
		}
		if !bytes.Equal(contents, payload) {
			t.Fatalf("staged readiness = %q, want complete record %q", contents, payload)
		}
		info, err := os.Lstat(oldPath)
		if err != nil {
			t.Fatalf("inspect staged readiness: %v", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("staged readiness mode = %v, want private regular file", info.Mode())
		}
		return os.Rename(oldPath, newPath)
	})
	if err != nil {
		t.Fatalf("publishReadinessWith() error = %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("rename calls = %d, want 1", renameCalls)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published readiness: %v", err)
	}
	if !bytes.Equal(contents, payload) {
		t.Fatalf("published readiness = %q, want %q", contents, payload)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("list readiness directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("readiness directory entries = %v, want only final file", entries)
	}
}
