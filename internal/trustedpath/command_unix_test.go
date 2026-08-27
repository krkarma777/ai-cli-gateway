//go:build !windows

package trustedpath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestOpenTrustedCommandIdentityOnlyRetainsExecutableWithoutReading(t *testing.T) {
	root := newTrustedCommandUnixTree(t)
	path := filepath.Join(root, "execute-only")
	writeTrustedCommandUnixFile(t, path, []byte("must-not-be-read"), 0o100)

	inspection, err := OpenCommandPath(path, CommandIdentityOnly, 0)
	if err != nil {
		t.Fatalf("OpenCommandPath() error = %v", err)
	}
	if got := inspection.Bytes(); len(got) != 0 {
		t.Fatalf("Bytes() = %q, want empty identity-only evidence", got)
	}
	if info := inspection.FileInfo(); info == nil || !info.Mode().IsRegular() {
		t.Fatalf("FileInfo() = %#v, want regular file", info)
	}
	metadata, ok := InspectionPath(inspection)
	if !ok || metadata.Clean != path || metadata.Resolved != path {
		t.Fatalf("InspectionPath() = %+v/%v, want %q", metadata, ok, path)
	}
	if err := inspection.Revalidate(); err != nil {
		t.Fatalf("Revalidate() error = %v", err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenTrustedCommandUsesAtomicCloseOnExecFlag(t *testing.T) {
	for _, mode := range []CommandReadMode{
		CommandIdentityOnly,
		CommandBoundedContent,
	} {
		flags := unixCommandOpenFlags(mode)
		if closeOnExec := unixCommandCloseOnExecFlag(); closeOnExec == 0 ||
			flags&closeOnExec == 0 {
			t.Fatalf("open flags %#x omit atomic close-on-exec %#x", flags, closeOnExec)
		}
	}
}

func TestUnixCommandIdentityFallbackPreservesDoctorAuthorityClasses(t *testing.T) {
	euid := uint32(os.Geteuid()) //nolint:gosec // Kernel UIDs use uint32.
	other := euid + 1
	if other == 0 {
		other++
	}

	for _, test := range []struct {
		name string
		mode fs.FileMode
		uid  uint32
		want bool
	}{
		{name: "root owner execute only", mode: 0o100, uid: 0, want: true},
		{name: "root owner read execute", mode: 0o500, uid: 0, want: true},
		{name: "root other execute only", mode: 0o001, uid: 0, want: true},
		{name: "current owner group execute only", mode: 0o010, uid: euid, want: true},
		{name: "trusted owner read without execute", mode: 0o400, uid: 0, want: false},
		{name: "trusted owner no permissions", mode: 0, uid: euid, want: false},
		{name: "untrusted owner executable", mode: 0o700, uid: other, want: false},
		{name: "trusted owner writable by others", mode: 0o702, uid: euid, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := trustedUnixFileInfo{mode: test.mode, uid: test.uid}
			if got := unixCommandIdentityFallbackAllowed(info); got != test.want {
				t.Fatalf("unixCommandIdentityFallbackAllowed() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestValidateUnixFreshCommandEvidenceRejectsOwnerChange(t *testing.T) {
	euid := uint32(os.Geteuid()) //nolint:gosec // Kernel UIDs use uint32.
	other := euid + 1
	if other == 0 {
		other++
	}
	original := trustedUnixFileInfo{mode: 0o700, uid: euid, ino: 73}
	fresh := trustedUnixFileInfo{mode: 0o700, uid: other, ino: 73}
	if err := validateUnixFreshCommandEvidence(original, fresh); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("validateUnixFreshCommandEvidence() error = %v, want ErrUnsafe", err)
	}
}

func TestOpenTrustedCommandDarwinRetainsParentForInaccessibleTrustedLeaf(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Geteuid() == 0 {
		t.Skip("Darwin non-root retained-parent fixture")
	}
	const path = "/usr/libexec/cups/backend/lpd"
	info, err := os.Lstat(path)
	if err != nil {
		t.Skipf("trusted system fixture unavailable: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm() != 0o700 {
		t.Skipf("fixture owner/mode changed: mode=%v stat=%T", info.Mode(), info.Sys())
	}

	if direct, openErr := openUnixCommand(path, CommandIdentityOnly); openErr == nil {
		_ = direct.Close()
		t.Fatal("fixture unexpectedly permits a direct leaf handle")
	} else if !errors.Is(openErr, fs.ErrPermission) {
		t.Fatalf("direct leaf open error = %v, want permission failure", openErr)
	}

	inspection, err := OpenCommandPath(path, CommandIdentityOnly, 0)
	if err != nil {
		t.Fatalf("OpenCommandPath() error = %v", err)
	}
	concrete, ok := inspection.(*unixCommandInspection)
	if !ok || concrete.parent == nil || concrete.parent.path != filepath.Dir(path) {
		_ = inspection.Close()
		t.Fatalf("inspection does not retain parent evidence: %#v", inspection)
	}
	if err := inspection.Revalidate(); err != nil {
		_ = inspection.Close()
		t.Fatalf("Revalidate() error = %v", err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenTrustedCommandDarwinParentEvidenceRejectsMutation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin retained-parent mutation fixtures")
	}
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, parent, path string)
	}{
		{
			name: "leaf identity replacement",
			mutate: func(t *testing.T, _ string, path string) {
				t.Helper()
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				writeTrustedCommandUnixFile(t, path, []byte("replacement"), 0o010)
			},
		},
		{
			name: "unsafe leaf mode",
			mutate: func(t *testing.T, _ string, path string) {
				t.Helper()
				//nolint:gosec // The fixture deliberately adds an unsafe group-write grant.
				if err := os.Chmod(path, 0o030); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "leaf symlink replacement",
			mutate: func(t *testing.T, _ string, path string) {
				t.Helper()
				old := path + ".old"
				if err := os.Rename(path, old); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(old, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "parent identity replacement",
			mutate: func(t *testing.T, parent, path string) {
				t.Helper()
				old := parent + ".old"
				if err := os.Rename(parent, old); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				writeTrustedCommandUnixFile(t, path, []byte("replacement"), 0o010)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newTrustedCommandUnixTree(t)
			parent := filepath.Join(root, "bin")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "provider")
			writeTrustedCommandUnixFile(t, path, []byte("original"), 0o010)
			inspection, err := OpenCommandPath(path, CommandIdentityOnly, 0)
			if err != nil {
				t.Fatalf("OpenCommandPath() error = %v", err)
			}
			defer inspection.Close() //nolint:errcheck // Test cleanup after assertions.
			concrete, ok := inspection.(*unixCommandInspection)
			if !ok || concrete.parent == nil {
				t.Fatal("fixture did not select retained-parent evidence")
			}
			test.mutate(t, parent, path)
			if err := inspection.Revalidate(); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("Revalidate() error = %v, want ErrUnsafe", err)
			}
		})
	}
}

func TestOpenTrustedCommandBoundedContentRequiresWholeFile(t *testing.T) {
	root := newTrustedCommandUnixTree(t)
	path := filepath.Join(root, "provider.cmd")
	payload := []byte("closed command fixture")
	writeTrustedCommandUnixFile(t, path, payload, 0o500)

	inspection, err := OpenCommandPath(
		path,
		CommandBoundedContent,
		int64(len(payload)),
	)
	if err != nil {
		t.Fatalf("OpenCommandPath() error = %v", err)
	}
	got := inspection.Bytes()
	if !slices.Equal(got, payload) {
		t.Fatalf("Bytes() = %q, want %q", got, payload)
	}
	got[0] ^= 0xff
	if slices.Equal(got, inspection.Bytes()) {
		t.Fatal("Bytes() aliases retained content")
	}
	if err := inspection.Revalidate(); err != nil {
		t.Fatalf("Revalidate() error = %v", err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := OpenCommandPath(
		path,
		CommandBoundedContent,
		int64(len(payload)-1),
	); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("overflow error = %v, want ErrUnsafe", err)
	}
	for _, test := range []struct {
		mode  CommandReadMode
		limit int64
	}{
		{mode: 0, limit: 0},
		{mode: CommandIdentityOnly, limit: 1},
		{mode: CommandBoundedContent, limit: 0},
		{mode: CommandBoundedContent, limit: -1},
	} {
		if _, err := OpenCommandPath(path, test.mode, test.limit); !errors.Is(err, ErrUnsafe) {
			t.Errorf("OpenCommandPath(mode=%v, limit=%d) error = %v, want ErrUnsafe", test.mode, test.limit, err)
		}
	}
}

func TestOpenTrustedCommandPreservesDoctorUnixDispositionTable(t *testing.T) {
	root := newTrustedCommandUnixTree(t)
	safe := filepath.Join(root, "safe")
	writeTrustedCommandUnixFile(t, safe, []byte("safe"), 0o700)
	missing := filepath.Join(root, "missing")
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(safe, alias); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "broken")
	if err := os.Symlink(missing, broken); err != nil {
		t.Fatal(err)
	}
	noExecute := filepath.Join(root, "no-execute")
	writeTrustedCommandUnixFile(t, noExecute, nil, 0o600)
	writable := filepath.Join(root, "writable")
	writeTrustedCommandUnixFile(t, writable, nil, 0o720)
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeParent := filepath.Join(root, "unsafe-parent")
	if err := os.Mkdir(unsafeParent, 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeAncestor := filepath.Join(unsafeParent, "provider")
	writeTrustedCommandUnixFile(t, unsafeAncestor, nil, 0o700)
	//nolint:gosec // This fixture intentionally models an unsafe writable ancestor.
	if err := os.Chmod(unsafeParent, 0o770); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr error
	}{
		{name: "safe", path: safe},
		{name: "safe symlink", path: alias},
		{name: "missing", path: missing, wantErr: ErrMissing},
		{name: "broken symlink", path: broken, wantErr: ErrUnsafe},
		{name: "relative", path: "relative", wantErr: ErrUnsafe},
		{name: "NUL", path: safe + "\x00tail", wantErr: ErrUnsafe},
		{name: "no execute", path: noExecute, wantErr: ErrUnsafe},
		{name: "writable leaf", path: writable, wantErr: ErrUnsafe},
		{name: "directory", path: directory, wantErr: ErrUnsafe},
		{name: "writable ancestor", path: unsafeAncestor, wantErr: ErrUnsafe},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection, err := OpenCommandPath(test.path, CommandIdentityOnly, 0)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("OpenCommandPath() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenCommandPath() error = %v", err)
			}
			defer inspection.Close() //nolint:errcheck // Test cleanup after the assertion.
			metadata, ok := InspectionPath(inspection)
			if !ok || metadata.Clean != filepath.Clean(test.path) {
				t.Fatalf("InspectionPath() = %+v/%v", metadata, ok)
			}
			if test.path == alias && metadata.Resolved != safe {
				t.Fatalf("resolved alias = %q, want %q", metadata.Resolved, safe)
			}
		})
	}
}

func TestOpenTrustedCommandRevalidateRejectsPathIdentityAndMetadataChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mode   CommandReadMode
		limit  int64
		mutate func(t *testing.T, path string)
	}{
		{
			name: "same spelling replacement",
			mode: CommandIdentityOnly,
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				writeTrustedCommandUnixFile(t, path, []byte("replacement"), 0o700)
			},
		},
		{
			name: "mode change",
			mode: CommandIdentityOnly,
			mutate: func(t *testing.T, path string) {
				t.Helper()
				//nolint:gosec // This fixture intentionally makes the retained command unsafe.
				if err := os.Chmod(path, 0o720); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "bounded content change",
			mode:  CommandBoundedContent,
			limit: 64,
			mutate: func(t *testing.T, path string) {
				t.Helper()
				//nolint:gosec // The exact executable fixture mode is restored by WriteFile.
				if err := os.WriteFile(path, []byte("changed payload"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newTrustedCommandUnixTree(t)
			path := filepath.Join(root, "provider")
			writeTrustedCommandUnixFile(t, path, []byte("initial payload"), 0o700)
			inspection, err := OpenCommandPath(path, test.mode, test.limit)
			if err != nil {
				t.Fatal(err)
			}
			defer inspection.Close() //nolint:errcheck // Test cleanup after the assertion.
			test.mutate(t, path)
			if err := inspection.Revalidate(); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("Revalidate() error = %v, want ErrUnsafe", err)
			}
		})
	}
}

func TestOpenTrustedCommandAcceptsTrustedSystemExecutable(t *testing.T) {
	path := "/bin/ls"
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		t.Skip("/bin/ls is unavailable")
	} else if err != nil {
		t.Fatal(err)
	}
	inspection, err := OpenCommandPath(path, CommandIdentityOnly, 0)
	if err != nil {
		t.Fatalf("OpenCommandPath(%q) error = %v", path, err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatal(err)
	}
}

func newTrustedCommandUnixTree(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(".", ".trusted-command-test-")
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Owner-only authority is the policy under test.
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(absolute)
}

func writeTrustedCommandUnixFile(
	t *testing.T,
	path string,
	payload []byte,
	mode fs.FileMode,
) {
	t.Helper()
	//nolint:gosec // Callers provide the exact executable fixture mode under test.
	if err := os.WriteFile(path, payload, mode); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Callers provide the exact executable fixture mode under test.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

type trustedUnixFileInfo struct {
	mode fs.FileMode
	uid  uint32
	ino  uint64
}

func (i trustedUnixFileInfo) Name() string       { return "test" }
func (i trustedUnixFileInfo) Size() int64        { return 0 }
func (i trustedUnixFileInfo) Mode() fs.FileMode  { return i.mode }
func (i trustedUnixFileInfo) ModTime() time.Time { return time.Time{} }
func (i trustedUnixFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i trustedUnixFileInfo) Sys() any {
	return &syscall.Stat_t{Uid: i.uid, Ino: i.ino}
}
