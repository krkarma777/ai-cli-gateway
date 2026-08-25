//go:build !windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitFreshConfigAndGatewayKeyAtomically(t *testing.T) {
	t.Parallel()

	root := privateStoreDir(t)
	configPath := filepath.Join(root, "config.toml")
	keyDirectory := filepath.Join(root, "keys")
	keyPath := filepath.Join(keyDirectory, "gateway.key")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	candidate := storeCandidate(t, keyPath, filepath.Join(root, "provider-home"))
	mutation := Mutation{
		Base:      base,
		Candidate: candidate,
		Key: KeyPlan{
			Intent:       KeyIntentEnsure,
			Path:         keyPath,
			DistinctFrom: []string{configPath},
		},
		PrivateDirs: []string{keyDirectory},
	}
	payload := gatewayKeyPayload('a')
	result, err := NewWriter().Commit(context.Background(), mutation, payload)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if result.State != CommitCommitted || !result.ConfigChanged ||
		!result.KeyCreated || result.BackupPath != "" {
		t.Fatalf("Commit() result = %#v", result)
	}
	assertPrivateFileBytes(t, configPath, candidate)
	assertPrivateFileBytes(t, keyPath, payload)
	if _, err := os.Lstat(BackupPath(configPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh commit created backup: %v", err)
	}
	assertPersistentLockAndNoTransactionTemps(t, configPath, keyPath)
}

func TestCommitRejectsConfigDirectoryReplacementAfterLockAcquisition(t *testing.T) {
	t.Parallel()

	parent := privateStoreDir(t)
	configDirectory := filepath.Join(parent, "config")
	configPath := filepath.Join(configDirectory, "config.toml")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	candidate := validStoreConfig(t)
	detached := filepath.Join(parent, "detached-config")
	writer := NewWriter()
	writer.ops.commitHook = func(point commitPoint) error {
		if point != commitAfterLock {
			return nil
		}
		if err := os.Rename(configDirectory, detached); err != nil {
			t.Fatalf("Rename config directory: %v", err)
		}
		if err := os.Mkdir(configDirectory, 0o700); err != nil {
			t.Fatalf("Mkdir replacement config directory: %v", err)
		}
		return nil
	}

	result, err := writer.Commit(context.Background(), Mutation{
		Base: base, Candidate: candidate,
	}, nil)
	if result != (CommitResult{}) || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Commit() = %#v, %v; want not committed unsafe path", result, err)
	}
	if _, err := os.Lstat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement directory received config: %v", err)
	}
}

func TestCommitExistingConfigPublishesImmediatePriorBackup(t *testing.T) {
	t.Parallel()

	root := privateStoreDir(t)
	configPath := filepath.Join(root, "config.toml")
	oldConfig := validStoreConfig(t)
	writePrivateStoreFile(t, configPath, oldConfig, 0o600)
	priorBackup := bytes.Replace(validStoreConfig(t), []byte("gpt-test"), []byte("gpt-prior"), 1)
	writePrivateStoreFile(t, BackupPath(configPath), priorBackup, 0o600)
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	candidate := bytes.Replace(oldConfig, []byte("gpt-test"), []byte("gpt-new"), 1)
	result, err := NewWriter().Commit(context.Background(), Mutation{
		Base: base, Candidate: candidate,
	}, nil)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if result != (CommitResult{
		State: CommitCommitted, ConfigChanged: true, BackupPath: BackupPath(configPath),
	}) {
		t.Fatalf("Commit() result = %#v", result)
	}
	assertPrivateFileBytes(t, configPath, candidate)
	assertPrivateFileBytes(t, BackupPath(configPath), oldConfig)
	assertPersistentLockAndNoTransactionTemps(t, configPath, "")
}

