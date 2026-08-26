//go:build !windows

package configstore

import "os"

func sealNativePrivateFile(
	_ *os.File,
	file *os.File,
	_ string,
) (*os.File, error) {
	if file == nil {
		return nil, ErrStore
	}
	return file, nil
}
