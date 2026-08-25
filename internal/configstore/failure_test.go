//go:build !windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCommitFailureInjectionCleansPrepublicationFilesAndKey(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		operation operationKind
		at        int
	}{
		{"candidate create", operationCreate, 2},
		{"candidate write", operationWrite, 2},
		{"candidate sync", operationSyncFile, 2},
		{"candidate stat", operationStat, 3},
		{"backup create", operationCreate, 3},
		{"backup write", operationWrite, 3},
		{"backup sync", operationSyncFile, 3},
		{"backup stat", operationStat, 5},
		{"backup replace", operationBackupReplace, 1},
		{"config replace", operationConfigReplace, 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := existingKeyCommitFixture(t)
			writer := NewWriter()
			calls := 0
			writer.ops.operationHook = func(operation operationKind) error {
				if operation == test.operation {
					calls++
					if calls == test.at {
						return errors.New("secret-injected-failure")
					}
				}
				return nil
			}
			result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
			if result != (CommitResult{}) || !errors.Is(err, ErrStore) {
				t.Fatalf("Commit() = %#v, %v; want not committed, ErrStore", result, err)
			}
			if strings.Contains(err.Error(), "secret-injected-failure") ||
				strings.Contains(err.Error(), fixture.configPath) {
				t.Fatalf("Commit() leaked failure detail: %q", err)
			}
			assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
			if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed transaction retained key: %v", err)
			}
			if _, err := os.Lstat(BackupPath(fixture.configPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed transaction retained backup: %v", err)
			}
			assertPersistentLockAndNoTransactionTemps(t, fixture.configPath, fixture.keyPath)
		})
	}
}

func TestCommitMkdirFailureLeavesNoAuthorityRepairOrConfig(t *testing.T) {
	t.Parallel()

	root := privateStoreDir(t)
	configPath := filepath.Join(root, "config.toml")
	providerHome := filepath.Join(root, "provider-home")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	writer := NewWriter()
	writer.ops.operationHook = func(operation operationKind) error {
		if operation == operationMkdir {
			return errors.New("mkdir")
		}
		return nil
	}
	result, err := writer.Commit(context.Background(), Mutation{
		Base:        base,
		Candidate:   validStoreConfig(t),
		PrivateDirs: []string{providerHome},
	}, nil)
	if result != (CommitResult{}) || !errors.Is(err, ErrStore) {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	for _, path := range []string{configPath, providerHome} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed mkdir transaction created %q: %v", path, err)
		}
	}
}

func TestCommitOpenAndLockFailuresAreClosedAndDoNotChangeConfig(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		operation operationKind
		want      error
	}{
		{"lock", operationLock, ErrStore},
		{"open after lock", operationOpen, ErrUnsafePath},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := existingKeyCommitFixture(t)
			writer := NewWriter()
			writer.ops.operationHook = func(operation operationKind) error {
				if operation == test.operation {
					return errors.New("secret-open-lock")
				}
				return nil
			}
			result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
			if result != (CommitResult{}) || !errors.Is(err, test.want) ||
				strings.Contains(err.Error(), "secret-open-lock") {
				t.Fatalf("Commit() = %#v, %v; want zero and %v", result, err, test.want)
			}
			assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
			if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed transaction created key: %v", err)
			}
		})
	}
}

func TestCommitBackupSyncFailureRestoresAbsence(t *testing.T) {
	t.Parallel()

	fixture := existingKeyCommitFixture(t)
	writer := NewWriter()
	realSync := writer.ops.syncDirectory
	calls := 0
	writer.ops.syncDirectory = func(directory *os.File) error {
		calls++
		if calls == 1 {
			return errors.New("secret-backup-sync")
		}
		return realSync(directory)
	}
	result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
	if result != (CommitResult{}) || !errors.Is(err, ErrStore) {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
	if _, err := os.Lstat(BackupPath(fixture.configPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup was not restored to absence: %v", err)
	}
	if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key remains after backup sync failure: %v", err)
	}
}

func TestCommitBackupReplacementRequiresRecoveryAndPreservesReplacement(t *testing.T) {
	t.Parallel()

	fixture := existingKeyCommitFixture(t)
	replacement := bytes.Replace(validStoreConfig(t), []byte("gpt-test"), []byte("gpt-third-party"), 1)
	writer := NewWriter()
	writer.ops.commitHook = func(point commitPoint) error {
		if point != commitAfterBackupPublish {
			return nil
		}
		moved := BackupPath(fixture.configPath) + ".moved"
		if err := os.Rename(BackupPath(fixture.configPath), moved); err != nil {
			t.Fatalf("move published backup: %v", err)
		}
		writePrivateStoreFile(t, BackupPath(fixture.configPath), replacement, 0o600)
		return errors.New("stop-before-config")
	}
	result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
	if result.State != CommitRecoveryRequired || err == nil {
		t.Fatalf("Commit() = %#v, %v; want recovery required", result, err)
	}
	assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
	assertPrivateFileBytes(t, BackupPath(fixture.configPath), replacement)
	if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery-required transaction retained key: %v", err)
	}
}

