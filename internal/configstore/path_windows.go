//go:build windows

package configstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsStoreSystemSID           = "S-1-5-18"
	windowsStoreAdministratorsSID   = "S-1-5-32-544"
	windowsStoreTrustedInstallerSID = "S-1-5-80-956008885-3418522649-" +
		"1831038044-1853292631-2271478464"
)

const windowsStoreUnsafePrivateGrant uint32 = windows.DELETE | windows.WRITE_DAC |
	windows.WRITE_OWNER | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
	windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | 0x00000040 | // FILE_DELETE_CHILD is not exported by x/sys/windows.
	windows.GENERIC_WRITE | windows.GENERIC_ALL

const windowsStoreUnsafeAncestorGrant uint32 = windows.DELETE | windows.WRITE_DAC |
	windows.WRITE_OWNER | 0x00000040 | // FILE_DELETE_CHILD is not exported by x/sys/windows.
	windows.GENERIC_ALL

type nativeFileMetadata struct {
	volume        uint32
	index         uint64
	attributes    uint32
	nlink         uint32
	size          int64
	creationTime  int64
	lastWriteTime int64
}

type nativeDirectoryEvidence struct {
	path     string
	metadata nativeFileMetadata
}

func sameNativeDirectoryIdentity(left nativeFileMetadata, right nativeFileMetadata) bool {
	return left.volume == right.volume && left.index == right.index
}

func openNativeLoadTarget(input string) (nativeLoadTarget, error) {
	path, ok := canonicalWindowsStorePath(input)
	if !ok {
		return nativeLoadTarget{}, ErrUnsafePath
	}
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		parent, missing, inspectErr := inspectWindowsMissingPath(path)
		if inspectErr != nil {
			return nativeLoadTarget{}, inspectErr
		}
		return nativeLoadTarget{path: path, parent: parent, missing: missing}, nil
	}
	if err != nil {
		return nativeLoadTarget{}, ErrStore
	}
	file, err := openWindowsStorePath(path, false)
	if err != nil {
		return nativeLoadTarget{}, err
	}
	finalPath, finalOK := windowsStoreFinalPath(file)
	if !finalOK || !windowsStoreLongPathMatches(finalPath, path) {
		_ = file.Close()
		return nativeLoadTarget{}, ErrUnsafePath
	}
	parent, err := openWindowsStoreDirectory(filepath.Dir(finalPath), true)
	if err != nil {
		_ = file.Close()
		return nativeLoadTarget{}, err
	}
	metadata, ok := windowsStoreMetadata(file, false)
	if !ok || !safeWindowsStoreSecurity(file, false, true) ||
		!revalidateWindowsDirectory(parent, true) || !safeWindowsStoreAncestors(finalPath) {
		_ = file.Close()
		return nativeLoadTarget{}, ErrUnsafePath
	}
	return nativeLoadTarget{
		path: finalPath, exists: true, file: file, metadata: metadata, parent: parent,
	}, nil
}

