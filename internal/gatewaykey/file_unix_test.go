//go:build !windows

package gatewaykey

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestLoadFileAcceptsPrivateOwnerOwnedRegularFile(t *testing.T) {
	for _, mode := range []fs.FileMode{0o400, 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			path := writeUnixKeyFile(t, testKey+"\n", mode)

			snapshot, err := LoadFile(path, nil)
			if err != nil {
				t.Fatalf("LoadFile() error = %v", err)
			}
			if !snapshot.Valid() || !snapshot.Enabled() || !snapshot.Matches(testKey) {
				t.Fatal("LoadFile() did not return the expected enabled snapshot")
			}
		})
	}
}

func makeUnixFIFO(path string, mode uint32) error {
	return unix.Mkfifo(path, mode)
}

func TestLoadFileRejectsUnsafePathAndLeafShapes(t *testing.T) {
	trusted := testutil.TrustedTempDir(t)
	valid := filepath.Join(trusted, "valid.key")
	testutil.WriteTrustedFile(t, valid, []byte(testKey+"\n"), 0o600)

	leafLink := filepath.Join(trusted, "leaf-link.key")
	if err := os.Symlink(valid, leafLink); err != nil {
		t.Fatalf("create leaf symlink: %v", err)
	}
	directory := filepath.Join(trusted, "directory.key")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create directory fixture: %v", err)
	}
	fifo := filepath.Join(trusted, "fifo.key")
	if err := makeUnixFIFO(fifo, 0o600); err != nil {
		t.Fatalf("create FIFO fixture: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative", path: filepath.Base(valid)},
		{name: "NUL", path: valid + "\x00suffix"},
		{name: "leaf symlink", path: leafLink},
		{name: "directory", path: directory},
		{name: "FIFO", path: fifo},
		{name: "device", path: "/dev/null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertLoadFileUnavailable(t, tt.path, nil)
		})
	}
}

func TestLoadFileRejectsUnsafeOwnershipModesLinksAndAncestors(t *testing.T) {
	t.Run("wrong owner", func(t *testing.T) {
		path := writeUnixKeyFile(t, testKey+"\n", 0o600)
		if err := os.Chown(path, os.Geteuid()+1, -1); err != nil {
			if errors.Is(err, fs.ErrPermission) {
				t.Skip("changing file ownership requires privilege on this host")
			}
			t.Fatalf("change fixture owner: %v", err)
		}
		assertLoadFileUnavailable(t, path, nil)
	})

	for _, mode := range []fs.FileMode{0o640, 0o604, 0o700, 0o200, 0o100, 0o000} {
		t.Run("mode_"+mode.String(), func(t *testing.T) {
			path := writeUnixKeyFile(t, testKey+"\n", 0o600)
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("chmod fixture: %v", err)
			}
			assertLoadFileUnavailable(t, path, nil)
		})
	}

	for _, special := range []fs.FileMode{fs.ModeSetuid, fs.ModeSetgid, fs.ModeSticky} {
		t.Run("special_"+special.String(), func(t *testing.T) {
			path := writeUnixKeyFile(t, testKey+"\n", 0o600)
			if err := os.Chmod(path, 0o600|special); err != nil {
				t.Fatalf("chmod special fixture: %v", err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("stat special fixture: %v", err)
			}
			if info.Mode()&special == 0 {
				t.Skip("filesystem did not preserve requested special mode")
			}
			assertLoadFileUnavailable(t, path, nil)
		})
	}

	t.Run("hard link", func(t *testing.T) {
		path := writeUnixKeyFile(t, testKey+"\n", 0o600)
		if err := os.Link(path, path+".alias"); err != nil {
			t.Fatalf("create hard link: %v", err)
		}
		assertLoadFileUnavailable(t, path, nil)
	})

	t.Run("writable ancestor", func(t *testing.T) {
		trusted := testutil.TrustedTempDir(t)
		ancestor := filepath.Join(trusted, "unsafe")
		if err := os.Mkdir(ancestor, 0o700); err != nil {
			t.Fatalf("create ancestor: %v", err)
		}
		path := filepath.Join(ancestor, "gateway.key")
		testutil.WriteTrustedFile(t, path, []byte(testKey+"\n"), 0o600)
		if err := os.Chmod(ancestor, 0o770); err != nil {
			t.Fatalf("chmod ancestor: %v", err)
		}
		assertLoadFileUnavailable(t, path, nil)
	})
}

