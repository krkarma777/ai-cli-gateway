//go:build !windows

package testutil

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockRepositoryScanFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func unlockRepositoryScanFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
