//go:build windows

package doctor

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errWindowsPathEvidence = errors.New("unsafe Windows path evidence")

func validateEntrypointPath(path string) (validatedPath, pathDisposition) {
	return validatePlatformPath(path, pathKindEntrypoint)
}

type windowsDescriptorEvidence struct {
	Supported   bool
	DACLPresent bool
	DACLNull    bool
	OwnerSID    string
	ACEs        []aclACE
}

func validatePlatformPath(
	path string,
	kind pathKind,
) (validatedPath, pathDisposition) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	clean, _, err := normalizeWindowsInputPath(path)
	if err != nil || !windowsLeafShapeAllowed(kind, clean) {
		return validatedPath{}, pathUnsafe
	}
	leafPolicy, ancestorPolicy, ok := windowsPoliciesForPathKind(kind)
	if !ok {
		return validatedPath{}, pathUnsafe
	}
	token, err := acquireWindowsTokenSnapshot()
	if err != nil {
		return validatedPath{}, pathUnsafe
	}

	if err := validateWindowsAncestorChain(
		clean,
		ancestorPolicy,
		token,
	); err != nil {
		return validatedPath{}, classifyWindowsPathError(err)
	}

	leaf, info, err := acquireWindowsPathSnapshot(clean, token)
	if err != nil {
		return validatedPath{}, classifyWindowsPathError(err)
	}
	if err := evaluateWindowsACL(leaf, leafPolicy); err != nil {
		return validatedPath{}, pathUnsafe
	}
	if !windowsLeafShapeAllowed(kind, leaf.CanonicalPath) {
		return validatedPath{}, pathUnsafe
	}

	if err := validateWindowsAncestorChain(
		leaf.CanonicalPath,
		ancestorPolicy,
		token,
	); err != nil {
		return validatedPath{}, pathUnsafe
	}
	key, err := windowsPathCaseKey(leaf.CanonicalPath)
	if err != nil {
		return validatedPath{}, pathUnsafe
	}
	return validatedPath{
		Clean:        clean,
		Resolved:     leaf.CanonicalPath,
		CanonicalKey: key,
		Info:         info,
	}, pathSafe
}

func validateWindowsAncestorChain(
	path string,
	policy windowsACLPolicy,
	token aclTokenSnapshot,
) error {
	ancestors, err := windowsAncestorPaths(path)
	if err != nil {
		return errWindowsPathEvidence
	}
	for _, ancestor := range ancestors {
		snapshot, _, err := acquireWindowsPathSnapshot(ancestor, token)
		if err != nil {
			return err
		}
		if err := evaluateWindowsACL(snapshot, policy); err != nil {
			return errWindowsPathEvidence
		}
	}
	return nil
}

func acquireWindowsPathSnapshot(
	path string,
	token aclTokenSnapshot,
) (windowsACLSnapshot, fs.FileInfo, error) {
	first, err := openWindowsPathHandle(path)
	if err != nil {
		return windowsACLSnapshot{}, nil, err
	}
	defer first.Close() //nolint:errcheck // Read-only diagnostic handle.

	firstStat, err := first.Stat()
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}
	firstNative, err := windowsFileInformation(first)
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}
	firstObject, firstReparse, firstID := normalizeWindowsFileEvidence(
		firstNative,
	)
	finalRaw, err := windowsFinalPath(first)
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}
	canonical, canonicalKey, err := normalizeWindowsFinalPath(finalRaw)
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}

	second, err := openWindowsPathHandle(canonical)
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}
	defer second.Close() //nolint:errcheck // Read-only diagnostic handle.
	secondStat, err := second.Stat()
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}
	secondNative, err := windowsFileInformation(second)
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}
	secondObject, secondReparse, secondID := normalizeWindowsFileEvidence(
		secondNative,
	)
	secondFinalRaw, err := windowsFinalPath(second)
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}
	secondCanonical, secondKey, err := normalizeWindowsFinalPath(secondFinalRaw)
	if err != nil || secondKey != canonicalKey || secondCanonical != canonical ||
		firstObject != secondObject || !os.SameFile(firstStat, secondStat) {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}

	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(second.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}
	descriptorEvidence, err := normalizeWindowsSecurityDescriptor(descriptor)
	if err != nil {
		return windowsACLSnapshot{}, nil, errWindowsPathEvidence
	}

	return windowsACLSnapshot{
		DescriptorSupported: descriptorEvidence.Supported,
		DACLPresent:         descriptorEvidence.DACLPresent,
		DACLNull:            descriptorEvidence.DACLNull,
		Object:              secondObject,
		OwnerSID:            descriptorEvidence.OwnerSID,
		Token:               cloneWindowsTokenSnapshot(token),
		ACEs:                append([]aclACE(nil), descriptorEvidence.ACEs...),
		Reparse:             firstReparse || secondReparse,
		OpenedID:            firstID,
		ReopenedID:          secondID,
		CanonicalPath:       canonical,
	}, secondStat, nil
}