func TestCommitBackupRestorationFailureIsRecoveryRequired(t *testing.T) {
	t.Parallel()

	fixture := existingKeyCommitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	writer := NewWriter()
	writer.ops.commitHook = func(point commitPoint) error {
		if point == commitAfterBackupPublish {
			cancel()
		}
		return nil
	}
	writer.ops.operationHook = func(operation operationKind) error {
		if operation == operationBackupRestore {
			return errors.New("secret-restore-failure")
		}
		return nil
	}
	result, err := writer.Commit(ctx, fixture.mutation, fixture.payload)
	if result.State != CommitRecoveryRequired || !errors.Is(err, context.Canceled) ||
		!errors.Is(err, ErrStore) {
		t.Fatalf("Commit() = %#v, %v; want recovery required with canceled/store", result, err)
	}
	assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
	assertPrivateFileBytes(t, BackupPath(fixture.configPath), fixture.oldConfig)
}

func TestCommitSourceChangesAreDetectedWithoutOverwritingWriter(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		replace bool
	}{
		{"content changes at stable name", false},
		{"identity is replaced", true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := existingKeyCommitFixture(t)
			external := bytes.Replace(
				fixture.oldConfig, []byte("gpt-test"), []byte("gpt-external"), 1,
			)
			writer := NewWriter()
			writer.ops.commitHook = func(point commitPoint) error {
				if point != commitBeforeFinalSourceCheck {
					return nil
				}
				if test.replace {
					temporary := fixture.configPath + ".external"
					writePrivateStoreFile(t, temporary, external, 0o600)
					if err := os.Rename(temporary, fixture.configPath); err != nil {
						t.Fatalf("replace source: %v", err)
					}
				} else {
					writePrivateStoreFile(t, fixture.configPath, external, 0o600)
				}
				return nil
			}
			result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
			if result != (CommitResult{}) || !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Commit() = %#v, %v; want not committed, unsafe", result, err)
			}
			assertPrivateFileBytes(t, fixture.configPath, external)
			if _, err := os.Lstat(BackupPath(fixture.configPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source race retained backup: %v", err)
			}
			if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source race retained key: %v", err)
			}
		})
	}
}

func TestCommitKeyTargetRaceDoesNotReuseUnconfirmedOrphan(t *testing.T) {
	t.Parallel()

	fixture := existingKeyCommitFixture(t)
	orphan := gatewayKeyPayload('c')
	writer := NewWriter()
	writer.ops.commitHook = func(point commitPoint) error {
		if point == commitAfterPrivateDirectories {
			writePrivateStoreFile(t, fixture.keyPath, orphan, 0o600)
		}
		return nil
	}
	result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
	if result != (CommitResult{}) || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	assertPrivateFileBytes(t, fixture.keyPath, orphan)
	assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
}

func TestCommitCancellationImmediatelyBeforeAndAfterConfigPublication(t *testing.T) {
	t.Parallel()

	t.Run("before", func(t *testing.T) {
		t.Parallel()
		fixture := existingKeyCommitFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		writer := NewWriter()
		writer.ops.commitHook = func(point commitPoint) error {
			if point == commitBeforeConfigPublish {
				cancel()
			}
			return nil
		}
		result, err := writer.Commit(ctx, fixture.mutation, fixture.payload)
		if result != (CommitResult{}) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Commit() = %#v, %v", result, err)
		}
		assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
		if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pre-publication cancellation retained key: %v", err)
		}
	})

	t.Run("during final publication operation", func(t *testing.T) {
		t.Parallel()
		fixture := existingKeyCommitFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		writer := NewWriter()
		writer.ops.operationHook = func(operation operationKind) error {
			if operation == operationConfigReplace {
				cancel()
			}
			return nil
		}
		result, err := writer.Commit(ctx, fixture.mutation, fixture.payload)
		if result != (CommitResult{}) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Commit() = %#v, %v", result, err)
		}
		assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
		if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pre-publication cancellation retained key: %v", err)
		}
	})

	t.Run("after final validation before native rename", func(t *testing.T) {
		t.Parallel()
		fixture := existingKeyCommitFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		writer := NewWriter()
		writer.ops.commitHook = func(point commitPoint) error {
			if point == commitBeforeNativeConfigPublish {
				cancel()
			}
			return nil
		}
		result, err := writer.Commit(ctx, fixture.mutation, fixture.payload)
		if result != (CommitResult{}) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Commit() = %#v, %v", result, err)
		}
		assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
		if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pre-rename cancellation retained key: %v", err)
		}
	})

	t.Run("after", func(t *testing.T) {
		t.Parallel()
		fixture := existingKeyCommitFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		writer := NewWriter()
		writer.ops.commitHook = func(point commitPoint) error {
			if point == commitAfterConfigPublish {
				cancel()
			}
			return nil
		}
		result, err := writer.Commit(ctx, fixture.mutation, fixture.payload)
		if result.State != CommitCommitted || !result.ConfigChanged ||
			!result.KeyCreated || !errors.Is(err, context.Canceled) {
			t.Fatalf("Commit() = %#v, %v", result, err)
		}
		assertPrivateFileBytes(t, fixture.configPath, fixture.candidate)
		assertPrivateFileBytes(t, fixture.keyPath, fixture.payload)
		assertPrivateFileBytes(t, BackupPath(fixture.configPath), fixture.oldConfig)
	})
}

