//go:build !windows

package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreatePrivateDirectoriesBuildsComponentsAndNeverRepairsUnsafeOnes(t *testing.T) {
	t.Parallel()

	t.Run("creates components", func(t *testing.T) {
		t.Parallel()
		root := privateStoreDir(t)
		first := filepath.Join(root, "one")
		second := filepath.Join(first, "two")
		if err := NewWriter().createPrivateDirectories(context.Background(), []string{second}); err != nil {
			t.Fatalf("createPrivateDirectories() error = %v", err)
		}
		for _, path := range []string{first, second} {
			info, err := os.Lstat(path)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("created directory %q info=%v err=%v", filepath.Base(path), info, err)
			}
		}
	})

	t.Run("rejects unsafe existing component", func(t *testing.T) {
		t.Parallel()
		root := privateStoreDir(t)
		unsafe := filepath.Join(root, "unsafe")
		if err := os.Mkdir(unsafe, 0o755); err != nil { // #nosec G301 -- intentionally unsafe fixture.
			t.Fatalf("Mkdir: %v", err)
		}
		if err := os.Chmod(unsafe, 0o755); err != nil { // #nosec G302 -- intentionally unsafe fixture.
			t.Fatalf("Chmod: %v", err)
		}
		fixtureInfo, err := os.Stat(unsafe)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if got := fixtureInfo.Mode().Perm(); got != 0o755 {
			t.Fatalf("unsafe directory mode = %04o, want 0755", got)
		}
		if err := NewWriter().createPrivateDirectories(
			context.Background(),
			[]string{unsafe, filepath.Join(unsafe, "child")},
		); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("createPrivateDirectories() error = %v, want ErrUnsafePath", err)
		}
		info, err := os.Lstat(unsafe)
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("unsafe directory was modified: info=%v err=%v", info, err)
		}
	})
}

func TestLockSerializesWritersPersistsAndHonorsCancellation(t *testing.T) {
	t.Parallel()

	root := privateStoreDir(t)
	configPath := filepath.Join(root, "config", "config.toml")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	first, err := NewWriter().acquireLock(context.Background(), base)
	if err != nil {
		t.Fatalf("first acquireLock() error = %v", err)
	}
	lockPath := LockPath(configPath)
	info, err := os.Lstat(lockPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock info=%v err=%v", info, err)
	}

	secondBase, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := NewWriter().acquireLock(waitCtx, secondBase); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquireLock() error = %v, want deadline", err)
	}
	if _, err := os.Lstat(lockPath); err != nil {
		t.Fatalf("held lock sentinel disappeared: %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("first release() error = %v", err)
	}
	if _, err := os.Lstat(lockPath); err != nil {
		t.Fatalf("released lock sentinel disappeared: %v", err)
	}
	third, err := NewWriter().acquireLock(context.Background(), secondBase)
	if err != nil {
		t.Fatalf("third acquireLock() error = %v", err)
	}
	if err := third.release(); err != nil {
		t.Fatalf("third release() error = %v", err)
	}
}

func TestLockRejectsSymlinkAndHardlinkSentinels(t *testing.T) {
	t.Parallel()

	for name, setup := range map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, lockPath string) {
			target := lockPath + ".target"
			writePrivateStoreFile(t, target, nil, 0o600)
			if err := os.Symlink(target, lockPath); err != nil {
				t.Fatalf("Symlink: %v", err)
			}
		},
		"hardlink": func(t *testing.T, lockPath string) {
			writePrivateStoreFile(t, lockPath, nil, 0o600)
			if err := os.Link(lockPath, lockPath+".other"); err != nil {
				t.Fatalf("Link: %v", err)
			}
		},
	} {
		name, setup := name, setup
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := privateStoreDir(t)
			configPath := filepath.Join(root, "config.toml")
			base, err := NewWriter().Load(context.Background(), configPath)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			setup(t, LockPath(configPath))
			if _, err := NewWriter().acquireLock(context.Background(), base); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("acquireLock() error = %v, want ErrUnsafePath", err)
			}
		})
	}
}
