//go:build !windows && !darwin && !linux

package process

import (
	"errors"
	"os"
)

func renameRuntimeNoReplace(
	_ *os.File,
	_ *os.Root,
	_, _ string,
) error {
	return errors.New("atomic no-replace runtime rename is unsupported")
}
