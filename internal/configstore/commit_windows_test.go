//go:build windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/windows"
)

func TestCommitWindowsHasNoDirectorySyncBoundary(t *testing.T) {
	t.Parallel()
	if supportsConfigDirectorySync {
		t.Fatal("Windows must not claim directory fsync support")
	}
}

func TestCommitWindowsFreshNestedConfigAndImmediateBackup(t *testing.T) {
	t.Run("fresh nested", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		configPath := filepath.Join(root, "config", "nested", "config.toml")
		base, err := NewWriter().Load(context.Background(), configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		candidate := validWindowsStoreConfig(t, root, "")
		result, err := NewWriter().Commit(context.Background(), Mutation{
			Base: base, Candidate: candidate,
		}, nil)
		if err != nil || result != (CommitResult{State: CommitCommitted, ConfigChanged: true}) {
			t.Fatalf("Commit() = %#v, %v", result, err)
		}
		assertWindowsStoreBytes(t, configPath, candidate)
		if target, err := openNativeLoadTarget(LockPath(configPath)); err != nil ||
			!target.exists || !safeNativeLockMetadata(target.metadata) {
			t.Fatalf("lock sentinel = exists %t metadata %#v error %v", target.exists, target.metadata, err)
		} else if target.file != nil {
			_ = target.file.Close()
		}
	})

	t.Run("existing backup", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		configPath := filepath.Join(root, "config.toml")
		prior := validWindowsStoreConfig(t, root, "")
		testutil.WriteTrustedFile(t, configPath, prior, 0o600)
		base, err := NewWriter().Load(context.Background(), configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		candidate := bytes.Replace(prior, []byte("gpt-test"), []byte("gpt-next"), 1)
		result, err := NewWriter().Commit(context.Background(), Mutation{
			Base: base, Candidate: candidate,
		}, nil)
		if err != nil || result != (CommitResult{
			State: CommitCommitted, ConfigChanged: true, BackupPath: BackupPath(configPath),
		}) {
			t.Fatalf("Commit() = %#v, %v", result, err)
		}
		assertWindowsStoreBytes(t, configPath, candidate)
		assertWindowsStoreBytes(t, BackupPath(configPath), prior)
	})
}

func TestCommitWindowsRejectsDirectoryReplacementAfterLock(t *testing.T) {
	root := testutil.TrustedTempDir(t)
	configDirectory := filepath.Join(root, "config")
	configPath := filepath.Join(configDirectory, "config.toml")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	detached := filepath.Join(root, "detached")
	writer := NewWriter()
	writer.ops.commitHook = func(point commitPoint) error {
		if point != commitAfterLock {
			return nil
		}
		if err := os.Rename(configDirectory, detached); err != nil {
			t.Fatalf("Rename config directory: %v", err)
		}
		createWindowsTestPrivateDirectory(t, configDirectory)
		return nil
	}
	result, err := writer.Commit(context.Background(), Mutation{
		Base: base, Candidate: validWindowsStoreConfig(t, root, ""),
	}, nil)
	if result != (CommitResult{}) || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	if _, err := os.Lstat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement directory received config: %v", err)
	}
}

func createWindowsTestPrivateDirectory(t *testing.T, path string) {
	t.Helper()
	attributes, descriptor, err := windowsStoreSecurityAttributes(true)
	if err != nil {
		t.Fatalf("windowsStoreSecurityAttributes: %v", err)
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	err = windows.CreateDirectory(pointer, attributes)
	runtime.KeepAlive(descriptor)
	if err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}
}

func assertWindowsStoreBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path) // #nosec G304 -- exact test-owned path.
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadFile(%q) = %q, %v", filepath.Base(path), got, err)
	}
}