func TestCommitCancellationWhileWaitingForLockDoesNotMutate(t *testing.T) {
	t.Parallel()

	fixture := existingKeyCommitFixture(t)
	held, err := NewWriter().acquireLock(context.Background(), fixture.mutation.Base)
	if err != nil {
		t.Fatalf("acquire held lock: %v", err)
	}
	defer func() { _ = held.release() }()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	result, err := NewWriter().Commit(ctx, fixture.mutation, fixture.payload)
	if result != (CommitResult{}) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	assertPrivateFileBytes(t, fixture.configPath, fixture.oldConfig)
	if _, err := os.Lstat(fixture.keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("waiting transaction created key: %v", err)
	}
}

func TestConcurrentCommitWritersSerializeAndOnlyOneBaseWins(t *testing.T) {
	t.Parallel()

	root := privateStoreDir(t)
	configPath := filepath.Join(root, "config.toml")
	source := validStoreConfig(t)
	writePrivateStoreFile(t, configPath, source, 0o600)
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	candidates := [][]byte{
		bytes.Replace(source, []byte("gpt-test"), []byte("gpt-one"), 1),
		bytes.Replace(source, []byte("gpt-test"), []byte("gpt-two"), 1),
	}
	start := make(chan struct{})
	results := make([]CommitResult, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range candidates {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results[index], errs[index] = NewWriter().Commit(context.Background(), Mutation{
				Base: base, Candidate: candidates[index],
			}, nil)
		}()
	}
	close(start)
	wait.Wait()
	committed := -1
	for index := range results {
		if results[index].State == CommitCommitted && errs[index] == nil {
			if committed != -1 {
				t.Fatalf("both writers committed: results=%#v errors=%v", results, errs)
			}
			committed = index
			continue
		}
		if results[index] != (CommitResult{}) || !errors.Is(errs[index], ErrUnsafePath) {
			t.Fatalf("losing writer %d = %#v, %v", index, results[index], errs[index])
		}
	}
	if committed == -1 {
		t.Fatalf("no writer committed: results=%#v errors=%v", results, errs)
	}
	assertPrivateFileBytes(t, configPath, candidates[committed])
	assertPrivateFileBytes(t, BackupPath(configPath), source)
	assertPersistentLockAndNoTransactionTemps(t, configPath, "")
}

func TestCommitRollbackFailureIsIndeterminateAndDoesNotRemoveKey(t *testing.T) {
	t.Parallel()

	fixture := existingKeyCommitFixture(t)
	writer := NewWriter()
	realSync := writer.ops.syncDirectory
	syncCalls := 0
	writer.ops.syncDirectory = func(directory *os.File) error {
		syncCalls++
		if syncCalls == 2 {
			return errors.New("final-sync")
		}
		return realSync(directory)
	}
	writer.ops.operationHook = func(operation operationKind) error {
		if operation == operationRollback {
			return errors.New("rollback")
		}
		return nil
	}
	result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
	if result.State != CommitIndeterminate || !result.KeyCreated || err == nil {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	assertPrivateFileBytes(t, fixture.configPath, fixture.candidate)
	assertPrivateFileBytes(t, fixture.keyPath, fixture.payload)
}

func TestCommitPostpublicationCleanupUnlockAndCloseFailuresStayCommitted(t *testing.T) {
	t.Parallel()

	for _, operation := range []operationKind{operationCleanup, operationUnlock, operationClose} {
		operation := operation
		t.Run(fmt.Sprintf("operation_%d", operation), func(t *testing.T) {
			t.Parallel()
			fixture := existingKeyCommitFixture(t)
			writer := NewWriter()
			writer.ops.operationHook = func(got operationKind) error {
				if got == operation {
					return errors.New("postpublication")
				}
				return nil
			}
			result, err := writer.Commit(context.Background(), fixture.mutation, fixture.payload)
			if result.State != CommitCommitted || !result.ConfigChanged ||
				!result.KeyCreated || !errors.Is(err, ErrStore) {
				t.Fatalf("Commit() = %#v, %v", result, err)
			}
			assertPrivateFileBytes(t, fixture.configPath, fixture.candidate)
		})
	}
}
