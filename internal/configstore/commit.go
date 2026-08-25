package configstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
)

// CommitState describes the proved authoritative outcome of Commit.
type CommitState uint8

const (
	// CommitNotCommitted means the authoritative config was not replaced and
	// any published backup change was proved restored.
	CommitNotCommitted CommitState = iota
	// CommitRolledBack means a published candidate was replaced by the prior config.
	CommitRolledBack
	// CommitCommitted means the configured platform publication boundary succeeded.
	CommitCommitted
	// CommitRecoveryRequired means the config was not replaced but backup restoration is unproved.
	CommitRecoveryRequired
	// CommitIndeterminate means candidate publication occurred and the final durable state is unknown.
	CommitIndeterminate
)

// CommitResult reports only state proved by the transaction.
type CommitResult struct {
	State         CommitState
	ConfigChanged bool
	KeyCreated    bool
	BackupPath    string
}

type commitPoint uint8

const (
	commitAfterLock commitPoint = iota + 1
	commitAfterPrivateDirectories
	commitAfterKeyPublish
	commitAfterCandidateStage
	commitAfterBackupStage
	commitAfterBackupPublish
	commitBeforeFinalSourceCheck
	commitBeforeConfigPublish
	commitBeforeNativeConfigPublish
	commitAfterConfigPublish
	commitBeforeBackupRestore
	commitBeforeConfigRollback
	commitBeforeCleanup
	commitBeforeUnlock
)

type opaqueRevision struct {
	path    string
	exists  bool
	bytes   []byte
	digest  [sha256.Size]byte
	file    nativeFileMetadata
	parent  nativeDirectoryEvidence
	missing []string
}

type transactionDirectory struct {
	path     string
	root     *os.Root
	file     *os.File
	identity fs.FileInfo
	files    []*ownedStoreFile
	closed   bool
}

type ownedStoreFile struct {
	directory *transactionDirectory
	file      *os.File
	identity  fs.FileInfo
	digest    [sha256.Size]byte
	size      int64
	name      string
	path      string
	linked    bool
	temporary bool
	closed    bool
}

