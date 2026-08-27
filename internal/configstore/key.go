package configstore

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
)

type keyCommitMode uint8

const (
	keyCommitNoop keyCommitMode = iota
	keyCommitReuse
	keyCommitCreate
)

type stagedKey struct {
	root              *os.Root
	directory         *os.File
	file              *os.File
	directoryIdentity fs.FileInfo
	identity          fs.FileInfo
	targetPath        string
	targetName        string
	tempPath          string
	tempName          string
	distinctPaths     []string
	published         bool
	closed            bool
}

func validateKeyCommitMatrix(
	plan KeyPlan,
	state KeyState,
	payload []byte,
) (keyCommitMode, error) {
	switch state {
	case KeyStateNone:
		if plan.Intent != KeyIntentNone || plan.Path != "" || plan.AllowExisting ||
			len(plan.DistinctFrom) != 0 || len(payload) != 0 {
			return keyCommitNoop, ErrStore
		}
		return keyCommitNoop, nil
	case KeyStateMissing:
		if plan.Intent != KeyIntentEnsure || !validGeneratedKeyPayload(payload) {
			return keyCommitNoop, ErrStore
		}
		return keyCommitCreate, nil
	case KeyStateNeedsConfirmation:
		return keyCommitNoop, ErrUnsafePath
	case KeyStateReusable:
		if len(payload) != 0 {
			return keyCommitNoop, ErrStore
		}
		if plan.Intent == KeyIntentInspect && !plan.AllowExisting {
			return keyCommitReuse, nil
		}
		if plan.Intent == KeyIntentEnsure && plan.AllowExisting {
			return keyCommitReuse, nil
		}
		return keyCommitNoop, ErrStore
	default:
		return keyCommitNoop, ErrStore
	}
}

func validGeneratedKeyPayload(payload []byte) bool {
	if len(payload) != 65 || payload[64] != '\n' {
		return false
	}
	snapshot, err := gatewaykey.Parse(bytes.NewReader(payload))
	return err == nil && snapshot.Valid() && snapshot.Enabled()
}

func (writer *Writer) stageKey(
	ctx context.Context,
	mutation Mutation,
	preflight PreflightResult,
	payload []byte,
) (*stagedKey, error) {
	if writer == nil || ctx == nil || writer.ops.syncFile == nil ||
		ctx.Err() != nil || !validSnapshotShape(mutation.Base) ||
		len(mutation.Candidate) == 0 || len(mutation.Candidate) > maxConfigBytes {
		return nil, ErrStore
	}
	candidate, err := config.Decode(bytes.NewReader(mutation.Candidate))
	if err != nil {
		return nil, ErrInvalidConfig
	}
	if _, err := validateKeyPlan(candidate.Server.APIKeyFile, mutation); err != nil {
		return nil, err
	}
	mode, err := validateKeyCommitMatrix(mutation.Key, preflight.KeyState, payload)
	if err != nil {
		return nil, err
	}
	switch mode {
	case keyCommitNoop:
		return nil, nil
	case keyCommitReuse:
		if err := validateReusableKey(mutation); err != nil {
			return nil, err
		}
		return nil, nil
	case keyCommitCreate:
		return writer.stageNewKey(ctx, mutation, payload)
	default:
		return nil, ErrStore
	}
}

func validateReusableKey(mutation Mutation) error {
	paths, err := keyDistinctPaths(mutation)
	if err != nil {
		return err
	}
	distinct, err := loadDistinctFileInfo(paths)
	if err != nil {
		return err
	}
	if _, err := gatewaykey.LoadFile(mutation.Key.Path, distinct); err != nil {
		return ErrUnsafePath
	}
	return nil
}

