//go:build windows

package gatewaykey

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/windows"
)

func TestLoadFileAcceptsOwnerOnlyLocalRegularFile(t *testing.T) {
	path := writeWindowsKeyFile(t, testKey+"\n")
	snapshot, err := LoadFile(path, nil)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if !snapshot.Valid() || !snapshot.Enabled() || !snapshot.Matches(testKey) {
		t.Fatal("LoadFile() did not return the expected enabled snapshot")
	}
}

func TestLoadFileRejectsUnsafeWindowsAncestorDACL(t *testing.T) {
	directory := testutil.TrustedTempDir(t)
	ancestor := filepath.Join(directory, "unsafe")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatalf("create ancestor: %v", err)
	}
	path := filepath.Join(ancestor, "gateway.key")
	testutil.WriteTrustedFile(t, path, []byte(testKey+"\n"), 0o600)

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("get token user: %v", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"D:P(A;;FA;;;%s)(A;;FA;;;WD)",
		user.User.Sid.String(),
	))
	if err != nil {
		t.Fatalf("construct unsafe DACL: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("extract unsafe DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		ancestor,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatalf("install unsafe DACL: %v", err)
	}

	assertWindowsLoadUnavailable(t, path, nil)
}

func TestLoadFileRejectsWindowsPathShapeAndNonRegularObjects(t *testing.T) {
	trusted := testutil.TrustedTempDir(t)
	directory := filepath.Join(trusted, "directory.key")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create directory fixture: %v", err)
	}
	for _, tt := range []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative", path: `relative\gateway.key`},
		{name: "drive relative", path: `C:gateway.key`},
		{name: "rooted without drive", path: `\gateway.key`},
		{name: "UNC", path: `\\server\share\gateway.key`},
		{name: "device", path: `\\.\NUL`},
		{name: "directory", path: directory},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertWindowsLoadUnavailable(t, tt.path, nil)
		})
	}
}

func TestLoadFileRejectsWindowsHardLinksReparseAndIdentityCollision(t *testing.T) {
	t.Run("hard link", func(t *testing.T) {
		path := writeWindowsKeyFile(t, testKey+"\n")
		if err := os.Link(path, path+".alias"); err != nil {
			t.Fatalf("create hard link: %v", err)
		}
		assertWindowsLoadUnavailable(t, path, nil)
	})

	t.Run("reparse leaf", func(t *testing.T) {
		path := writeWindowsKeyFile(t, testKey+"\n")
		link := path + ".link"
		if err := os.Symlink(path, link); err != nil {
			t.Skipf("creating a Windows symlink requires host support: %v", err)
		}
		assertWindowsLoadUnavailable(t, link, nil)
	})

	t.Run("distinct identity", func(t *testing.T) {
		path := writeWindowsKeyFile(t, testKey+"\n")
		identity, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat key: %v", err)
		}
		assertWindowsLoadUnavailable(t, path, []fs.FileInfo{nil, identity})
	})
}

func TestLoadFileRejectsWindowsMalformedContent(t *testing.T) {
	assertWindowsLoadUnavailable(t, writeWindowsKeyFile(t, "not-a-key\n"), nil)
}

func TestLoadFileParsesWindowsRetainedHandleOnceAndRejectsReplacement(t *testing.T) {
	path := writeWindowsKeyFile(t, testKey+"\n")
	replacement := writeWindowsKeyFile(t, strings.Repeat("1", 64)+"\n")
	calls := 0

	snapshot, err := loadFile(path, nil, func(reader io.Reader) (Snapshot, error) {
		calls++
		if err := os.Rename(replacement, path); err != nil {
			t.Skipf("host sharing policy did not permit replacement: %v", err)
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

func assertWindowsLoadUnavailable(t *testing.T, path string, distinct []fs.FileInfo) {
	t.Helper()
	snapshot, err := LoadFile(path, distinct)
	if err != ErrUnavailable {
		t.Fatalf("LoadFile() error = %v, want exact ErrUnavailable", err)
	}
	if snapshot.Valid() || snapshot.Matches(testKey) {
		t.Fatal("LoadFile() failure returned an authorizing snapshot")
	}
}

func writeWindowsKeyFile(t *testing.T, payload string) string {
	t.Helper()
	directory := testutil.TrustedTempDir(t)
	path := filepath.Join(directory, "gateway.key")
	testutil.WriteTrustedFile(t, path, []byte(payload), 0o600)
	return path
}
