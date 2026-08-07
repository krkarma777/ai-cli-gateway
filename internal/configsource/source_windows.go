//go:build windows

package configsource

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openSourceFile(path string) (*os.File, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		path16,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), "configuration-source")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, ErrUnavailable
	}
	if !validWindowsSourceHandle(handle) {
		_ = file.Close()
		return nil, ErrUnavailable
	}
	return file, nil
}

func platformSourceStable(path string, file *os.File) bool {
	if file == nil {
		return false
	}
	handle := windows.Handle(file.Fd())
	if !validWindowsSourceHandle(handle) {
		return false
	}
	var before windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &before); err != nil {
		return false
	}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	var selected windows.Win32FileAttributeData
	if err := windows.GetFileAttributesEx(
		path16,
		windows.GetFileExInfoStandard,
		(*byte)(unsafe.Pointer(&selected)),
	); err != nil || selected.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		selected.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return false
	}
	var after windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(handle, &after) == nil &&
		sameWindowsSourceIdentity(before, after)
}

func validWindowsSourceHandle(handle windows.Handle) bool {
	if handle == 0 || handle == windows.InvalidHandle {
		return false
	}
	typeValue, err := windows.GetFileType(handle)
	if err != nil || typeValue != windows.FILE_TYPE_DISK {
		return false
	}
	var information windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(handle, &information) == nil &&
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 &&
		information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0
}

func sameWindowsSourceIdentity(
	left windows.ByHandleFileInformation,
	right windows.ByHandleFileInformation,
) bool {
	return left.VolumeSerialNumber == right.VolumeSerialNumber &&
		left.FileIndexHigh == right.FileIndexHigh &&
		left.FileIndexLow == right.FileIndexLow
}