func (writer *Writer) stageNewKey(
	ctx context.Context,
	mutation Mutation,
	payload []byte,
) (result *stagedKey, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fixedContextError(err)
	}
	exists, err := inspectNativePrivateFile(mutation.Key.Path)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrUnsafePath
	}
	directoryPath := filepath.Dir(mutation.Key.Path)
	directoryExists, err := inspectNativePrivateDirectory(directoryPath)
	if err != nil || !directoryExists {
		if err != nil {
			return nil, err
		}
		return nil, ErrUnsafePath
	}
	if err := writer.runOperationHook(operationOpen); err != nil {
		return nil, err
	}
	root, directory, directoryIdentity, err := openKeyDirectory(directoryPath)
	if err != nil {
		return nil, err
	}
	closeAnchor := true
	defer func() {
		if closeAnchor {
			_ = directory.Close()
			_ = root.Close()
		}
	}()

	targetName := filepath.Base(mutation.Key.Path)
	tempName := "." + targetName + ".ai-cli-gateway.tmp"
	tempPath := filepath.Join(directoryPath, tempName)
	if _, ok := nativeStorePathKey(tempPath); !ok || filepath.Base(tempPath) != tempName {
		return nil, ErrUnsafePath
	}
	if err := writer.runOperationHook(operationCreate); err != nil {
		return nil, err
	}
	file, err := createNativePrivateFile(directory, root, tempName)
	if err != nil {
		return nil, err
	}
	staged := &stagedKey{
		root:              root,
		directory:         directory,
		file:              file,
		directoryIdentity: directoryIdentity,
		targetPath:        mutation.Key.Path,
		targetName:        targetName,
		tempPath:          tempPath,
		tempName:          tempName,
	}
	identity, statErr := file.Stat()
	if statErr != nil || identity == nil || !identity.Mode().IsRegular() || identity.Size() != 0 {
		return nil, errors.Join(ErrStore, staged.rollback())
	}
	staged.identity = identity
	if err := writer.runOperationHook(operationStat); err != nil {
		return nil, errors.Join(err, staged.rollback())
	}
	staged.distinctPaths, err = keyDistinctPaths(mutation)
	if err != nil {
		return nil, errors.Join(err, staged.rollback())
	}
	closeAnchor = false
	defer func() {
		if resultErr != nil && !staged.closed {
			resultErr = errors.Join(resultErr, staged.rollback())
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
		afterWrite.Size() != 65 || !os.SameFile(staged.identity, afterWrite) {
		return nil, ErrStore
	}
	file, err = sealNativePrivateFile(staged.directory, file, staged.tempName)
	staged.file = file
	if err != nil {
		return nil, err
	}
	afterSeal, err := file.Stat()
	if err != nil || afterSeal == nil || !afterSeal.Mode().IsRegular() ||
		afterSeal.Size() != 65 || !os.SameFile(staged.identity, afterSeal) {
		return nil, ErrUnsafePath
	}
	if err := staged.validatePath(staged.tempName, staged.tempPath); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fixedContextError(err)
	}
	return staged, nil
}

func writeComplete(file *os.File, payload []byte) error {
	if file == nil {
		return ErrStore
	}
	remaining := payload
	for len(remaining) != 0 {
		written, err := file.Write(remaining)
		if err != nil || written <= 0 || written > len(remaining) {
			return ErrStore
		}
		remaining = remaining[written:]
	}
	return nil
}

func openKeyDirectory(path string) (*os.Root, *os.File, fs.FileInfo, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, nil, ErrStore
	}
	directory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, nil, nil, ErrStore
	}
	handleInfo, handleErr := directory.Stat()
	pathInfo, pathErr := os.Lstat(path)
	exists, inspectErr := inspectNativePrivateDirectory(path)
	if handleErr != nil || pathErr != nil || inspectErr != nil || !exists ||
		handleInfo == nil || pathInfo == nil || !handleInfo.IsDir() ||
		pathInfo.Mode()&fs.ModeSymlink != 0 || !pathInfo.IsDir() ||
		!os.SameFile(handleInfo, pathInfo) {
		_ = directory.Close()
		_ = root.Close()
		return nil, nil, nil, ErrUnsafePath
	}
	return root, directory, handleInfo, nil
}

func (staged *stagedKey) directoryMatches() bool {
	if staged == nil || staged.root == nil || staged.directory == nil ||
		staged.directoryIdentity == nil {
		return false
	}
	handleInfo, handleErr := staged.directory.Stat()
	pathInfo, pathErr := os.Lstat(filepath.Dir(staged.targetPath))
	exists, inspectErr := inspectNativePrivateDirectory(filepath.Dir(staged.targetPath))
	return handleErr == nil && pathErr == nil && inspectErr == nil && exists &&
		handleInfo != nil && pathInfo != nil && handleInfo.IsDir() &&
		pathInfo.Mode()&fs.ModeSymlink == 0 && pathInfo.IsDir() &&
		os.SameFile(staged.directoryIdentity, handleInfo) &&
		os.SameFile(handleInfo, pathInfo)
}