// Commit publishes a validated candidate and any new Gateway key transactionally.
func (writer *Writer) Commit(
	ctx context.Context,
	mutation Mutation,
	keyPayload []byte,
) (result CommitResult, resultErr error) {
	if writer == nil || ctx == nil || writer.ops.syncFile == nil ||
		writer.ops.syncDirectory == nil || !validSnapshotShape(mutation.Base) ||
		len(mutation.Candidate) == 0 || len(mutation.Candidate) > maxConfigBytes {
		return CommitResult{}, ErrStore
	}
	if err := ctx.Err(); err != nil {
		return CommitResult{}, fixedContextError(err)
	}
	candidate, err := config.Decode(bytes.NewReader(mutation.Candidate))
	if err != nil {
		return CommitResult{}, ErrInvalidConfig
	}
	if _, err := validateKeyPlan(candidate.Server.APIKeyFile, mutation); err != nil {
		return CommitResult{}, err
	}

	lock, err := writer.acquireLock(ctx, mutation.Base)
	if err != nil {
		return CommitResult{}, err
	}
	resources := lock.directory
	var (
		key        *stagedKey
		keyCreated bool
	)
	defer func() {
		if hookErr := writer.runCommitHook(commitBeforeCleanup); hookErr != nil {
			resultErr = errors.Join(resultErr, hookErr)
		}
		if hookErr := writer.runOperationHook(operationCleanup); hookErr != nil {
			resultErr = errors.Join(resultErr, hookErr)
		}
		if resources != nil {
			if cleanupErr := resources.cleanup(); cleanupErr != nil {
				resultErr = errors.Join(resultErr, cleanupErr)
			}
		}
		keepKey := result.State == CommitCommitted || result.State == CommitIndeterminate
		if key != nil {
			var keyErr error
			if keepKey {
				keyErr = key.finish()
			} else {
				keyErr = key.rollback()
			}
			if keyErr != nil {
				resultErr = errors.Join(resultErr, keyErr)
			}
		}
		result.KeyCreated = keyCreated && keepKey
		if hookErr := writer.runOperationHook(operationClose); hookErr != nil {
			resultErr = errors.Join(resultErr, hookErr)
		}
		if hookErr := writer.runCommitHook(commitBeforeUnlock); hookErr != nil {
			resultErr = errors.Join(resultErr, hookErr)
		}
		if hookErr := writer.runOperationHook(operationUnlock); hookErr != nil {
			resultErr = errors.Join(resultErr, hookErr)
		}
		if unlockErr := lock.release(); unlockErr != nil {
			resultErr = errors.Join(resultErr, unlockErr)
		}
		resultErr = fixedCommitError(resultErr)
	}()

	if err := writer.runCommitHook(commitAfterLock); err != nil {
		return CommitResult{}, err
	}
	if err := writer.validateBaseRevision(ctx, mutation.Base, resources); err != nil {
		return CommitResult{}, err
	}
	if err := writer.createPrivateDirectories(ctx, mutation.PrivateDirs); err != nil {
		return CommitResult{}, err
	}
	if err := writer.runCommitHook(commitAfterPrivateDirectories); err != nil {
		return CommitResult{}, err
	}
	if err := writer.validateBaseRevision(ctx, mutation.Base, resources); err != nil {
		return CommitResult{}, err
	}
	if err := inspectReservedArtifacts(mutation.Base.path); err != nil {
		return CommitResult{}, err
	}
	if err := inspectPrivateDirectories(mutation); err != nil {
		return CommitResult{}, err
	}
	preflight, err := preflightKey(candidate.Server.APIKeyFile, mutation)
	if err != nil {
		return CommitResult{}, err
	}
	if _, err := validateKeyCommitMatrix(mutation.Key, preflight.KeyState, keyPayload); err != nil {
		return CommitResult{}, err
	}
	key, err = writer.stageKey(ctx, mutation, preflight, keyPayload)
	if err != nil {
		key = nil
		return CommitResult{}, err
	}
	if key != nil {
		if err := key.publish(ctx); err != nil {
			key = nil
			return CommitResult{}, err
		}
		keyCreated = true
	}
	if err := writer.runCommitHook(commitAfterKeyPublish); err != nil {
		return CommitResult{}, err
	}

	configChanged := !mutation.Base.exists || !bytes.Equal(mutation.Base.bytes, mutation.Candidate)
	if !configChanged {
		return CommitResult{State: CommitCommitted}, nil
	}
	if err := writer.runOperationHook(operationOpen); err != nil {
		return CommitResult{}, err
	}
	candidateFile, err := resources.stage(
		writer, filepath.Base(mutation.Base.path)+".tmp", mutation.Candidate,
	)
	if err != nil {
		return CommitResult{}, err
	}
	if err := writer.runCommitHook(commitAfterCandidateStage); err != nil {
		return CommitResult{}, err
	}

	var (
		backupRevision  opaqueRevision
		backupFile      *ownedStoreFile
		rollbackFile    *ownedStoreFile
		restoreFile     *ownedStoreFile
		backupPublished bool
	)
	if mutation.Base.exists {
		backupRevision, err = loadOpaqueRevision(BackupPath(mutation.Base.path))
		if err != nil {
			return CommitResult{}, err
		}
		defer clear(backupRevision.bytes)
		backupFile, err = resources.stage(
			writer, filepath.Base(BackupPath(mutation.Base.path))+".tmp", mutation.Base.bytes,
		)
		if err != nil {
			return CommitResult{}, err
		}
		rollbackFile, err = resources.stage(
			writer, filepath.Base(mutation.Base.path)+".rollback.tmp", mutation.Base.bytes,
		)
		if err != nil {
			return CommitResult{}, err
		}
		if backupRevision.exists {
			restoreFile, err = resources.stage(
				writer,
				filepath.Base(BackupPath(mutation.Base.path))+".restore.tmp",
				backupRevision.bytes,
			)
			if err != nil {
				return CommitResult{}, err
			}
		}
	}
	if err := writer.runCommitHook(commitAfterBackupStage); err != nil {
		return CommitResult{}, err
	}
	if err := writer.validateBaseRevision(ctx, mutation.Base, resources); err != nil {
		return CommitResult{}, err
	}

	if mutation.Base.exists {
		if err := validateOpaqueRevision(backupRevision); err != nil {
			return CommitResult{}, err
		}
		backupTargetName := filepath.Base(BackupPath(mutation.Base.path))
		if err := resources.publish(
			ctx,
			backupFile,
			backupTargetName,
			!backupRevision.exists,
			func() error {
				return writer.runOperationHook(operationBackupReplace)
			},
		); err != nil {
			if backupFile.name == backupTargetName {
				backupFile.temporary = false
				return writer.failBeforeConfig(
					resources, keyCreated, backupRevision, backupFile, restoreFile, err,
				)
			}
			return CommitResult{}, err
		}
		backupFile.temporary = false
		backupPublished = true
		if err := writer.runCommitHook(commitAfterBackupPublish); err != nil {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile, err,
			)
		}
		if err := writer.syncConfigDirectory(resources.file); err != nil {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile, err,
			)
		}
	}

	if err := ctx.Err(); err != nil {
		if backupPublished {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile,
				fixedContextError(err),
			)
		}
		return CommitResult{}, fixedContextError(err)
	}
	if err := writer.runCommitHook(commitBeforeFinalSourceCheck); err != nil {
		if backupPublished {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile, err,
			)
		}
		return CommitResult{}, err
	}
	if err := writer.validateBaseRevision(ctx, mutation.Base, resources); err != nil {
		if backupPublished {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile, err,
			)
		}
		return CommitResult{}, err
	}
	if err := writer.runCommitHook(commitBeforeConfigPublish); err != nil {
		if backupPublished {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile, err,
			)
		}
		return CommitResult{}, err
	}
	if err := ctx.Err(); err != nil {
		if backupPublished {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile,
				fixedContextError(err),
			)
		}
		return CommitResult{}, fixedContextError(err)
	}
	if err := writer.validateBaseRevision(ctx, mutation.Base, resources); err != nil {
		if backupPublished {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile, err,
			)
		}
		return CommitResult{}, err
	}
	configTargetName := filepath.Base(mutation.Base.path)
	if err := resources.publish(
		ctx, candidateFile, configTargetName, !mutation.Base.exists,
		func() error {
			if err := writer.runCommitHook(commitBeforeNativeConfigPublish); err != nil {
				return err
			}
			return writer.runOperationHook(operationConfigReplace)
		},
	); err != nil {
		if candidateFile.name == configTargetName {
			candidateFile.temporary = false
			return CommitResult{State: CommitIndeterminate}, err
		}
		if backupPublished {
			return writer.failBeforeConfig(
				resources, keyCreated, backupRevision, backupFile, restoreFile, err,
			)
		}
		return CommitResult{}, err
	}
	candidateFile.temporary = false
	postPublishErr := writer.runCommitHook(commitAfterConfigPublish)

	if err := writer.syncConfigDirectory(resources.file); err != nil {
		rollbackResult, rollbackErr := writer.rollbackPublishedConfig(
			resources, mutation.Base, candidateFile, rollbackFile,
		)
		return rollbackResult, errors.Join(err, postPublishErr, rollbackErr)
	}
	result = CommitResult{
		State:         CommitCommitted,
		ConfigChanged: true,
	}
	if mutation.Base.exists {
		result.BackupPath = BackupPath(mutation.Base.path)
	}
	if err := ctx.Err(); err != nil {
		return result, errors.Join(postPublishErr, fixedContextError(err))
	}
	return result, postPublishErr
}

