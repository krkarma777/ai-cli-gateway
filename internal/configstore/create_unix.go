//go:build !windows

package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

func (writer *Writer) createPrivateDirectories(ctx context.Context, paths []string) error {
	if writer == nil || ctx == nil {
		return ErrStore
	}
	unique := make(map[string]string, len(paths))
	for _, path := range paths {
		key, ok := nativeStorePathKey(path)
		if !ok {
			return ErrUnsafePath
		}
		unique[key] = path
	}
	ordered := make([]string, 0, len(unique))
	for _, path := range unique {
		ordered = append(ordered, path)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return directoryDepth(ordered[left]) < directoryDepth(ordered[right])
	})
	for _, path := range ordered {
		if err := writer.createUnixPrivateDirectory(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func (writer *Writer) createUnixPrivateDirectory(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return fixedContextError(err)
	}
	if exists, err := inspectNativePrivateDirectory(path); err != nil || exists {
		return err
	}
	current := path
	var missing []string
	privateParent := true
	for {
		var stat unix.Stat_t
		err := unix.Lstat(current, &stat)
		if err == nil {
			metadata, ok := unixStoreMetadata(stat)
			if !ok || !safeUnixStoreDirectory(metadata, false) ||
				!safeUnixStoreAncestorsFrom(current, false) {
				return ErrUnsafePath
			}
			privateParent = current == path
			break
		}
		if !errors.Is(err, unix.ENOENT) {
			return ErrStore
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return ErrUnsafePath
		}
		current = parent
		privateParent = false
	}
	evidence, err := openUnixStoreDirectory(current, privateParent)
	if err != nil {
		return err
	}
	parent, err := openUnixSnapshotDirectory(evidence, privateParent)
	if err != nil {
		return err
	}
	ordered := make([]string, 0, len(missing))
	for index := len(missing) - 1; index >= 0; index-- {
		ordered = append(ordered, missing[index])
	}
	parent, _, err = writer.createUnixDirectoryChain(ctx, parent, current, ordered, privateParent)
	if parent != nil {
		err = errors.Join(err, parent.Close())
	}
	if err != nil {
		return fixedStoreError(err)
	}
	return nil
}

func (writer *Writer) bootstrapTransactionDirectory(
	ctx context.Context,
	base Snapshot,
) (*transactionDirectory, error) {
	if writer == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrStore
	}
	directories, ok := snapshotConfigDirectories(base)
	if !ok {
		return nil, ErrUnsafePath
	}
	privateParent := len(directories) == 0
	parent, err := openUnixSnapshotDirectory(base.parent, privateParent)
	if err != nil {
		return nil, err
	}
	parent, finalPath, err := writer.createUnixDirectoryChain(
		ctx, parent, base.parent.path, directories, privateParent,
	)
	if err != nil {
		return nil, err
	}
	directory, err := transactionDirectoryFromAnchor(finalPath, parent)
	if err != nil {
		return nil, err
	}
	return directory, nil
}

func openUnixSnapshotDirectory(
	evidence nativeDirectoryEvidence,
	private bool,
) (*os.File, error) {
	if evidence.path == "" {
		return nil, ErrUnsafePath
	}
	fd, err := unix.Open(
		evidence.path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, ErrUnsafePath
	}
	file := os.NewFile(uintptr(fd), "configstore-directory-anchor")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrStore
	}
	if !unixDirectoryFileMatches(file, evidence.path, evidence.metadata, private) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	return file, nil
}

func (writer *Writer) createUnixDirectoryChain(
	ctx context.Context,
	parent *os.File,
	parentPath string,
	directories []string,
	parentPrivate bool,
) (*os.File, string, error) {
	if parent == nil || parentPath == "" {
		return nil, "", ErrStore
	}
	current := parent
	currentPath := parentPath
	currentPrivate := parentPrivate
	for _, childPath := range directories {
		if err := ctx.Err(); err != nil {
			_ = current.Close()
			return nil, "", fixedContextError(err)
		}
		if !sameNativeStorePath(filepath.Dir(childPath), currentPath) ||
			!unixDirectoryFileMatchesCurrent(current, currentPath, currentPrivate) {
			_ = current.Close()
			return nil, "", ErrUnsafePath
		}
		if err := writer.runOperationHook(operationMkdir); err != nil {
			_ = current.Close()
			return nil, "", err
		}
		mkdirErr := unix.Mkdirat(int(current.Fd()), filepath.Base(childPath), 0o700)
		if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			_ = current.Close()
			return nil, "", ErrStore
		}
		child, err := openUnixChildDirectory(current, childPath)
		closeErr := current.Close()
		if err != nil || closeErr != nil {
			if child != nil {
				_ = child.Close()
			}
			return nil, "", fixedStoreError(errors.Join(err, closeErr))
		}
		current = child
		currentPath = childPath
		currentPrivate = true
	}
	return current, currentPath, nil
}

func openUnixChildDirectory(parent *os.File, path string) (*os.File, error) {
	fd, err := unix.Openat(
		int(parent.Fd()),
		filepath.Base(path),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, ErrUnsafePath
	}
	file := os.NewFile(uintptr(fd), "configstore-directory-component")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrStore
	}
	var handle unix.Stat_t
	if unix.Fstat(fd, &handle) != nil {
		_ = file.Close()
		return nil, ErrStore
	}
	metadata, ok := unixStoreMetadata(handle)
	if !ok || !unixDirectoryFileMatches(file, path, metadata, true) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	return file, nil
}

func unixDirectoryFileMatchesCurrent(file *os.File, path string, private bool) bool {
	if file == nil {
		return false
	}
	var handle unix.Stat_t
	if unix.Fstat(int(file.Fd()), &handle) != nil {
		return false
	}
	metadata, ok := unixStoreMetadata(handle)
	return ok && unixDirectoryFileMatches(file, path, metadata, private)
}

func unixDirectoryFileMatches(
	file *os.File,
	path string,
	expected nativeFileMetadata,
	private bool,
) bool {
	if file == nil || path == "" {
		return false
	}
	var handle unix.Stat_t
	var selected unix.Stat_t
	if unix.Fstat(int(file.Fd()), &handle) != nil || unix.Lstat(path, &selected) != nil {
		return false
	}
	handleMetadata, handleOK := unixStoreMetadata(handle)
	selectedMetadata, selectedOK := unixStoreMetadata(selected)
	return handleOK && selectedOK && handleMetadata == selectedMetadata &&
		sameNativeDirectoryIdentity(handleMetadata, expected) &&
		safeUnixStoreDirectory(handleMetadata, private) &&
		safeUnixStoreAncestorsFrom(path, private)
}

func transactionDirectoryFromAnchor(
	path string,
	anchor *os.File,
) (*transactionDirectory, error) {
	if anchor == nil {
		return nil, ErrStore
	}
	anchorInfo, err := anchor.Stat()
	if err != nil || anchorInfo == nil || !anchorInfo.IsDir() {
		_ = anchor.Close()
		return nil, ErrStore
	}
	directory, err := openTransactionDirectory(path)
	closeErr := anchor.Close()
	if err != nil || closeErr != nil {
		if directory != nil {
			_ = directory.cleanup()
		}
		return nil, fixedStoreError(errors.Join(err, closeErr))
	}
	if !directory.matches() || !os.SameFile(anchorInfo, directory.identity) {
		_ = directory.cleanup()
		return nil, ErrUnsafePath
	}
	return directory, nil
}

func directoryDepth(path string) int {
	depth := 0
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		depth++
		if parent := filepath.Dir(current); parent == current {
			return depth
		}
	}
}
