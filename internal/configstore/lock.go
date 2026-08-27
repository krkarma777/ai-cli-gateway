package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type storeLock struct {
	mu        sync.Mutex
	file      *os.File
	directory *transactionDirectory
}

func (writer *Writer) acquireLock(ctx context.Context, base Snapshot) (*storeLock, error) {
	if writer == nil || ctx == nil || ctx.Err() != nil || !validSnapshotShape(base) {
		return nil, ErrStore
	}
	directory, err := writer.bootstrapTransactionDirectory(ctx, base)
	if err != nil {
		return nil, err
	}
	if err := writer.runOperationHook(operationLock); err != nil {
		_ = directory.cleanup()
		return nil, err
	}
	file, err := acquireNativeLock(ctx, directory, LockPath(base.path))
	if err != nil {
		_ = directory.cleanup()
		return nil, err
	}
	return &storeLock{file: file, directory: directory}, nil
}

func (lock *storeLock) release() error {
	if lock == nil {
		return ErrStore
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.file == nil {
		return ErrStore
	}
	file := lock.file
	directory := lock.directory
	lock.file = nil
	lock.directory = nil
	err := errors.Join(unlockNativeFile(file), file.Close())
	if directory != nil {
		err = errors.Join(err, directory.cleanup())
	}
	if err != nil {
		return ErrStore
	}
	return nil
}

func snapshotConfigDirectories(base Snapshot) ([]string, bool) {
	if !validSnapshotShape(base) {
		return nil, false
	}
	configDirectory := filepath.Dir(base.path)
	if base.exists {
		return nil, sameNativeStorePath(base.parent.path, configDirectory)
	}
	if len(base.missing) == 0 || !sameNativeStorePath(base.missing[0], base.path) {
		return nil, false
	}
	ordered := make([]string, 0, len(base.missing)-1)
	for index := len(base.missing) - 1; index >= 1; index-- {
		ordered = append(ordered, base.missing[index])
	}
	current := base.parent.path
	for _, directory := range ordered {
		if !sameNativeStorePath(filepath.Dir(directory), current) {
			return nil, false
		}
		current = directory
	}
	if !sameNativeStorePath(current, configDirectory) {
		return nil, false
	}
	return ordered, true
}

func sameNativeStorePath(left string, right string) bool {
	leftKey, leftOK := nativeStorePathKey(left)
	rightKey, rightOK := nativeStorePathKey(right)
	return leftOK && rightOK && leftKey == rightKey
}
