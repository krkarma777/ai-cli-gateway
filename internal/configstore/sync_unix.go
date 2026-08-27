//go:build !windows

package configstore

import (
	"os"

	"golang.org/x/sys/unix"
)

const supportsConfigDirectorySync = true

func nativeSyncConfigDirectory(directory *os.File) error {
	if directory == nil || unix.Fsync(int(directory.Fd())) != nil {
		return ErrStore
	}
	return nil
}
