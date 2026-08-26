//go:build windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/windows"
)

func testConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(testutil.TrustedTempDir(t), "config.toml")
}

func TestLoadWindowsPrivateConfigAndRejectsUnsafeObjects(t *testing.T) {
	t.Run("missing and existing", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		path := filepath.Join(root, "config.toml")
		missing, err := NewWriter().Load(context.Background(), path)
		if err != nil || missing.Exists() || missing.Path() != path {
			t.Fatalf("Load(missing) = %#v, %v", missing, err)
		}
		payload := validWindowsStoreConfig(t, root, "")
		testutil.WriteTrustedFile(t, path, payload, 0o600)
		existing, err := NewWriter().Load(context.Background(), path)
		if err != nil || !existing.Exists() || !bytes.Equal(existing.Bytes(), payload) {
			t.Fatalf("Load(existing) = exists %t bytes %q error %v", existing.Exists(), existing.Bytes(), err)
		}
	})

	t.Run("unsafe DACL", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		path := filepath.Join(root, "config.toml")
		testutil.WriteTrustedFile(t, path, validWindowsStoreConfig(t, root, ""), 0o600)
		installUnsafeWindowsStoreDACL(t, path)
		if _, err := NewWriter().Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load(unsafe DACL) error = %v", err)
		}
	})

	t.Run("world-readable file DACL", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		path := filepath.Join(root, "config.toml")
		testutil.WriteTrustedFile(t, path, validWindowsStoreConfig(t, root, ""), 0o600)
		installWindowsStoreDACL(t, path, "D:P(A;;FA;;;%s)(A;;GR;;;WD)")
		if _, err := NewWriter().Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load(world-readable file) error = %v", err)
		}
	})

	t.Run("world-traversable directory DACL", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		directory := filepath.Join(root, "config")
		createWindowsTestPrivateDirectory(t, directory)
		path := filepath.Join(directory, "config.toml")
		testutil.WriteTrustedFile(t, path, validWindowsStoreConfig(t, root, ""), 0o600)
		installWindowsStoreDACL(t, directory, "D:P(A;;FA;;;%s)(A;;GRGX;;;WD)")
		if _, err := NewWriter().Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load(world-traversable directory) error = %v", err)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		path := filepath.Join(root, "config.toml")
		testutil.WriteTrustedFile(t, path, validWindowsStoreConfig(t, root, ""), 0o600)
		if err := os.Link(path, filepath.Join(root, "alias.toml")); err != nil {
			t.Fatalf("Link: %v", err)
		}
		if _, err := NewWriter().Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load(hardlink) error = %v", err)
		}
	})

	t.Run("reparse leaf", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		target := filepath.Join(root, "target.toml")
		link := filepath.Join(root, "config.toml")
		testutil.WriteTrustedFile(t, target, validWindowsStoreConfig(t, root, ""), 0o600)
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("creating a Windows symlink requires host support: %v", err)
		}
		if _, err := NewWriter().Load(context.Background(), link); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load(reparse) error = %v", err)
		}
	})
}
func TestLoadWindowsAllowsUntrustedCreateGrantOnNonPrivateAncestor(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	installWindowsStoreDACL(t, root, "D:P(A;;FA;;;%s)(A;;0x00000002;;;WD)")
	private := filepath.Join(root, "private")
	createWindowsTestPrivateDirectory(t, private)

	path := filepath.Join(private, "config.toml")
	snapshot, err := NewWriter().Load(context.Background(), path)
	if err != nil || snapshot.Exists() || snapshot.Path() != path {
		t.Fatalf("Load(missing below creatable ancestor) = %#v, %v", snapshot, err)
	}
}

func TestLoadWindowsIgnoresInheritOnlyAncestorAllow(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	private := filepath.Join(root, "private")
	createWindowsTestPrivateDirectory(t, private)
	installWindowsStoreDACL(
		t,
		root,
		"D:P(A;;FA;;;%s)(A;OICIIO;FA;;;WD)",
	)

	path := filepath.Join(private, "config.toml")
	snapshot, err := NewWriter().Load(context.Background(), path)
	if err != nil || snapshot.Exists() || snapshot.Path() != path {
		t.Fatalf("Load(missing below inherit-only grant) = %#v, %v", snapshot, err)
	}
}