func revalidateNativeLoadTarget(target nativeLoadTarget) bool {
	if target.path == "" || target.parent.path == "" {
		return false
	}
	if !target.exists {
		for _, path := range target.missing {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
		privateParent := strings.EqualFold(target.parent.path, filepath.Dir(target.path))
		return revalidateWindowsDirectory(target.parent, privateParent) &&
			safeWindowsStoreAncestorsFrom(target.parent.path, privateParent)
	}
	if target.file == nil || !revalidateWindowsDirectory(target.parent, true) ||
		!safeWindowsStoreAncestors(target.path) {
		return false
	}
	metadata, ok := windowsStoreMetadata(target.file, false)
	return ok && metadata == target.metadata &&
		safeWindowsStoreSecurity(target.file, false, true) &&
		windowsStoreFinalPathMatches(target.file, target.path)
}

func cleanWindowsStorePath(path string) (string, bool) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || windowsStoreDevicePath(path) ||
		strings.HasPrefix(path, `\\`) {
		return "", false
	}
	clean := filepath.Clean(path)
	if clean != path || !filepath.IsAbs(clean) || windowsStoreDevicePath(clean) {
		return "", false
	}
	volume := filepath.VolumeName(clean)
	if len(volume) != 2 || volume[1] != ':' || strings.Contains(clean[len(volume):], ":") {
		return "", false
	}
	drive := volume[0]
	if !((drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')) {
		return "", false
	}
	maxComponent, volumeOK := windowsStoreVolumePolicy(volume)
	if !volumeOK {
		return "", false
	}
	for _, component := range strings.Split(strings.TrimPrefix(clean[len(volume):], `\`), `\`) {
		if !safeWindowsStoreComponent(component) ||
			windowsStoreComponentUTF16Length(component) > int(maxComponent) {
			return "", false
		}
	}
	return clean, true
}

func nativeStorePathKey(path string) (string, bool) {
	clean, ok := canonicalWindowsStorePath(path)
	if !ok {
		return "", false
	}
	return strings.ToLower(clean), true
}

func canonicalWindowsStorePath(path string) (string, bool) {
	current, ok := cleanWindowsStorePath(path)
	if !ok {
		return "", false
	}
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			long, longOK := longWindowsStorePath(current)
			if !longOK {
				return "", false
			}
			canonical, cleanOK := cleanWindowsStorePath(
				strings.ReplaceAll(long, "/", `\`),
			)
			if !cleanOK {
				return "", false
			}
			for index := len(missing) - 1; index >= 0; index-- {
				canonical = filepath.Join(canonical, missing[index])
			}
			return canonical, true
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func inspectNativePrivateDirectory(path string) (bool, error) {
	clean, ok := cleanWindowsStorePath(path)
	if !ok {
		return false, ErrUnsafePath
	}
	_, err := os.Lstat(clean)
	if err == nil {
		evidence, openErr := openWindowsStoreDirectory(clean, true)
		if openErr != nil {
			return false, openErr
		}
		if !revalidateWindowsDirectory(evidence, true) ||
			!safeWindowsStoreAncestorsFrom(clean, true) {
			return false, ErrUnsafePath
		}
		return true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, ErrStore
	}
	probe := filepath.Join(clean, ".configstore-probe")
	evidence, missing, inspectErr := inspectWindowsMissingPath(probe)
	if inspectErr != nil {
		return false, inspectErr
	}
	target := nativeLoadTarget{path: probe, parent: evidence, missing: missing}
	if !revalidateNativeLoadTarget(target) {
		return false, ErrUnsafePath
	}
	return false, nil
}

func windowsStoreDevicePath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	return strings.HasPrefix(normalized, `\\?\`) ||
		strings.HasPrefix(normalized, `\\.\`) ||
		strings.HasPrefix(normalized, `\??\`) ||
		strings.HasPrefix(normalized, `\\??\`) ||
		strings.HasPrefix(normalized, `\device\`)
}

func windowsStoreVolumePolicy(volume string) (uint32, bool) {
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil || windows.GetDriveType(root) != windows.DRIVE_FIXED {
		return 0, false
	}
	var (
		serial       uint32
		maxComponent uint32
		flags        uint32
	)
	if err := windows.GetVolumeInformation(
		root, nil, 0, &serial, &maxComponent, &flags, nil, 0,
	); err != nil || maxComponent == 0 || flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return 0, false
	}
	return maxComponent, true
}

func inspectWindowsMissingPath(path string) (nativeDirectoryEvidence, []string, error) {
	missing := []string{path}
	current := filepath.Dir(path)
	private := true
	for {
		_, err := os.Lstat(current)
		if err == nil {
			evidence, openErr := openWindowsStoreDirectory(current, private)
			if openErr != nil {
				return nativeDirectoryEvidence{}, nil, openErr
			}
			if !safeWindowsStoreAncestorsFrom(evidence.path, private) {
				return nativeDirectoryEvidence{}, nil, ErrUnsafePath
			}
			return evidence, missing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nativeDirectoryEvidence{}, nil, ErrStore
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return nativeDirectoryEvidence{}, nil, ErrUnsafePath
		}
		current = parent
		private = false
	}
}

func openWindowsStoreDirectory(path string, private bool) (nativeDirectoryEvidence, error) {
	file, err := openWindowsStorePath(path, true)
	if err != nil {
		return nativeDirectoryEvidence{}, err
	}
	defer func() { _ = file.Close() }()
	finalPath, finalOK := windowsStoreFinalPath(file)
	metadata, ok := windowsStoreMetadata(file, true)
	if !ok || !safeWindowsStoreSecurity(file, true, private) ||
		!finalOK || !windowsStoreLongPathMatches(finalPath, path) {
		return nativeDirectoryEvidence{}, ErrUnsafePath
	}
	return nativeDirectoryEvidence{path: finalPath, metadata: metadata}, nil
}

func revalidateWindowsDirectory(evidence nativeDirectoryEvidence, private bool) bool {
	file, err := openWindowsStorePath(evidence.path, true)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	metadata, ok := windowsStoreMetadata(file, true)
	return ok && metadata == evidence.metadata &&
		safeWindowsStoreSecurity(file, true, private) &&
		windowsStoreFinalPathMatches(file, evidence.path)
}

func safeWindowsStoreAncestors(path string) bool {
	return safeWindowsStoreAncestorsFrom(filepath.Dir(path), true)
}

func safeWindowsStoreAncestorsFrom(start string, privateFirst bool) bool {
	volume := filepath.VolumeName(start)
	root := filepath.Clean(volume + `\`)
	current := start
	first := true
	for {
		file, err := openWindowsStorePath(current, true)
		if err != nil {
			return false
		}
		_, metadataOK := windowsStoreMetadata(file, true)
		safe := metadataOK && safeWindowsStoreSecurity(file, true, privateFirst && first) &&
			windowsStoreFinalPathMatches(file, current)
		closeErr := file.Close()
		if !safe || closeErr != nil {
			return false
		}
		if strings.EqualFold(current, root) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
		first = false
	}
}

func openWindowsStorePath(path string, directory bool) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrUnsafePath
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, ErrUnsafePath
		}
		return nil, ErrStore
	}
	file := os.NewFile(uintptr(handle), "configstore-path")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, ErrStore
	}
	return file, nil
}

func windowsStoreMetadata(file *os.File, directory bool) (nativeFileMetadata, bool) {
	if file == nil {
		return nativeFileMetadata{}, false
	}
	handle := windows.Handle(file.Fd())
	typeValue, err := windows.GetFileType(handle)
	if err != nil || typeValue != windows.FILE_TYPE_DISK {
		return nativeFileMetadata{}, false
	}
	var information windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &information) != nil ||
		information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 ||
		(information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory ||
		(!directory && information.NumberOfLinks != 1) {
		return nativeFileMetadata{}, false
	}
	index := uint64(information.FileIndexHigh)<<32 | uint64(information.FileIndexLow)
	if information.VolumeSerialNumber == 0 && index == 0 {
		return nativeFileMetadata{}, false
	}
	return nativeFileMetadata{
		volume:        information.VolumeSerialNumber,
		index:         index,
		attributes:    information.FileAttributes,
		nlink:         information.NumberOfLinks,
		size:          int64(uint64(information.FileSizeHigh)<<32 | uint64(information.FileSizeLow)),
		creationTime:  windowsStoreFiletime(information.CreationTime),
		lastWriteTime: windowsStoreFiletime(information.LastWriteTime),
	}, true
}

func safeWindowsStoreSecurity(file *os.File, directory bool, private bool) bool {
	if file == nil {
		return false
	}
	token := windows.GetCurrentThreadEffectiveToken()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return false
	}
	userSID := user.User.Sid.String()
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return false
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return false
	}
	ownerSID := owner.String()
	if private {
		if ownerSID != userSID {
			return false
		}
	} else if ownerSID != userSID && ownerSID != windowsStoreSystemSID &&
		ownerSID != windowsStoreAdministratorsSID && ownerSID != windowsStoreTrustedInstallerSID {
		return false
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 ||
		(private && control&windows.SE_DACL_PROTECTED == 0) {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return false
	}
	unsafeGrant := windowsStoreUnsafeAncestorGrant
	if private {
		unsafeGrant = windowsStoreUnsafePrivateGrant
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var native *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, index, &native) != nil || native == nil {
			return false
		}
		if native.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE &&
			native.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			return false
		}
		const minimumSIDBytes = uintptr(8)
		offset := unsafe.Offsetof(native.SidStart)
		if uintptr(native.Header.AceSize) < offset+minimumSIDBytes {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&native.SidStart))
		if !sid.IsValid() || uintptr(sid.Len()) > uintptr(native.Header.AceSize)-offset {
			return false
		}
		trusted := sid.String() == userSID || sid.String() == windowsStoreSystemSID ||
			sid.String() == windowsStoreAdministratorsSID || (!private && sid.String() == windowsStoreTrustedInstallerSID)
		if native.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && !trusted &&
			!safeWindowsStoreUntrustedACE(
				private,
				native.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0,
				uint32(native.Mask),
				unsafeGrant,
			) {
			return false
		}
	}
	_ = directory
	return true
}

func windowsStoreFinalPathMatches(file *os.File, selected string) bool {
	final, ok := windowsStoreFinalPath(file)
	return ok && windowsStoreLongPathMatches(final, selected)
}

func windowsStoreFinalPath(file *os.File) (string, bool) {
	if file == nil {
		return "", false
	}
	size := uint32(windows.MAX_PATH)
	for {
		buffer := make([]uint16, size)
		length, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], size, 0)
		if err != nil || length == 0 || length >= 1<<15 {
			return "", false
		}
		if length >= size {
			size = length + 1
			continue
		}
		final := strings.ReplaceAll(windows.UTF16ToString(buffer[:length]), "/", `\`)
		lower := strings.ToLower(final)
		if strings.HasPrefix(lower, `\\?\unc\`) {
			return "", false
		}
		if strings.HasPrefix(lower, `\\?\`) {
			final = final[4:]
		}
		return cleanWindowsStorePath(filepath.Clean(final))
	}
}

func windowsStoreLongPathMatches(final string, selected string) bool {
	selectedLong, ok := longWindowsStorePath(selected)
	if !ok {
		return false
	}
	selectedClean, ok := cleanWindowsStorePath(filepath.Clean(
		strings.ReplaceAll(selectedLong, "/", `\`),
	))
	return ok && strings.EqualFold(final, selectedClean)
}

func longWindowsStorePath(path string) (string, bool) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	size := uint32(windows.MAX_PATH)
	for {
		buffer := make([]uint16, size)
		length, err := windows.GetLongPathName(pointer, &buffer[0], size)
		if err != nil || length == 0 || length >= 1<<15 {
			return "", false
		}
		if length >= size {
			size = length + 1
			continue
		}
		return windows.UTF16ToString(buffer[:length]), true
	}
}

func windowsStoreFiletime(value windows.Filetime) int64 {
	return int64(uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)) //nolint:gosec // FILETIME uses a signed LARGE_INTEGER representation.
}
