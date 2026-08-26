//go:build windows

package configstore

import (
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sealNativePrivateFile drops write sharing before the staged key is parsed.
// The retained read/delete handle keeps the same file identity available for
// validation and handle-relative publication while excluding concurrent writers.
func sealNativePrivateFile(
	directory *os.File,
	file *os.File,
	name string,
) (*os.File, error) {
	if directory == nil || file == nil || name == "" || filepath.Base(name) != name {
		return nil, ErrStore
	}
	if err := file.Close(); err != nil {
		return nil, ErrStore
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, ErrUnsafePath
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(directory.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var (
		handle windows.Handle
		status windows.IO_STATUS_BLOCK
	)
	err = windows.NtCreateFile(
		&handle,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.DELETE|windows.SYNCHRONIZE,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	runtime.KeepAlive(directory)
	if err != nil {
		return nil, ErrStore
	}
	sealed := os.NewFile(uintptr(handle), "configstore-key-sealed")
	if sealed == nil {
		_ = windows.CloseHandle(handle)
		return nil, ErrStore
	}
	return sealed, nil
}