func (staged *stagedKey) validatePath(name string, path string) error {
	if staged == nil || staged.closed || staged.file == nil || staged.identity == nil ||
		!staged.directoryMatches() {
		return ErrUnsafePath
	}
	handleInfo, handleErr := staged.file.Stat()
	pathInfo, pathErr := staged.root.Lstat(name)
	if handleErr != nil || pathErr != nil || handleInfo == nil || pathInfo == nil ||
		!handleInfo.Mode().IsRegular() || pathInfo.Mode()&fs.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() || !os.SameFile(staged.identity, handleInfo) ||
		!os.SameFile(handleInfo, pathInfo) {
		return ErrUnsafePath
	}
	distinct, err := loadDistinctFileInfo(staged.distinctPaths)
	if err != nil {
		return err
	}
	if _, err := gatewaykey.LoadFile(path, distinct); err != nil {
		return ErrUnsafePath
	}
	after, err := staged.root.Lstat(name)
	if err != nil || after == nil || after.Mode()&fs.ModeSymlink != 0 ||
		!after.Mode().IsRegular() || !os.SameFile(staged.identity, after) {
		return ErrUnsafePath
	}
	return nil
}

func (staged *stagedKey) publish(ctx context.Context) error {
	if staged == nil || ctx == nil || staged.closed || staged.published {
		return ErrStore
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(fixedContextError(err), staged.rollback())
	}
	if err := staged.validatePath(staged.tempName, staged.tempPath); err != nil {
		return errors.Join(err, staged.rollback())
	}
	if _, err := staged.root.Lstat(staged.targetName); err == nil {
		return errors.Join(ErrUnsafePath, staged.rollback())
	} else if !errors.Is(err, fs.ErrNotExist) {
		return errors.Join(ErrStore, staged.rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(fixedContextError(err), staged.rollback())
	}
	if err := nativeRenameNoReplace(
		staged.directory, staged.root, staged.file, staged.tempName, staged.targetName,
	); err != nil {
		return errors.Join(err, staged.rollback())
	}
	staged.published = true
	if err := staged.validatePath(staged.targetName, staged.targetPath); err != nil {
		return errors.Join(err, staged.rollback())
	}
	if err := nativeSyncPrivateDirectory(staged.directory); err != nil {
		return errors.Join(ErrStore, staged.rollback())
	}
	return nil
}

func (staged *stagedKey) rollback() error {
	if staged == nil || staged.closed {
		return ErrStore
	}
	name := staged.tempName
	if staged.published {
		name = staged.targetName
	}
	cleanupErr := staged.removeOwned(name)
	closeErr := staged.closeHandles()
	return errors.Join(cleanupErr, closeErr)
}

func (staged *stagedKey) finish() error {
	if staged == nil || staged.closed || !staged.published {
		return ErrStore
	}
	return staged.closeHandles()
}

func (staged *stagedKey) removeOwned(name string) error {
	if staged.root == nil || staged.identity == nil || !staged.directoryMatches() {
		return ErrUnsafePath
	}
	current, err := staged.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || current == nil || current.Mode()&fs.ModeSymlink != 0 ||
		!current.Mode().IsRegular() || !os.SameFile(staged.identity, current) {
		return ErrUnsafePath
	}
	if err := staged.root.Remove(name); err != nil {
		return ErrStore
	}
	if _, err := staged.root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		return ErrUnsafePath
	}
	if err := nativeSyncPrivateDirectory(staged.directory); err != nil {
		return ErrStore
	}
	return nil
}

func (staged *stagedKey) closeHandles() error {
	if staged == nil || staged.closed {
		return ErrStore
	}
	staged.closed = true
	file := staged.file
	directory := staged.directory
	root := staged.root
	staged.file = nil
	staged.directory = nil
	staged.root = nil
	if errors.Join(closeStoreFile(file), closeStoreFile(directory), closeStoreRoot(root)) != nil {
		return ErrStore
	}
	return nil
}

func closeStoreFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func closeStoreRoot(root *os.Root) error {
	if root == nil {
		return nil
	}
	return root.Close()
}

func keyDistinctPaths(mutation Mutation) ([]string, error) {
	paths := append([]string{
		mutation.Base.path,
		BackupPath(mutation.Base.path),
		LockPath(mutation.Base.path),
	}, mutation.Key.DistinctFrom...)
	targetKey, ok := nativeStorePathKey(mutation.Key.Path)
	if !ok {
		return nil, ErrUnsafePath
	}
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		key, valid := nativeStorePathKey(path)
		if !valid || key == targetKey {
			return nil, ErrUnsafePath
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, path)
	}
	return result, nil
}

func loadDistinctFileInfo(paths []string) ([]fs.FileInfo, error) {
	result := make([]fs.FileInfo, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || info == nil {
			return nil, ErrUnsafePath
		}
		result = append(result, info)
	}
	return result, nil
}
