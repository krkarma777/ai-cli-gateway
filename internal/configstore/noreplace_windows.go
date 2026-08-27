//go:build windows

package configstore

import (
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsKeyRenameInformation struct {
	replaceIfExists uint32
	rootDirectory   windows.Handle
	fileNameLength  uint32
	fileName        [1]uint16
}

func createNativePrivateFile(directory *os.File, _ *os.Root, name string) (*os.File, error) {
	if directory == nil {
		return nil, ErrStore
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, ErrUnsafePath
	}
	_, descriptor, err := windowsStoreSecurityAttributes(false)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      windows.Handle(directory.Fd()),
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	var (
		handle windows.Handle
		status windows.IO_STATUS_BLOCK
	)
	err = windows.NtCreateFile(
		&handle,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|
			windows.DELETE|windows.SYNCHRONIZE,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) ||
			errors.Is(err, windows.ERROR_FILE_EXISTS) ||
			errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, ErrUnsafePath
		}
		return nil, ErrStore
	}
	file := os.NewFile(uintptr(handle), "configstore-key-temp")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, ErrStore
	}
	return file, nil
}

func nativeRenameNoReplace(
	directory *os.File,
	root *os.Root,
	source *os.File,
	oldName string,
	newName string,
) error {
	return nativeRenameWindows(directory, root, source, oldName, newName, false)
}

func nativeRenameReplace(
	directory *os.File,
	root *os.Root,
	source *os.File,
	oldName string,
	newName string,
) error {
	return nativeRenameWindows(directory, root, source, oldName, newName, true)
}

func nativeRenameWindows(
	directory *os.File,
	_ *os.Root,
	source *os.File,
	_ string,
	newName string,
	replace bool,
) error {
	if directory == nil || source == nil {
		return ErrStore
	}
	newNameUTF16, err := windows.UTF16FromString(newName)
	if err != nil {
		return ErrUnsafePath
	}
	nameBytes := (len(newNameUTF16) - 1) * 2
	var layout windowsKeyRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.fileName)) + nameBytes
	buffer := make([]byte, bufferSize)
	information := (*windowsKeyRenameInformation)(unsafe.Pointer(&buffer[0]))
	if replace {
		information.replaceIfExists = 1
	}
	information.rootDirectory = windows.Handle(directory.Fd())
	information.fileNameLength = uint32(nameBytes)
	copy(
		unsafe.Slice(&information.fileName[0], nameBytes/2),
		newNameUTF16[:len(newNameUTF16)-1],
	)
	var status windows.IO_STATUS_BLOCK
	err = windows.NtSetInformationFile(
		windows.Handle(source.Fd()),
		&status,
		&buffer[0],
		uint32(bufferSize),
		windows.FileRenameInformation,
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) ||
		errors.Is(err, windows.ERROR_FILE_EXISTS) ||
		errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return ErrUnsafePath
	}
	return ErrStore
}

func nativeSyncPrivateDirectory(_ *os.File) error {
	return nil
}