func (writer *Writer) runCommitHook(point commitPoint) error {
	if writer == nil || writer.ops.commitHook == nil {
		return nil
	}
	if writer.ops.commitHook(point) != nil {
		return ErrStore
	}
	return nil
}

func (writer *Writer) syncConfigDirectory(directory *os.File) error {
	if !supportsConfigDirectorySync {
		return nil
	}
	if err := writer.runOperationHook(operationDirectorySync); err != nil {
		return err
	}
	if writer == nil || writer.ops.syncDirectory == nil ||
		writer.ops.syncDirectory(directory) != nil {
		return ErrStore
	}
	return nil
}

func (writer *Writer) validateBaseRevision(
	ctx context.Context,
	base Snapshot,
	directory *transactionDirectory,
) error {
	if writer == nil || ctx == nil || !validSnapshotShape(base) || directory == nil {
		return ErrStore
	}
	if err := ctx.Err(); err != nil {
		return fixedContextError(err)
	}
	if !sameNativeStorePath(directory.path, filepath.Dir(base.path)) || !directory.matches() {
		return ErrUnsafePath
	}
	if !base.exists {
		if _, err := directory.root.Lstat(filepath.Base(base.path)); !errors.Is(err, fs.ErrNotExist) {
			return ErrUnsafePath
		}
		if _, err := os.Lstat(base.path); !errors.Is(err, fs.ErrNotExist) {
			return ErrUnsafePath
		}
		if err := ctx.Err(); err != nil {
			return fixedContextError(err)
		}
		return nil
	}
	current, err := writer.Load(ctx, base.path)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrUnsafePath
	}
	if current.path != base.path || !current.exists || current.file != base.file ||
		current.digest != base.digest || !bytes.Equal(current.bytes, base.bytes) ||
		!sameNativeDirectoryIdentity(base.parent.metadata, current.parent.metadata) {
		return ErrUnsafePath
	}
	return nil
}

