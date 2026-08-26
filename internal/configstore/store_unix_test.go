//go:build !windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestLoadMissingAndExistingPrivateConfig(t *testing.T) {
	t.Parallel()

	directory := privateStoreDir(t)
	path := filepath.Join(directory, "config.toml")
	writer := NewWriter()

	missing, err := writer.Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load(missing) error = %v", err)
	}
	if missing.Exists() || missing.Path() != path || missing.Bytes() != nil {
		t.Fatalf("missing snapshot = exists %t path %q bytes %v", missing.Exists(), missing.Path(), missing.Bytes())
	}

	want := validStoreConfig(t)
	writePrivateStoreFile(t, path, want, 0o600)
	existing, err := writer.Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load(existing) error = %v", err)
	}
	if !existing.Exists() || existing.Path() != path || !bytes.Equal(existing.Bytes(), want) {
		t.Fatalf("existing snapshot = exists %t path %q bytes %q", existing.Exists(), existing.Path(), existing.Bytes())
	}
	copyBytes := existing.Bytes()
	copyBytes[0] ^= 0xff
	if bytes.Equal(existing.Bytes(), copyBytes) || !bytes.Equal(existing.Bytes(), want) {
		t.Fatal("Snapshot.Bytes() did not return a defensive copy")
	}
}

func TestLoadAcceptsOnlyBoundedValidPrivateRegularFile(t *testing.T) {
	t.Parallel()

	for _, mode := range []os.FileMode{0o400, 0o600} {
		mode := mode
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(privateStoreDir(t), "config.toml")
			writePrivateStoreFile(t, path, validStoreConfig(t), mode)
			if _, err := NewWriter().Load(context.Background(), path); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}

	for name, setup := range map[string]func(*testing.T, string){
		"public mode": func(t *testing.T, path string) {
			writePrivateStoreFile(t, path, validStoreConfig(t), 0o644)
		},
		"malformed config": func(t *testing.T, path string) {
			writePrivateStoreFile(t, path, []byte("not = [toml"), 0o600)
		},
		"more than one MiB": func(t *testing.T, path string) {
			writePrivateStoreFile(t, path, bytes.Repeat([]byte{'x'}, maxConfigBytes+1), 0o600)
		},
		"directory leaf": func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatalf("Mkdir: %v", err)
			}
		},
	} {
		name, setup := name, setup
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(privateStoreDir(t), "config.toml")
			setup(t, path)
			_, err := NewWriter().Load(context.Background(), path)
			if name == "malformed config" {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("Load() error = %v, want ErrInvalidConfig", err)
				}
				return
			}
			if !errors.Is(err, ErrUnsafePath) && !errors.Is(err, ErrStore) {
				t.Fatalf("Load() error = %v, want closed store error", err)
			}
		})
	}
}

func TestLoadRejectsUncleanLinksUnsafeAncestorsAndReadReplacement(t *testing.T) {
	t.Parallel()

	t.Run("unclean", func(t *testing.T) {
		t.Parallel()
		directory := privateStoreDir(t)
		path := directory + string(filepath.Separator) + "child" +
			string(filepath.Separator) + ".." + string(filepath.Separator) + "config.toml"
		if _, err := NewWriter().Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		directory := privateStoreDir(t)
		target := filepath.Join(directory, "target.toml")
		path := filepath.Join(directory, "config.toml")
		writePrivateStoreFile(t, target, validStoreConfig(t), 0o600)
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if _, err := NewWriter().Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		t.Parallel()
		directory := privateStoreDir(t)
		path := filepath.Join(directory, "config.toml")
		link := filepath.Join(directory, "other.toml")
		writePrivateStoreFile(t, path, validStoreConfig(t), 0o600)
		if err := os.Link(path, link); err != nil {
			t.Fatalf("Link: %v", err)
		}
		if _, err := NewWriter().Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("unsafe parent", func(t *testing.T) {
		t.Parallel()
		directory := privateStoreDir(t)
		path := filepath.Join(directory, "config.toml")
		writePrivateStoreFile(t, path, validStoreConfig(t), 0o600)
		if err := os.Chmod(directory, 0o770); err != nil { // #nosec G302 -- intentionally unsafe fixture.
			t.Fatalf("Chmod: %v", err)
		}
		if _, err := NewWriter().Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("replacement during read", func(t *testing.T) {
		directory := privateStoreDir(t)
		path := filepath.Join(directory, "config.toml")
		replacement := filepath.Join(directory, "replacement.toml")
		writePrivateStoreFile(t, path, validStoreConfig(t), 0o600)
		writePrivateStoreFile(t, replacement, validStoreConfig(t), 0o600)
		writer := NewWriter()
		writer.ops.afterRead = func() {
			if err := os.Rename(replacement, path); err != nil {
				t.Fatalf("Rename: %v", err)
			}
		}
		if _, err := writer.Load(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Load() error = %v, want ErrUnsafePath", err)
		}
	})
}

func privateStoreDir(t *testing.T) string {
	t.Helper()
	return testutil.TrustedTempDir(t)
}

func testConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(privateStoreDir(t), "config.toml")
}

func validStoreConfig(t *testing.T) []byte {
	t.Helper()
	root := filepath.Join(privateStoreDir(t), "runtime")
	home := filepath.Join(privateStoreDir(t), "provider-home")
	return []byte("[runtime]\nroot = " + quotedStoreValue(root) + "\n\n" +
		"[providers.codex]\nexecutable = '/bin/echo'\nconfig_home = " + quotedStoreValue(home) + "\n\n" +
		"[[models]]\nid = 'codex-test'\nprovider = 'codex'\nprovider_model = 'gpt-test'\n")
}

func quotedStoreValue(value string) string {
	return `"` + filepath.ToSlash(value) + `"`
}

func writePrivateStoreFile(t *testing.T, path string, value []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, value, mode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
}