func openWindowsPathHandle(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, errWindowsPathEvidence
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), "validated-windows-path")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errWindowsPathEvidence
	}
	return file, nil
}

func windowsFileInformation(
	file *os.File,
) (windows.ByHandleFileInformation, error) {
	if file == nil {
		return windows.ByHandleFileInformation{}, errWindowsPathEvidence
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(
		windows.Handle(file.Fd()),
		&information,
	); err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	return information, nil
}

func windowsFinalPath(file *os.File) (string, error) {
	if file == nil {
		return "", errWindowsPathEvidence
	}
	size := uint32(windows.MAX_PATH)
	for {
		buffer := make([]uint16, size)
		length, err := windows.GetFinalPathNameByHandle(
			windows.Handle(file.Fd()),
			&buffer[0],
			size,
			0,
		)
		if err != nil {
			return "", err
		}
		if length == 0 {
			return "", errWindowsPathEvidence
		}
		if length < size {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		if length >= 1<<15 {
			return "", errWindowsPathEvidence
		}
		size = length + 1
	}
}

func normalizeWindowsInputPath(path string) (string, string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", "", errWindowsPathEvidence
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || windowsDeviceNamespace(clean) {
		return "", "", errWindowsPathEvidence
	}
	key, err := windowsPathCaseKey(clean)
	if err != nil {
		return "", "", errWindowsPathEvidence
	}
	return clean, key, nil
}

func normalizeWindowsFinalPath(path string) (string, string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", "", errWindowsPathEvidence
	}
	normalizedSeparators := strings.ReplaceAll(path, "/", `\`)
	lower := strings.ToLower(normalizedSeparators)
	const (
		extendedPrefix    = `\\?\`
		extendedUNCPrefix = `\\?\unc\`
	)
	switch {
	case strings.HasPrefix(lower, extendedUNCPrefix):
		normalizedSeparators = `\\` + normalizedSeparators[len(extendedUNCPrefix):]
	case strings.HasPrefix(lower, extendedPrefix):
		normalizedSeparators = normalizedSeparators[len(extendedPrefix):]
	case windowsDeviceNamespace(normalizedSeparators):
		return "", "", errWindowsPathEvidence
	}
	clean := filepath.Clean(normalizedSeparators)
	if !filepath.IsAbs(clean) || windowsDeviceNamespace(clean) {
		return "", "", errWindowsPathEvidence
	}
	key, err := windowsPathCaseKey(clean)
	if err != nil {
		return "", "", errWindowsPathEvidence
	}
	return clean, key, nil
}

func windowsDeviceNamespace(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	return strings.HasPrefix(normalized, `\\?\`) ||
		strings.HasPrefix(normalized, `\\.\`) ||
		strings.HasPrefix(normalized, `\??\`) ||
		strings.HasPrefix(normalized, `\\??\`) ||
		strings.HasPrefix(normalized, `\device\`)
}

func windowsAncestorPaths(path string) ([]string, error) {
	clean, cleanKey, err := normalizeWindowsInputPath(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return nil, errWindowsPathEvidence
	}
	root := filepath.Clean(volume + string(filepath.Separator))
	rootKey, err := windowsPathCaseKey(root)
	if err != nil {
		return nil, errWindowsPathEvidence
	}
	if cleanKey == rootKey {
		return nil, nil
	}

	current := filepath.Dir(clean)
	reversed := make([]string, 0, 8)
	for {
		currentKey, err := windowsPathCaseKey(current)
		if err != nil {
			return nil, errWindowsPathEvidence
		}
		reversed = append(reversed, current)
		if currentKey == rootKey {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, errWindowsPathEvidence
		}
		current = parent
	}
	ancestors := make([]string, len(reversed))
	for index := range reversed {
		ancestors[index] = reversed[len(reversed)-1-index]
	}
	return ancestors, nil
}

func normalizeWindowsFileEvidence(
	information windows.ByHandleFileInformation,
) (aclObject, bool, windowsFileID) {
	reparse := information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	identity := windowsFileID{
		Volume: information.VolumeSerialNumber,
		Index: uint64(information.FileIndexHigh)<<32 |
			uint64(information.FileIndexLow),
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DEVICE != 0 {
		return aclObjectUnknown, reparse, identity
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return aclObjectDirectory, reparse, identity
	}
	return aclObjectFile, reparse, identity
}

func normalizeWindowsSecurityDescriptor(
	descriptor *windows.SECURITY_DESCRIPTOR,
) (windowsDescriptorEvidence, error) {
	evidence := windowsDescriptorEvidence{}
	if descriptor == nil || !descriptor.IsValid() {
		return evidence, nil
	}
	evidence.Supported = true
	control, _, err := descriptor.Control()
	if err != nil {
		return windowsDescriptorEvidence{}, err
	}
	evidence.DACLPresent = control&windows.SE_DACL_PRESENT != 0
	owner, _, err := descriptor.Owner()
	if err != nil {
		return windowsDescriptorEvidence{}, err
	}
	if owner == nil || !owner.IsValid() {
		evidence.Supported = false
	} else {
		evidence.OwnerSID = owner.String()
		if evidence.OwnerSID == "" {
			evidence.Supported = false
		}
	}
	if !evidence.DACLPresent {
		return evidence, nil
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return windowsDescriptorEvidence{}, err
	}
	if dacl == nil {
		evidence.DACLNull = true
		return evidence, nil
	}
	evidence.ACEs = make([]aclACE, 0, dacl.AceCount)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var nativeACE *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &nativeACE); err != nil {
			return windowsDescriptorEvidence{}, err
		}
		ace, ok := normalizeWindowsACE(nativeACE)
		if !ok {
			evidence.Supported = false
			return evidence, nil
		}
		evidence.ACEs = append(evidence.ACEs, ace)
	}
	return evidence, nil
}

func normalizeWindowsACE(native *windows.ACCESS_ALLOWED_ACE) (aclACE, bool) {
	if native == nil {
		return aclACE{}, false
	}
	kind := aclACEUnknown
	switch native.Header.AceType {
	case windows.ACCESS_ALLOWED_ACE_TYPE:
		kind = aclACEAllow
	case windows.ACCESS_DENIED_ACE_TYPE:
		kind = aclACEDeny
	default:
		return aclACE{}, false
	}
	const minimumSIDBytes = uintptr(8)
	sidOffset := unsafe.Offsetof(native.SidStart)
	if uintptr(native.Header.AceSize) < sidOffset+minimumSIDBytes {
		return aclACE{}, false
	}
	sid := (*windows.SID)(unsafe.Pointer(&native.SidStart))
	if !sid.IsValid() || uintptr(sid.Len()) > uintptr(native.Header.AceSize)-sidOffset {
		return aclACE{}, false
	}
	sidString := sid.String()
	if sidString == "" {
		return aclACE{}, false
	}
	return aclACE{
		Kind:  kind,
		Flags: native.Header.AceFlags,
		Mask:  aclMask(native.Mask),
		SID:   sidString,
	}, true
}

func acquireWindowsTokenSnapshot() (aclTokenSnapshot, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	token := windows.GetCurrentThreadEffectiveToken()
	restricted, err := token.IsRestricted()
	if err != nil || restricted {
		return aclTokenSnapshot{}, errWindowsPathEvidence
	}
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil ||
		!user.User.Sid.IsValid() {
		return aclTokenSnapshot{}, errWindowsPathEvidence
	}
	userSID := user.User.Sid.String()
	if userSID == "" {
		return aclTokenSnapshot{}, errWindowsPathEvidence
	}
	groups, err := token.GetTokenGroups()
	if err != nil || groups == nil {
		return aclTokenSnapshot{}, errWindowsPathEvidence
	}
	snapshot := aclTokenSnapshot{
		Supported: true,
		UserSID:   userSID,
		Groups:    make([]aclTokenGroup, 0, groups.GroupCount),
	}
	for _, group := range groups.AllGroups() {
		if group.Sid == nil || !group.Sid.IsValid() {
			return aclTokenSnapshot{}, errWindowsPathEvidence
		}
		sid := group.Sid.String()
		if sid == "" {
			return aclTokenSnapshot{}, errWindowsPathEvidence
		}
		snapshot.Groups = append(snapshot.Groups, aclTokenGroup{
			SID:      sid,
			Enabled:  group.Attributes&windows.SE_GROUP_ENABLED != 0,
			DenyOnly: group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY != 0,
		})
	}
	return snapshot, nil
}

func cloneWindowsTokenSnapshot(token aclTokenSnapshot) aclTokenSnapshot {
	cloned := token
	cloned.Groups = append([]aclTokenGroup(nil), token.Groups...)
	return cloned
}

func normalizeWindowsSystemDirectories(
	root string,
	system string,
) (string, string, error) {
	cleanRoot, rootKey, err := normalizeWindowsInputPath(root)
	if err != nil {
		return "", "", errWindowsPathEvidence
	}
	cleanSystem, _, err := normalizeWindowsInputPath(system)
	if err != nil || !strings.EqualFold(filepath.Base(cleanSystem), "System32") {
		return "", "", errWindowsPathEvidence
	}
	parentKey, err := windowsPathCaseKey(filepath.Dir(cleanSystem))
	if err != nil || parentKey != rootKey {
		return "", "", errWindowsPathEvidence
	}
	return cleanRoot, cleanSystem, nil
}

func platformPathDefaults() (platformDefaults, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rootFromAPI, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return platformDefaults{}, errPathUnsafe
	}
	systemFromAPI, err := windows.GetSystemDirectory()
	if err != nil {
		return platformDefaults{}, errPathUnsafe
	}
	root, system, err := normalizeWindowsSystemDirectories(
		rootFromAPI,
		systemFromAPI,
	)
	if err != nil {
		return platformDefaults{}, errPathUnsafe
	}
	validatedRoot, disposition := validateSafeDirectoryPath(root)
	if disposition != pathSafe {
		return platformDefaults{}, errPathUnsafe
	}
	validatedSystem, disposition := validateSafeDirectoryPath(system)
	if disposition != pathSafe {
		return platformDefaults{}, errPathUnsafe
	}
	resolvedParentKey, err := windowsPathCaseKey(
		filepath.Dir(validatedSystem.Resolved),
	)
	if err != nil || resolvedParentKey != validatedRoot.CanonicalKey {
		return platformDefaults{}, errPathUnsafe
	}
	return platformDefaults{
		SafePathTail:     []validatedPath{validatedSystem},
		FrozenSystemRoot: validatedRoot.Resolved,
	}, nil
}

func classifyWindowsPathError(err error) pathDisposition {
	if errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return pathMissing
	}
	return pathUnsafe
}