func TestWindowsStoreFinalPathAcceptsTrustedTempLongName(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	handle, err := openWindowsStorePath(root, true)
	if err != nil {
		t.Fatalf("open trusted fixture: %v", err)
	}
	defer handle.Close() //nolint:errcheck // Test cleanup after assertion.

	if !windowsStoreFinalPathMatches(handle, root) {
		t.Fatalf("final path did not match trusted fixture %q", root)
	}
}

func TestWindowsStoreFinalPathAcceptsMountedVolumeRootByIdentity(t *testing.T) {
	fixture := testutil.TrustedTempDir(t)
	root := filepath.Clean(filepath.VolumeName(fixture) + `\`)
	if _, ok := cleanWindowsStorePath(root); ok {
		t.Fatalf("config path validation accepted bare volume root %q", root)
	}
	cleanRoot, ok := cleanWindowsStoreVolumeRoot(root)
	if !ok || !strings.EqualFold(cleanRoot, root) {
		t.Fatalf("volume root validation = %q/%t, want %q/true", cleanRoot, ok, root)
	}
	if _, ok := cleanWindowsStoreVolumeRoot(fixture); ok {
		t.Fatalf("volume root validation accepted non-root directory %q", fixture)
	}
	rootHandle, err := openWindowsStorePath(root, true)
	if err != nil {
		t.Fatalf("open mounted volume root: %v", err)
	}
	defer rootHandle.Close() //nolint:errcheck // Test cleanup after assertion.

	if !windowsStoreVolumeRootMatches(rootHandle, root) {
		t.Fatalf("mounted volume root identity did not match %q", root)
	}
	if !windowsStoreFinalPathMatches(rootHandle, root) {
		t.Fatalf("final path fallback did not match mounted volume root %q", root)
	}
	fixtureHandle, err := openWindowsStorePath(fixture, true)
	if err != nil {
		t.Fatalf("open trusted fixture: %v", err)
	}
	defer fixtureHandle.Close() //nolint:errcheck // Test cleanup after assertion.
	if windowsStoreVolumeRootMatches(fixtureHandle, root) {
		t.Fatal("non-root directory identity matched the mounted volume root")
	}
}

func TestLoadWindowsCanonicalizesShortAndLongConfigAliases(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	longPath := filepath.Join(root, "configuration-for-guided-init.toml")
	testutil.WriteTrustedFile(t, longPath, validWindowsStoreConfig(t, root, ""), 0o600)
	shortPath := windowsStoreShortPath(t, longPath)
	if strings.EqualFold(filepath.Base(shortPath), filepath.Base(longPath)) {
		t.Skip("test volume did not expose a DOS 8.3 alias for the config file")
	}

	longSnapshot, err := NewWriter().Load(context.Background(), longPath)
	if err != nil {
		t.Fatalf("Load(long path): %v", err)
	}
	shortSnapshot, err := NewWriter().Load(context.Background(), shortPath)
	if err != nil {
		t.Fatalf("Load(short path): %v", err)
	}
	if !strings.EqualFold(longSnapshot.Path(), shortSnapshot.Path()) ||
		!strings.EqualFold(LockPath(longSnapshot.Path()), LockPath(shortSnapshot.Path())) {
		t.Fatalf(
			"aliases retained different config/lock paths: long=%q short=%q",
			longSnapshot.Path(), shortSnapshot.Path(),
		)
	}
	longKey, longOK := nativeStorePathKey(longPath)
	shortKey, shortOK := nativeStorePathKey(shortPath)
	if !longOK || !shortOK || longKey != shortKey {
		t.Fatalf("alias keys = %q/%t and %q/%t", longKey, longOK, shortKey, shortOK)
	}

	first, err := NewWriter().acquireLock(context.Background(), longSnapshot)
	if err != nil {
		t.Fatalf("acquire long-path lock: %v", err)
	}
	defer first.release() //nolint:errcheck // Best-effort cleanup after assertions.
	waitCtx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	second, secondErr := NewWriter().acquireLock(waitCtx, shortSnapshot)
	if second != nil {
		_ = second.release()
	}
	if !errors.Is(secondErr, context.DeadlineExceeded) {
		t.Fatalf("short-path alias acquired a distinct lock: %v", secondErr)
	}
}

func TestLoadWindowsCanonicalizesMissingTargetBelowShortAncestor(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	longParent := filepath.Join(root, "private-configuration-directory")
	createWindowsTestPrivateDirectory(t, longParent)
	shortParent := windowsStoreShortPath(t, longParent)
	if strings.EqualFold(filepath.Base(shortParent), filepath.Base(longParent)) {
		t.Skip("test volume did not expose a DOS 8.3 alias for the existing parent")
	}

	longPath := filepath.Join(longParent, "future-configuration.toml")
	shortPath := filepath.Join(shortParent, filepath.Base(longPath))
	longSnapshot, err := NewWriter().Load(context.Background(), longPath)
	if err != nil {
		t.Fatalf("Load(long missing path): %v", err)
	}
	shortSnapshot, err := NewWriter().Load(context.Background(), shortPath)
	if err != nil {
		t.Fatalf("Load(short missing path): %v", err)
	}
	if longSnapshot.Exists() || shortSnapshot.Exists() ||
		!strings.EqualFold(longSnapshot.Path(), shortSnapshot.Path()) ||
		!strings.EqualFold(LockPath(longSnapshot.Path()), LockPath(shortSnapshot.Path())) {
		t.Fatalf(
			"missing aliases retained different config/lock paths: long=%q short=%q",
			longSnapshot.Path(), shortSnapshot.Path(),
		)
	}
}

func windowsStoreShortPath(t *testing.T, path string) string {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	size := uint32(windows.MAX_PATH)
	for {
		buffer := make([]uint16, size)
		length, err := windows.GetShortPathName(pointer, &buffer[0], size)
		if err != nil || length == 0 {
			t.Skipf("DOS 8.3 aliases unavailable: %v", err)
		}
		if length >= size {
			size = length + 1
			continue
		}
		return windows.UTF16ToString(buffer[:length])
	}
}

func TestNativePathWindowsRejectsAmbiguousComponentsAndRequiresACLVolume(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	volume := filepath.VolumeName(root)
	if _, ok := windowsStoreVolumePolicy(volume); !ok {
		t.Fatalf("trusted test volume %q does not prove fixed persistent-ACL policy", volume)
	}
	for _, component := range []string{
		"CON", "CONIN$", "CONOUT$", "CON .txt", "COM¹", "LPT³.log",
		"bad<name", "bad|name", "bad?name", strings.Repeat("a", 256),
	} {
		path := filepath.Join(root, component, "config.toml")
		if _, ok := cleanWindowsStorePath(path); ok {
			t.Fatalf("cleanWindowsStorePath(%q) accepted", component)
		}
	}
}

func validWindowsStoreConfig(t *testing.T, root string, keyPath string) []byte {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	auth := ""
	if keyPath != "" {
		auth = "api_key_file = " + windowsStoreTOMLValue(keyPath) + "\n"
	}
	return []byte("[server]\n" + auth + "\n[runtime]\nroot = " +
		windowsStoreTOMLValue(filepath.Join(root, "runtime")) + "\n\n" +
		"[providers.codex]\nexecutable = " + windowsStoreTOMLValue(executable) + "\n" +
		"config_home = " + windowsStoreTOMLValue(filepath.Join(root, "provider-home")) + "\n\n" +
		"[[models]]\nid = 'codex-test'\nprovider = 'codex'\nprovider_model = 'gpt-test'\n")
}

func windowsStoreTOMLValue(value string) string {
	return strconv.Quote(value)
}

func installUnsafeWindowsStoreDACL(t *testing.T, path string) {
	t.Helper()
	installWindowsStoreDACL(t, path, "D:P(A;;FA;;;WD)")
}

func installWindowsStoreDACL(t *testing.T, path, format string) {
	t.Helper()
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatalf("GetTokenUser: %v", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		format, user.User.Sid.String(),
	))
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatalf("SetNamedSecurityInfo: %v", err)
	}
}
