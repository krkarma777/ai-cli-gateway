//go:build windows

package configstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"unsafe"

	"golang.org/x/sys/windows"
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
		if err := writer.createWindowsPrivateDirectory(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func (writer *Writer) createWindowsPrivateDirectory(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return fixedContextError(err)
	}
	if exists, err := inspectNativePrivateDirectory(path); err != nil || exists {
		return err
	}
	current := path
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			if !safeWindowsStoreAncestorsFrom(current, false) {
				return ErrUnsafePath
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ErrStore
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return ErrUnsafePath
		}
		current = parent
	}
	evidence, err := openWindowsStoreDirectory(current, false)
	if err != nil {
		return err
	}
	parent, err := openWindowsSnapshotDirectory(evidence, false)
	if err != nil {
		return err
	}
	ordered := make([]string, 0, len(missing))
	for index := len(missing) - 1; index >= 0; index-- {
		ordered = append(ordered, missing[index])
	}
	parent, _, err = writer.createWindowsDirectoryChain(ctx, parent, current, ordered, false)
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
	parent, err := openWindowsSnapshotDirectory(base.parent, privateParent)
	if err != nil {
		return nil, err
	}
	parent, finalPath, err := writer.createWindowsDirectoryChain(
		ctx, parent, base.parent.path, directories, privateParent,
	)
	if err != nil {
		return nil, err
	}
	directory, err := transactionDirectoryFromWindowsAnchor(finalPath, parent)
	if err != nil {
		return nil, err
	}
	return directory, nil
}

func openWindowsSnapshotDirectory(
	evidence nativeDirectoryEvidence,
	private bool,
) (*os.File, error) {
	if evidence.path == "" {
		return nil, ErrUnsafePath
	}
	file, err := openWindowsStorePath(evidence.path, true)
	if err != nil {
		return nil, err
	}
	if !windowsDirectoryFileMatches(file, evidence.path, evidence.metadata, private) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	return file, nil
}

func (writer *Writer) createWindowsDirectoryChain(
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
			!windowsDirectoryFileMatchesCurrent(current, currentPath, currentPrivate) {
			_ = current.Close()
			return nil, "", ErrUnsafePath
		}
		if err := writer.runOperationHook(operationMkdir); err != nil {
			_ = current.Close()
			return nil, "", err
		}
		child, err := openOrCreateWindowsDirectoryAt(current, childPath)
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

func openOrCreateWindowsDirectoryAt(parent *os.File, path string) (*os.File, error) {
	if parent == nil || filepath.Base(path) == "." {
		return nil, ErrUnsafePath
	}
	name, err := windows.NewNTUnicodeString(filepath.Base(path))
	if err != nil {
		return nil, ErrUnsafePath
	}
	_, descriptor, err := windowsStoreSecurityAttributes(true)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      windows.Handle(parent.Fd()),
		ObjectName:         name,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	var (
		handle windows.Handle
		status windows.IO_STATUS_BLOCK
	)
	err = windows.NtCreateFile(
		&handle,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.SYNCHRONIZE,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN_IF,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return nil, ErrStore
	}
	file := os.NewFile(uintptr(handle), "configstore-directory-component")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, ErrStore
	}
	metadata, ok := windowsStoreMetadata(file, true)
	if !ok || !windowsDirectoryFileMatches(file, path, metadata, true) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	return file, nil
}

func windowsDirectoryFileMatchesCurrent(file *os.File, path string, private bool) bool {
	metadata, ok := windowsStoreMetadata(file, true)
	return ok && windowsDirectoryFileMatches(file, path, metadata, private)
}

func windowsDirectoryFileMatches(
	file *os.File,
	path string,
	expected nativeFileMetadata,
	private bool,
) bool {
	metadata, ok := windowsStoreMetadata(file, true)
	return ok && sameNativeDirectoryIdentity(metadata, expected) &&
		safeWindowsStoreSecurity(file, true, private) &&
		windowsStoreFinalPathMatches(file, path) &&
		safeWindowsStoreAncestorsFrom(path, private)
}

func transactionDirectoryFromWindowsAnchor(
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

func windowsStoreSecurityAttributes(
	inherit bool,
) (*windows.SecurityAttributes, *windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, nil, ErrStore
	}
	flags := ""
	if inherit {
		flags = "OICI"
	}
	sddl := fmt.Sprintf(
		"O:%[1]sD:P(A;%[2]s;FA;;;%[1]s)(A;%[2]s;FA;;;SY)(A;%[2]s;FA;;;BA)",
		user.User.Sid.String(),
		flags,
	)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, ErrStore
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	return attributes, descriptor, nil
}
