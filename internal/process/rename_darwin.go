//go:build darwin

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
	return unix.RenameatxNp(
		int(rootDir.Fd()),
		oldName,
		int(rootDir.Fd()),
		newName,
		unix.RENAME_EXCL,
	)
}
