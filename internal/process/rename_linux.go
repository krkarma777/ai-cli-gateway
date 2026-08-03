//go:build linux

package process

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameRuntimeNoReplace(
	rootDir *os.File,
	_ *os.Root,
	oldName, newName string,
) error {
	return unix.Renameat2(
		int(rootDir.Fd()),
		oldName,
		int(rootDir.Fd()),
		newName,
		unix.RENAME_NOREPLACE,
	)
}