func TestLoadFileRejectsWrongOwnerAndSpecialBitsAtMetadataBoundary(t *testing.T) {
	path := writeUnixKeyFile(t, testKey+"\n", 0o600)
	var valid unix.Stat_t
	if err := unix.Lstat(path, &valid); err != nil {
		t.Fatalf("lstat fixture: %v", err)
	}
	if !safeUnixKeyStat(valid) {
		t.Fatal("baseline fixture metadata is not valid")
	}

	wrongOwner := valid
	wrongOwner.Uid++
	if safeUnixKeyStat(wrongOwner) {
		t.Fatal("metadata policy accepted a wrong owner")
	}
	for _, tt := range []struct {
		name   string
		mutate func(*unix.Stat_t)
	}{
		{name: "setuid", mutate: func(stat *unix.Stat_t) { stat.Mode |= unix.S_ISUID }},
		{name: "setgid", mutate: func(stat *unix.Stat_t) { stat.Mode |= unix.S_ISGID }},
		{name: "sticky", mutate: func(stat *unix.Stat_t) { stat.Mode |= unix.S_ISVTX }},
	} {
		modified := valid
		tt.mutate(&modified)
		if safeUnixKeyStat(modified) {
			t.Fatalf("metadata policy accepted %s mode", tt.name)
		}
	}
}

func TestLoadFileRejectsDistinctIdentityAndMalformedContent(t *testing.T) {
	path := writeUnixKeyFile(t, testKey+"\n", 0o600)
	identity, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	assertLoadFileUnavailable(t, path, []fs.FileInfo{nil, identity})

	malformed := writeUnixKeyFile(t, "not-a-key\n", 0o600)
	assertLoadFileUnavailable(t, malformed, nil)
}

func TestLoadFileParsesRetainedHandleOnceAndRejectsPathReplacement(t *testing.T) {
	path := writeUnixKeyFile(t, testKey+"\n", 0o600)
	replacement := writeUnixKeyFile(t, strings.Repeat("1", 64)+"\n", 0o600)
	calls := 0

	snapshot, err := loadFile(path, nil, func(reader io.Reader) (Snapshot, error) {
		calls++
		if calls != 1 {
			t.Fatal("parser called more than once")
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatalf("replace key path: %v", err)
		}
		return Parse(reader)
	})
	if err != ErrUnavailable {
		t.Fatalf("loadFile() error = %v, want ErrUnavailable", err)
	}
	if snapshot.Valid() {
		t.Fatal("loadFile() returned valid snapshot after path replacement")
	}
	if calls != 1 {
		t.Fatalf("parser calls = %d, want 1", calls)
	}
}

func assertLoadFileUnavailable(t *testing.T, path string, distinct []fs.FileInfo) {
	t.Helper()
	snapshot, err := LoadFile(path, distinct)
	if err != ErrUnavailable {
		t.Fatalf("LoadFile() error = %v, want exact ErrUnavailable", err)
	}
	if snapshot.Valid() || snapshot.Enabled() || snapshot.Matches(testKey) {
		t.Fatal("LoadFile() failure returned an authorizing snapshot")
	}
	if strings.Contains(err.Error(), path) && path != "" {
		t.Fatalf("LoadFile() error exposed path: %q", err)
	}
}

func writeUnixKeyFile(t *testing.T, payload string, mode fs.FileMode) string {
	t.Helper()
	directory := testutil.TrustedTempDir(t)
	path := filepath.Join(directory, "gateway.key")
	testutil.WriteTrustedFile(t, path, []byte(payload), mode)
	return path
}