func openTransactionDirectory(path string) (*transactionDirectory, error) {
	root, directory, identity, err := openKeyDirectory(path)
	if err != nil {
		return nil, err
	}
	return &transactionDirectory{
		path: path, root: root, file: directory, identity: identity,
	}, nil
}

func (directory *transactionDirectory) matches() bool {
	if directory == nil || directory.closed || directory.root == nil ||
		directory.file == nil || directory.identity == nil {
		return false
	}
	handleInfo, handleErr := directory.file.Stat()
	pathInfo, pathErr := os.Lstat(directory.path)
	exists, inspectErr := inspectNativePrivateDirectory(directory.path)
	return handleErr == nil && pathErr == nil && inspectErr == nil && exists &&
		handleInfo != nil && pathInfo != nil && handleInfo.IsDir() &&
		pathInfo.Mode()&fs.ModeSymlink == 0 && pathInfo.IsDir() &&
		os.SameFile(directory.identity, handleInfo) && os.SameFile(handleInfo, pathInfo)
}

func (directory *transactionDirectory) stage(
	writer *Writer,
	name string,
	payload []byte,
) (result *ownedStoreFile, resultErr error) {
	if directory == nil || writer == nil || writer.ops.syncFile == nil ||
		!directory.matches() || name == "" || filepath.Base(name) != name {
		return nil, ErrStore
	}
	path := filepath.Join(directory.path, name)
	if _, ok := nativeStorePathKey(path); !ok {
		return nil, ErrUnsafePath
	}
	if err := writer.runOperationHook(operationCreate); err != nil {
		return nil, err
	}
	file, err := createNativePrivateFile(directory.file, directory.root, name)
	if err != nil {
		return nil, err
	}
	owned := &ownedStoreFile{
		directory: directory,
		file:      file,
		name:      name,
		path:      path,
		linked:    true,
		temporary: true,
		digest:    sha256.Sum256(payload),
		size:      int64(len(payload)),
	}
	directory.files = append(directory.files, owned)
	identity, statErr := file.Stat()
	if statErr != nil || identity == nil || !identity.Mode().IsRegular() || identity.Size() != 0 {
		return nil, ErrStore
	}
	owned.identity = identity
	if err := writer.runOperationHook(operationStat); err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, owned.cleanup())
		}
	}()
	if err := writer.runOperationHook(operationWrite); err != nil {
		return nil, err
	}
	if err := writeComplete(file, payload); err != nil {
		return nil, ErrStore
	}
	if err := writer.runOperationHook(operationSyncFile); err != nil {
		return nil, err
	}
	if err := writer.ops.syncFile(file); err != nil {
		return nil, ErrStore
	}
	if err := writer.runOperationHook(operationStat); err != nil {
		return nil, err
	}
	afterWrite, err := file.Stat()
	if err != nil || afterWrite == nil || !afterWrite.Mode().IsRegular() ||
		afterWrite.Size() != int64(len(payload)) || !os.SameFile(owned.identity, afterWrite) {
		return nil, ErrStore
	}
	if err := owned.validate(); err != nil {
		return nil, err
	}
	return owned, nil
}

