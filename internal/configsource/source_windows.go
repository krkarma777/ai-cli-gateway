//go:build windows

package configsource

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsCreateFile func(
	*uint16,
	uint32,
	uint32,
	*windows.SecurityAttributes,
	uint32,
	uint32,
	windows.Handle,
) (windows.Handle, error)

type windowsSourceBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	reserved       uint32
}

type sourceMetadata struct {
	volume        uint32
	index         uint64
	attributes    uint32
	nlink         uint32
	size          int64
	creationTime  int64
	lastWriteTime int64
	changeTime    int64
}

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

func platformSourceMetadata(path string, file *os.File) (sourceMetadata, bool) {
	return platformSourceMetadataWithOpen(path, file, windows.CreateFile)
}

func platformSourceMetadataWithOpen(
	path string,
	file *os.File,
	create windowsCreateFile,
) (sourceMetadata, bool) {
	if file == nil || create == nil {
		return sourceMetadata{}, false
	}
	retainedBefore, ok := windowsSourceFileMetadata(file)
	if !ok {
		return sourceMetadata{}, false
	}
	selectedFile, err := openWindowsSourcePathWith(path, create)
	if err != nil || selectedFile == nil {
		if selectedFile != nil {
			_ = selectedFile.Close()
		}
		return sourceMetadata{}, false
	}
	selected, selectedOK := windowsSourceFileMetadata(selectedFile)
	closeErr := selectedFile.Close()
	retainedAfter, retainedOK := windowsSourceFileMetadata(file)
	if !selectedOK || closeErr != nil || !retainedOK ||
		!sameSourceMetadata(retainedBefore, selected) ||
		!sameSourceMetadata(retainedBefore, retainedAfter) {
		return sourceMetadata{}, false
	}
	return retainedBefore, true
}

func openWindowsSourcePathWith(
	path string,
	create windowsCreateFile,
) (*os.File, error) {
	if create == nil {
		return nil, ErrUnavailable
	}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := create(
		path16,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), "configuration-source-path")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, ErrUnavailable
	}
	return file, nil
}

func windowsSourceFileMetadata(file *os.File) (sourceMetadata, bool) {
	if file == nil {
		return sourceMetadata{}, false
	}
	handle := windows.Handle(file.Fd())
	typeValue, err := windows.GetFileType(handle)
	if err != nil {
		return sourceMetadata{}, false
	}
	var native windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &native); err != nil {
		return sourceMetadata{}, false
	}
	var basic windowsSourceBasicInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)),
		uint32(unsafe.Sizeof(basic)), //nolint:gosec // Windows buffer sizes fit uint32.
	); err != nil {
		return sourceMetadata{}, false
	}
	return windowsSourceMetadataFromEvidence(typeValue, native, basic)
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

func windowsSourceMetadataFromEvidence(
	typeValue uint32,
	native windows.ByHandleFileInformation,
	basic windowsSourceBasicInfo,
) (sourceMetadata, bool) {
	const unsafeAttributes = windows.FILE_ATTRIBUTE_REPARSE_POINT |
		windows.FILE_ATTRIBUTE_DIRECTORY |
		windows.FILE_ATTRIBUTE_DEVICE
	if typeValue != windows.FILE_TYPE_DISK ||
		native.FileAttributes&unsafeAttributes != 0 ||
		basic.FileAttributes&unsafeAttributes != 0 ||
		native.FileAttributes != basic.FileAttributes ||
		native.NumberOfLinks == 0 {
		return sourceMetadata{}, false
	}
	identity := windowsSourceIdentity(native)
	if identity.volume == 0 && identity.index == 0 {
		return sourceMetadata{}, false
	}
	creationTime := windowsSourceFiletime(native.CreationTime)
	lastWriteTime := windowsSourceFiletime(native.LastWriteTime)
	if creationTime != basic.CreationTime ||
		lastWriteTime != basic.LastWriteTime ||
		basic.ChangeTime == 0 {
		return sourceMetadata{}, false
	}
	return sourceMetadata{
		volume: identity.volume, index: identity.index,
		attributes: native.FileAttributes, nlink: native.NumberOfLinks,
		size:          int64(uint64(native.FileSizeHigh)<<32 | uint64(native.FileSizeLow)),
		creationTime:  creationTime,
		lastWriteTime: lastWriteTime, changeTime: basic.ChangeTime,
	}, true
}

type windowsSourceFileIdentity struct {
	volume uint32
	index  uint64
}

func windowsSourceIdentity(information windows.ByHandleFileInformation) windowsSourceFileIdentity {
	return windowsSourceFileIdentity{
		volume: information.VolumeSerialNumber,
		index: uint64(information.FileIndexHigh)<<32 |
			uint64(information.FileIndexLow),
	}
}

func windowsSourceFiletime(value windows.Filetime) int64 {
	return int64(uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)) //nolint:gosec // FILETIME is a signed LARGE_INTEGER value.
}

func sameSourceMetadata(left, right sourceMetadata) bool {
	return left == right
}

func sameWindowsSourceIdentity(
	left windows.ByHandleFileInformation,
	right windows.ByHandleFileInformation,
) bool {
	return left.VolumeSerialNumber == right.VolumeSerialNumber &&
		left.FileIndexHigh == right.FileIndexHigh &&
		left.FileIndexLow == right.FileIndexLow
}
