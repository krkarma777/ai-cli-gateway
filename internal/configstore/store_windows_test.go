//go:build windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	return `"` + strings.ReplaceAll(filepath.ToSlash(value), `"`, `\"`) + `"`
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
