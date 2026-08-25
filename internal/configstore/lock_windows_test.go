//go:build windows

package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestCreatePrivateDirectoriesWindowsBuildsRootedChainAndRejectsUnsafe(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	target := filepath.Join(root, "one", "two")
	if err := NewWriter().createPrivateDirectories(context.Background(), []string{target}); err != nil {
		t.Fatalf("createPrivateDirectories() error = %v", err)
	}
	for _, path := range []string{filepath.Join(root, "one"), target} {
		if exists, err := inspectNativePrivateDirectory(path); err != nil || !exists {
			t.Fatalf("private directory %q = %t, %v", filepath.Base(path), exists, err)
		}
	}

	unsafe := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatalf("Mkdir unsafe fixture: %v", err)
	}
	installUnsafeWindowsStoreDACL(t, unsafe)
	child := filepath.Join(unsafe, "child")
	if err := NewWriter().createPrivateDirectories(
		context.Background(), []string{child},
	); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("createPrivateDirectories(unsafe) error = %v", err)
	}
	if _, err := os.Lstat(child); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe parent was repaired or used: %v", err)
	}
}

func TestLockWindowsSerializesAndValidatesPersistentSentinel(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	configPath := filepath.Join(root, "config.toml")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	first, err := NewWriter().acquireLock(context.Background(), base)
	if err != nil {
		t.Fatalf("first acquireLock() error = %v", err)
	}
	firstHeld := true
	t.Cleanup(func() {
		if firstHeld {
			_ = first.release()
		}
	})
	target, err := openNativeLoadTarget(LockPath(configPath))
	if err != nil || !target.exists || !safeNativeLockMetadata(target.metadata) {
		t.Fatalf("persistent lock = exists %t metadata %#v error %v", target.exists, target.metadata, err)
	}
	if target.file != nil {
		_ = target.file.Close()
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := NewWriter().acquireLock(waitCtx, base); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquireLock() error = %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	firstHeld = false
	third, err := NewWriter().acquireLock(context.Background(), base)
	if err != nil {
		t.Fatalf("third acquireLock() error = %v", err)
	}
	if err := third.release(); err != nil {
		t.Fatalf("release third lock: %v", err)
	}
	if _, err := os.Stat(LockPath(configPath)); err != nil {
		t.Fatalf("persistent sentinel missing: %v", err)
	}
	if windowsStoreUnsafeGrant == 0 {
		t.Fatal("Windows lock policy mask is empty")
	}

	t.Run("hardlink sentinel", func(t *testing.T) {
		fixtureRoot := testutil.TrustedTempDir(t)
		fixtureConfig := filepath.Join(fixtureRoot, "config.toml")
		lockPath := LockPath(fixtureConfig)
		testutil.WriteTrustedFile(t, lockPath, nil, 0o600)
		if err := os.Link(lockPath, lockPath+".alias"); err != nil {
			t.Fatalf("Link lock sentinel: %v", err)
		}
		fixtureBase, err := NewWriter().Load(context.Background(), fixtureConfig)
		if err != nil {
			t.Fatalf("Load hardlink fixture: %v", err)
		}
		if _, err := NewWriter().acquireLock(context.Background(), fixtureBase); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("acquireLock(hardlink) error = %v", err)
		}
	})
}
