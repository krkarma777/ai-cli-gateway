//go:build !windows

package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func acquireNativeLock(
	ctx context.Context,
	directory *transactionDirectory,
	path string,
) (*os.File, error) {
	if directory == nil || !directory.matches() {
		return nil, ErrUnsafePath
	}
	file, metadata, err := openOrCreateUnixLock(directory, path)
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
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			if !directory.matches() || !revalidateUnixPrivateFile(path, file, metadata) {
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				return nil, ErrUnsafePath
			}
			locked = true
			return file, nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EAGAIN):
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
		default:
			return nil, ErrStore
		}
	}
}

func openOrCreateUnixLock(
	directory *transactionDirectory,
	path string,
) (*os.File, nativeFileMetadata, error) {
	clean, ok := cleanUnixStorePath(path)
	if !ok || directory == nil || directory.file == nil || !directory.matches() ||
		!sameNativeStorePath(filepath.Dir(clean), directory.path) {
		return nil, nativeFileMetadata{}, ErrUnsafePath
	}
	name := filepath.Base(clean)
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Openat(
		int(directory.file.Fd()), name, flags|unix.O_CREAT|unix.O_EXCL, 0o600,
	)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(directory.file.Fd()), name, flags, 0)
	}
	if err != nil {
		return nil, nativeFileMetadata{}, ErrUnsafePath
	}
	file := os.NewFile(uintptr(fd), "configstore-lock")
	if file == nil {
		_ = unix.Close(fd)
		return nil, nativeFileMetadata{}, ErrStore
	}
	if created && unix.Fchmod(fd, 0o600) != nil {
		_ = file.Close()
		return nil, nativeFileMetadata{}, ErrStore
	}
	var handle unix.Stat_t
	var selected unix.Stat_t
	if unix.Fstat(fd, &handle) != nil || unix.Lstat(clean, &selected) != nil {
		_ = file.Close()
		return nil, nativeFileMetadata{}, ErrStore
	}
	handleMetadata, handleOK := unixStoreMetadata(handle)
	selectedMetadata, selectedOK := unixStoreMetadata(selected)
	if !handleOK || !selectedOK || handleMetadata != selectedMetadata ||
		!safeNativeLockMetadata(handleMetadata) ||
		!directory.matches() || !safeUnixStoreAncestors(clean) {
		_ = file.Close()
		return nil, nativeFileMetadata{}, ErrUnsafePath
	}
	return file, handleMetadata, nil
}

func safeNativeLockMetadata(metadata nativeFileMetadata) bool {
	return safeUnixStoreFile(metadata) && metadata.mode&0o777 == 0o600 && metadata.size == 0
}

func revalidateUnixPrivateFile(path string, file *os.File, baseline nativeFileMetadata) bool {
	if file == nil {
		return false
	}
	var handle unix.Stat_t
	var selected unix.Stat_t
	if unix.Fstat(int(file.Fd()), &handle) != nil || unix.Lstat(path, &selected) != nil {
		return false
	}
	handleMetadata, handleOK := unixStoreMetadata(handle)
	selectedMetadata, selectedOK := unixStoreMetadata(selected)
	return handleOK && selectedOK && handleMetadata == baseline &&
		selectedMetadata == baseline && safeUnixStoreFile(baseline) &&
		safeNativeLockMetadata(baseline) && safeUnixStoreAncestors(path) &&
		filepath.Dir(path) != path
}

func unlockNativeFile(file *os.File) error {
	if file == nil {
		return ErrStore
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return ErrStore
	}
	return nil
}
