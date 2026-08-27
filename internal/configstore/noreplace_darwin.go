//go:build darwin

package configstore

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func createNativePrivateFile(directory *os.File, _ *os.Root, name string) (*os.File, error) {
	if directory == nil {
		return nil, ErrStore
	}
	fd, err := unix.Openat(
		int(directory.Fd()), name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ELOOP) {
			return nil, ErrUnsafePath
		}
		return nil, ErrStore
	}
	file := os.NewFile(uintptr(fd), "configstore-key-temp")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrStore
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = file.Close()
		return nil, ErrStore
	}
	return file, nil
}

func nativeRenameNoReplace(
	directory *os.File,
	_ *os.Root,
	_ *os.File,
	oldName string,
	newName string,
) error {
	if directory == nil {
		return ErrStore
	}
	err := unix.RenameatxNp(
		int(directory.Fd()), oldName,
		int(directory.Fd()), newName,
		unix.RENAME_EXCL,
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EEXIST) {
		return ErrUnsafePath
	}
	return ErrStore
}

func nativeRenameReplace(
	directory *os.File,
	_ *os.Root,
	_ *os.File,
	oldName string,
	newName string,
) error {
	if directory == nil {
		return ErrStore
	}
	if err := unix.Renameat(
		int(directory.Fd()), oldName,
		int(directory.Fd()), newName,
	); err != nil {
		return ErrStore
	}
	return nil
}

func nativeSyncPrivateDirectory(directory *os.File) error {
	if directory == nil || unix.Fsync(int(directory.Fd())) != nil {
		return ErrStore
	}
	return nil
}
