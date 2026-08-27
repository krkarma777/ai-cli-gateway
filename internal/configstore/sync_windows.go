//go:build windows

package configstore

import "os"

const supportsConfigDirectorySync = false

func nativeSyncConfigDirectory(_ *os.File) error {
	return nil
}
