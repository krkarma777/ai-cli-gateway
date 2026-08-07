//go:build !windows

package configsource

import (
	"os"

	"golang.org/x/sys/unix"
)

func openSourceFile(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "configuration-source")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrUnavailable
	}
	return file, nil
}

func platformSourceStable(path string, file *os.File) bool {
	if file == nil {
		return false
	}
	var handle unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &handle); err != nil ||
		uint32(handle.Mode)&unix.S_IFMT != unix.S_IFREG {
		return false
	}
	var selected unix.Stat_t
	return unix.Lstat(path, &selected) == nil &&
		uint32(selected.Mode)&unix.S_IFMT == unix.S_IFREG &&
		uint64(handle.Dev) == uint64(selected.Dev) &&
		uint64(handle.Ino) == uint64(selected.Ino)
}