func TestCommitSemanticNoopDoesNotTouchConfigOrBackup(t *testing.T) {
	t.Parallel()

	root := privateStoreDir(t)
	configPath := filepath.Join(root, "config.toml")
	source := validStoreConfig(t)
	writePrivateStoreFile(t, configPath, source, 0o600)
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	before, err := os.Lstat(configPath)
	if err != nil {
		t.Fatalf("Lstat config: %v", err)
	}
	result, err := NewWriter().Commit(context.Background(), Mutation{
		Base: base, Candidate: source,
	}, nil)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if result != (CommitResult{State: CommitCommitted}) {
		t.Fatalf("Commit() result = %#v", result)
	}
	after, err := os.Lstat(configPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("no-op replaced config: before=%v after=%v err=%v", before, after, err)
	}
	if _, err := os.Lstat(BackupPath(configPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op created backup: %v", err)
	}
}

func TestCommitCancellationAfterBackupRestoresPriorStateAndKey(t *testing.T) {
	t.Parallel()

	fixture := existingKeyCommitFixture(t)
	priorBackup := bytes.Replace(validStoreConfig(t), []byte("gpt-test"), []byte("gpt-prior"), 1)
	writePrivateStoreFile(t, BackupPath(fixture.configPath), priorBackup, 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	writer := NewWriter()
	var reached []commitPoint
	writer.ops.commitHook = func(point commitPoint) error {
		reached = append(reached, point)
		if point == commitAfterBackupPublish {
			cancel()
		}
		return nil
	}
	result, err := writer.Commit(ctx, fixture.mutation, fixture.payload)
	if result != (CommitResult{}) || !errors.Is(err, context.Canceled) {
		entries, _ := os.ReadDir(filepath.Dir(fixture.configPath))
		backupBytes, _ := os.ReadFile(BackupPath(fixture.configPath)) // #nosec G304 -- exact test-owned path.
		t.Fatalf("Commit() = %#v, %v; want not committed, canceled; points=%v entries=%v backup_restored=%t", result, err, reached, entries, bytes.Equal(backupBytes, priorBackup))
	}
	assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
	assertPrivateFileBytes(t, BackupPath(fixture.configPath), priorBackup)
	if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back key remains: %v", err)
	}
	assertPersistentLockAndNoTransactionTemps(t, fixture.configPath, fixture.keyPath)
}

func TestCommitFinalDirectorySyncFailureReturnsTruthfulRollbackStates(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		failRollback bool
		wantState    CommitState
		keyStays     bool
	}{
		{"rollback sync succeeds", false, CommitRolledBack, false},
		{"rollback sync fails", true, CommitIndeterminate, true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := existingKeyCommitFixture(t)
			writer := NewWriter()
			realSync := writer.ops.syncDirectory
			calls := 0
			var reached []commitPoint
			writer.ops.commitHook = func(point commitPoint) error {
				reached = append(reached, point)
				return nil
			}
			writer.ops.syncDirectory = func(directory *os.File) error {
				calls++
				if calls == 2 || (calls == 3 && test.failRollback) {
					return errors.New("secret-sync-failure")
				}
				return realSync(directory)
			}
			result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
			if result.State != test.wantState || err == nil {
				t.Fatalf("Commit() = %#v, %v; want state %d and error; sync calls=%d points=%v", result, err, test.wantState, calls, reached)
			}
			if strings.Contains(err.Error(), "secret-sync-failure") ||
				strings.Contains(err.Error(), fixture.configPath) {
				t.Fatalf("Commit() leaked detail: %q", err)
			}
			assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
			_, keyErr := os.Lstat(fixture.keyPath)
			if test.keyStays && keyErr != nil {
				t.Fatalf("indeterminate transaction removed key: %v", keyErr)
			}
			if !test.keyStays && !errors.Is(keyErr, os.ErrNotExist) {
				t.Fatalf("rolled-back transaction retained key: %v", keyErr)
			}
		})
	}
}

func TestCommitCleanupFailurePreservesCommittedState(t *testing.T) {
	t.Parallel()

	fixture := existingKeyCommitFixture(t)
	writer := NewWriter()
	var reached []commitPoint
	writer.ops.commitHook = func(point commitPoint) error {
		reached = append(reached, point)
		if point == commitBeforeCleanup {
			return errors.New("secret-cleanup-failure")
		}
		return nil
	}
	result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
	if result.State != CommitCommitted || !result.ConfigChanged ||
		!result.KeyCreated || err == nil {
		t.Fatalf("Commit() = %#v, %v; want committed plus error; points=%v", result, err, reached)
	}
	assertPrivateFileBytes(t, fixture.configPath, fixture.candidate)
	assertPrivateFileBytes(t, fixture.keyPath, fixture.payload)
}

type existingCommitFixture struct {
	mutation   Mutation
	configPath string
	keyPath    string
	oldConfig  []byte
	candidate  []byte
	payload    []byte
}

func existingKeyCommitFixture(t *testing.T) existingCommitFixture {
	t.Helper()
	root := privateStoreDir(t)
	configPath := filepath.Join(root, "config.toml")
	keyPath := filepath.Join(root, "gateway.key")
	oldConfig := validStoreConfig(t)
	writePrivateStoreFile(t, configPath, oldConfig, 0o600)
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	candidate := storeCandidate(t, keyPath, filepath.Join(root, "provider-home"))
	return existingCommitFixture{
		mutation: Mutation{
			Base:      base,
			Candidate: candidate,
			Key: KeyPlan{
				Intent:       KeyIntentEnsure,
				Path:         keyPath,
				DistinctFrom: []string{configPath},
			},
		},
		configPath: configPath,
		keyPath:    keyPath,
		oldConfig:  oldConfig,
		candidate:  candidate,
		payload:    gatewayKeyPayload('b'),
	}
}

func assertPrivateFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("private file %q info=%v err=%v", filepath.Base(path), info, err)
	}
	got, err := os.ReadFile(path) // #nosec G304 -- exact test-owned path.
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("private file %q bytes=%q err=%v", filepath.Base(path), got, err)
	}
}

func assertPersistentLockAndNoTransactionTemps(t *testing.T, configPath string, keyPath string) {
	t.Helper()
	if info, err := os.Lstat(LockPath(configPath)); err != nil ||
		!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("persistent lock info=%v err=%v", info, err)
	}
	for _, path := range []string{
		configPath + ".tmp",
		BackupPath(configPath) + ".tmp",
		configPath + ".rollback.tmp",
		BackupPath(configPath) + ".restore.tmp",
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transaction temp remains at %q: %v", filepath.Base(path), err)
		}
	}
	if keyPath != "" {
		keyTemp := filepath.Join(filepath.Dir(keyPath), "."+filepath.Base(keyPath)+".ai-cli-gateway.tmp")
		if _, err := os.Lstat(keyTemp); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("key temp remains: %v", err)
		}
	}
}