func (directory *transactionDirectory) publish(
	ctx context.Context,
	owned *ownedStoreFile,
	targetName string,
	noReplace bool,
	beforeRename func() error,
) error {
	if ctx == nil || directory == nil || owned == nil || owned.directory != directory ||
		owned.closed || !owned.linked || !owned.temporary ||
		targetName == "" || filepath.Base(targetName) != targetName || !directory.matches() {
		return ErrStore
	}
	if err := owned.validate(); err != nil {
		return err
	}
	if beforeRename != nil {
		if err := beforeRename(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return fixedContextError(err)
	}
	var err error
	if noReplace {
		err = nativeRenameNoReplace(
			directory.file, directory.root, owned.file, owned.name, targetName,
		)
	} else {
		err = nativeReplaceStoreFile(
			directory.file, directory.root, owned.file, owned.name, targetName,
		)
	}
	if err != nil {
		return err
	}
	owned.name = targetName
	owned.path = filepath.Join(directory.path, targetName)
	if err := owned.validate(); err != nil {
		return err
	}
	return nil
}

func (owned *ownedStoreFile) validate() error {
	if owned == nil || owned.closed || owned.directory == nil || owned.file == nil ||
		owned.identity == nil || !owned.linked || !owned.directory.matches() {
		return ErrUnsafePath
	}
	handleInfo, handleErr := owned.file.Stat()
	pathInfo, pathErr := owned.directory.root.Lstat(owned.name)
	if handleErr != nil || pathErr != nil || handleInfo == nil || pathInfo == nil ||
		!handleInfo.Mode().IsRegular() || pathInfo.Mode()&fs.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() || !os.SameFile(owned.identity, handleInfo) ||
		!os.SameFile(handleInfo, pathInfo) || handleInfo.Size() != owned.size {
		return ErrUnsafePath
	}
	target, err := openNativeLoadTarget(owned.path)
	if err != nil || !target.exists || target.file == nil {
		if target.file != nil {
			_ = target.file.Close()
		}
		return ErrUnsafePath
	}
	openedInfo, statErr := target.file.Stat()
	raw, readOK := readBoundedConfig(target.file)
	stable := statErr == nil && openedInfo != nil &&
		os.SameFile(owned.identity, openedInfo) && readOK &&
		int64(len(raw)) == owned.size && sha256.Sum256(raw) == owned.digest &&
		revalidateNativeLoadTarget(target)
	clear(raw)
	closeErr := target.file.Close()
	if !stable || closeErr != nil {
		return ErrUnsafePath
	}
	return nil
}

func (owned *ownedStoreFile) remove() error {
	if owned == nil || owned.closed || !owned.linked || owned.identity == nil ||
		owned.directory == nil || !owned.directory.matches() {
		return ErrUnsafePath
	}
	current, err := owned.directory.root.Lstat(owned.name)
	if errors.Is(err, fs.ErrNotExist) {
		owned.linked = false
		return nil
	}
	if err != nil || current == nil || current.Mode()&fs.ModeSymlink != 0 ||
		!current.Mode().IsRegular() || !os.SameFile(owned.identity, current) {
		return ErrUnsafePath
	}
	if err := owned.directory.root.Remove(owned.name); err != nil {
		return ErrStore
	}
	if _, err := owned.directory.root.Lstat(owned.name); !errors.Is(err, fs.ErrNotExist) {
		return ErrUnsafePath
	}
	owned.linked = false
	return nil
}

func (owned *ownedStoreFile) close() error {
	if owned == nil || owned.closed {
		return nil
	}
	owned.closed = true
	if err := closeStoreFile(owned.file); err != nil {
		owned.file = nil
		return ErrStore
	}
	owned.file = nil
	return nil
}

func (owned *ownedStoreFile) cleanup() error {
	if owned == nil || owned.closed {
		return nil
	}
	var removeErr error
	if owned.linked && owned.temporary {
		removeErr = owned.remove()
	}
	return errors.Join(removeErr, owned.close())
}

func (directory *transactionDirectory) cleanup() error {
	if directory == nil || directory.closed {
		return nil
	}
	var result error
	for index := len(directory.files) - 1; index >= 0; index-- {
		result = errors.Join(result, directory.files[index].cleanup())
	}
	directory.closed = true
	result = errors.Join(
		result,
		closeStoreFile(directory.file),
		closeStoreRoot(directory.root),
	)
	directory.file = nil
	directory.root = nil
	if result != nil {
		return ErrStore
	}
	return nil
}

func loadOpaqueRevision(path string) (opaqueRevision, error) {
	target, err := openNativeLoadTarget(path)
	if err != nil {
		return opaqueRevision{}, err
	}
	if !target.exists {
		if !revalidateNativeLoadTarget(target) {
			return opaqueRevision{}, ErrUnsafePath
		}
		return opaqueRevision{
			path: target.path, parent: target.parent,
			missing: append([]string(nil), target.missing...),
		}, nil
	}
	raw, ok := readBoundedConfig(target.file)
	if !ok || !revalidateNativeLoadTarget(target) {
		if target.file != nil {
			_ = target.file.Close()
		}
		clear(raw)
		return opaqueRevision{}, ErrUnsafePath
	}
	if err := target.file.Close(); err != nil {
		clear(raw)
		return opaqueRevision{}, ErrStore
	}
	return opaqueRevision{
		path: target.path, exists: true, bytes: raw,
		digest: sha256.Sum256(raw), file: target.metadata, parent: target.parent,
	}, nil
}

func validateOpaqueRevision(expected opaqueRevision) error {
	current, err := loadOpaqueRevision(expected.path)
	if err != nil {
		return err
	}
	defer clear(current.bytes)
	if current.path != expected.path || current.exists != expected.exists {
		return ErrUnsafePath
	}
	if expected.exists {
		if current.file != expected.file || current.digest != expected.digest ||
			!bytes.Equal(current.bytes, expected.bytes) {
			return ErrUnsafePath
		}
		return nil
	}
	// Staging transaction-owned siblings legitimately changes the private
	// parent directory metadata. Absence of this exact leaf remains the
	// revision evidence while the anchored directory identity is retained.
	return nil
}

func (writer *Writer) failBeforeConfig(
	directory *transactionDirectory,
	_ bool,
	prior opaqueRevision,
	published *ownedStoreFile,
	restore *ownedStoreFile,
	cause error,
) (CommitResult, error) {
	if directory == nil || published == nil {
		return CommitResult{}, fixedCommitError(cause)
	}
	if err := writer.runCommitHook(commitBeforeBackupRestore); err != nil {
		return CommitResult{State: CommitRecoveryRequired}, errors.Join(cause, err)
	}
	if err := published.validate(); err != nil {
		return CommitResult{State: CommitRecoveryRequired}, errors.Join(cause, err)
	}
	if hookErr := writer.runOperationHook(operationBackupRestore); hookErr != nil {
		return CommitResult{State: CommitRecoveryRequired}, errors.Join(cause, hookErr)
	}
	var restoreErr error
	if prior.exists {
		if restore == nil {
			restoreErr = ErrStore
		} else {
			restoreErr = directory.publish(
				context.Background(), restore, filepath.Base(prior.path), false, nil,
			)
			if restoreErr == nil {
				published.linked = false
				restore.temporary = false
			}
		}
	} else {
		restoreErr = published.remove()
	}
	if restoreErr == nil {
		restoreErr = writer.syncConfigDirectory(directory.file)
	}
	if restoreErr == nil {
		restoreErr = verifyRestoredBackup(prior)
	}
	if restoreErr != nil {
		return CommitResult{State: CommitRecoveryRequired}, errors.Join(cause, restoreErr)
	}
	return CommitResult{}, cause
}

func verifyRestoredBackup(prior opaqueRevision) error {
	current, err := loadOpaqueRevision(prior.path)
	if err != nil {
		return err
	}
	defer clear(current.bytes)
	if current.exists != prior.exists {
		return ErrUnsafePath
	}
	if !prior.exists {
		return nil
	}
	if current.digest != prior.digest || !bytes.Equal(current.bytes, prior.bytes) {
		return ErrUnsafePath
	}
	return nil
}

func (writer *Writer) rollbackPublishedConfig(
	directory *transactionDirectory,
	base Snapshot,
	candidate *ownedStoreFile,
	rollback *ownedStoreFile,
) (CommitResult, error) {
	indeterminate := CommitResult{State: CommitIndeterminate}
	if err := writer.runCommitHook(commitBeforeConfigRollback); err != nil {
		return indeterminate, err
	}
	if candidate == nil || candidate.validate() != nil {
		return indeterminate, ErrUnsafePath
	}
	if base.exists {
		if rollback == nil {
			return indeterminate, ErrStore
		}
		if err := writer.runOperationHook(operationRollback); err != nil {
			return indeterminate, err
		}
		if err := directory.publish(
			context.Background(), rollback, filepath.Base(base.path), false, nil,
		); err != nil {
			return indeterminate, err
		}
		candidate.linked = false
		rollback.temporary = false
	} else {
		if err := writer.runOperationHook(operationRollback); err != nil {
			return indeterminate, err
		}
		if err := candidate.remove(); err != nil {
			return indeterminate, err
		}
	}
	if err := writer.syncConfigDirectory(directory.file); err != nil {
		return indeterminate, err
	}
	if base.exists {
		if err := rollback.validate(); err != nil {
			return indeterminate, err
		}
	} else if current, err := writer.Load(context.Background(), base.path); err != nil || current.exists {
		return indeterminate, ErrUnsafePath
	}
	return CommitResult{State: CommitRolledBack}, nil
}

func fixedCommitError(err error) error {
	if err == nil {
		return nil
	}
	var result error
	if errors.Is(err, context.Canceled) {
		result = errors.Join(result, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		result = errors.Join(result, context.DeadlineExceeded)
	}
	if errors.Is(err, ErrInvalidConfig) {
		result = errors.Join(result, ErrInvalidConfig)
	}
	if errors.Is(err, ErrUnsafePath) {
		result = errors.Join(result, ErrUnsafePath)
	}
	if errors.Is(err, ErrStore) || result == nil {
		result = errors.Join(result, ErrStore)
	}
	return result
}
