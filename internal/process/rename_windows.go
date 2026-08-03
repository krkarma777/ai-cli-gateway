//go:build windows

package process

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileRenameInformation struct {
	replaceIfExists uint32
	rootDirectory   windows.Handle
	fileNameLength  uint32
	fileName        [1]uint16
}

func renameRuntimeNoReplace(
	rootDir *os.File,
	_ *os.Root,
	oldName, newName string,
) error {
	oldObjectName, err := windows.NewNTUnicodeString(oldName)
	if err != nil {
		return err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(rootDir.Fd()),
		ObjectName:    oldObjectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var (
		source windows.Handle
		status windows.IO_STATUS_BLOCK
	)
	err = windows.NtCreateFile(
		&source,
		windows.DELETE|windows.SYNCHRONIZE,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|
			windows.FILE_SHARE_WRITE|
			windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|
			windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_OPEN_FOR_BACKUP_INTENT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(source)

	newNameUTF16, err := windows.UTF16FromString(newName)
	if err != nil {
		return err
	}
	nameBytes := (len(newNameUTF16) - 1) * 2
	var layout windowsFileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.fileName)) + nameBytes
	buffer := make([]byte, bufferSize)
	info := (*windowsFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.rootDirectory = windows.Handle(rootDir.Fd())
	info.fileNameLength = uint32(nameBytes)
	copy(
		unsafe.Slice(&info.fileName[0], nameBytes/2),
		newNameUTF16[:len(newNameUTF16)-1],
	)
	return windows.NtSetInformationFile(
		source,
		&status,
		&buffer[0],
		uint32(bufferSize),
		windows.FileRenameInformation,
	)
}
