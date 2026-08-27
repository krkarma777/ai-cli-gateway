//go:build windows

package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func acquireNativeLock(
	ctx context.Context,
	directory *transactionDirectory,
	path string,
) (*os.File, error) {
	if directory == nil || !directory.matches() {
		return nil, ErrUnsafePath
	}
	file, metadata, err := openOrCreateWindowsLock(directory, path)
	if err != nil {
		return nil, err
	}
	locked := false
	defer func() {
		if !locked {
			_ = file.Close()
		}
	}()
	backoff := 5 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return nil, fixedContextError(err)
		}
		var overlapped windows.Overlapped
		err := windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&overlapped,
		)
		if err == nil {
			current, ok := windowsStoreMetadata(file, false)
			if !ok || current != metadata || !safeNativeLockMetadata(current) ||
				!safeWindowsStoreSecurity(file, false, true) ||
				!windowsStoreFinalPathMatches(file, path) || !directory.matches() {
				var release windows.Overlapped
				_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &release)
				return nil, ErrUnsafePath
			}
			locked = true
			return file, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrStore
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, fixedContextError(ctx.Err())
		case <-timer.C:
		}
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

func openOrCreateWindowsLock(
	directory *transactionDirectory,
	path string,
) (*os.File, nativeFileMetadata, error) {
	clean, ok := cleanWindowsStorePath(path)
	if !ok || directory == nil || directory.file == nil || !directory.matches() ||
		!sameNativeStorePath(filepath.Dir(clean), directory.path) {
		return nil, nativeFileMetadata{}, ErrUnsafePath
	}
	name, err := windows.NewNTUnicodeString(filepath.Base(clean))
	if err != nil {
		return nil, nativeFileMetadata{}, ErrUnsafePath
	}
	_, descriptor, err := windowsStoreSecurityAttributes(false)
	if err != nil {
		return nil, nativeFileMetadata{}, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      windows.Handle(directory.file.Fd()),
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
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.SYNCHRONIZE,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) ||
		errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		attributes.SecurityDescriptor = nil
		err = windows.NtCreateFile(
			&handle,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.SYNCHRONIZE,
			attributes,
			&status,
			nil,
			windows.FILE_ATTRIBUTE_NORMAL,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			windows.FILE_OPEN,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|
				windows.FILE_SYNCHRONOUS_IO_NONALERT,
			0,
			0,
		)
	}
	runtime.KeepAlive(descriptor)
	if err != nil {
		return nil, nativeFileMetadata{}, ErrUnsafePath
	}
	file := os.NewFile(uintptr(handle), "configstore-lock")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, nativeFileMetadata{}, ErrStore
	}
	metadata, metadataOK := windowsStoreMetadata(file, false)
	if !metadataOK || !safeNativeLockMetadata(metadata) || !safeWindowsStoreSecurity(file, false, true) ||
		!windowsStoreFinalPathMatches(file, clean) || !directory.matches() ||
		!safeWindowsStoreAncestors(clean) {
		_ = file.Close()
		return nil, nativeFileMetadata{}, ErrUnsafePath
	}
	return file, metadata, nil
}

func safeNativeLockMetadata(metadata nativeFileMetadata) bool {
	return metadata.size == 0
}

func unlockNativeFile(file *os.File) error {
	if file == nil {
		return ErrStore
	}
	var overlapped windows.Overlapped
	if err := windows.UnlockFileEx(
		windows.Handle(file.Fd()), 0, 1, 0, &overlapped,
	); err != nil {
		return ErrStore
	}
	return nil
}
