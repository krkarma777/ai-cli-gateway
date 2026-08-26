//go:build windows

package gatewaykey

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsACLReadData       uint32 = 0x00000001
	windowsACLExecute        uint32 = 0x00000020
	windowsACLReadAttributes uint32 = 0x00000080

	windowsACLForbiddenAncestor uint32 = 0x000d0040
	windowsACLForbiddenLeaf     uint32 = 0x000d01ff
	windowsACLConcreteRights    uint32 = 0x001f01ff

	windowsACLGenericAll     uint32 = 0x10000000
	windowsACLGenericExecute uint32 = 0x20000000
	windowsACLGenericWrite   uint32 = 0x40000000
	windowsACLGenericRead    uint32 = 0x80000000
)

const (
	windowsACLACEObjectInherit    uint8 = 0x01
	windowsACLACEContainerInherit uint8 = 0x02
	windowsACLACENoPropagate      uint8 = 0x04
	windowsACLACEInheritOnly      uint8 = 0x08
	windowsACLACEInherited        uint8 = 0x10
	windowsACLACEValidFlags             = windowsACLACEObjectInherit |
		windowsACLACEContainerInherit |
		windowsACLACENoPropagate |
		windowsACLACEInheritOnly |
		windowsACLACEInherited
)

const (
	windowsLocalSystemSID           = "S-1-5-18"
	windowsBuiltinAdministratorsSID = "S-1-5-32-544"
	windowsTrustedInstallerSID      = "S-1-5-80-956008885-3418522649-" +
		"1831038044-1853292631-2271478464"
)

type windowsKeyIdentity struct {
	volume uint32
	index  uint64
}

type windowsKeyTokenGroup struct {
	sid      string
	enabled  bool
	denyOnly bool
}

type windowsKeyToken struct {
	userSID string
	groups  []windowsKeyTokenGroup
}

type windowsKeyACEKind uint8

const (
	windowsKeyACEUnknown windowsKeyACEKind = iota
	windowsKeyACEAllow
	windowsKeyACEDeny
)

type windowsKeyACE struct {
	kind  windowsKeyACEKind
	flags uint8
	mask  uint32
	sid   string
}

type windowsKeyACLEvidence struct {
	descriptorSupported bool
	daclPresent         bool
	daclNull            bool
	daclProtected       bool
	ownerSID            string
	aces                []windowsKeyACE
}

func loadFile(
	path string,
	distinctFrom []fs.FileInfo,
	parse snapshotParser,
) (Snapshot, error) {
	return loadFileWithWindowsPathIdentity(
		path,
		distinctFrom,
		parse,
		windowsKeyPathIdentity,
	)
}

type windowsPathIdentity func(string) (windowsKeyIdentity, bool)

func loadFileWithWindowsPathIdentity(
	path string,
	distinctFrom []fs.FileInfo,
	parse snapshotParser,
	pathIdentity windowsPathIdentity,
) (Snapshot, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	clean, ok := cleanWindowsKeyPath(path)
	if !ok || parse == nil || pathIdentity == nil {
		return Snapshot{}, ErrUnavailable
	}
	token, ok := currentWindowsKeyToken()
	if !ok || !safeWindowsKeyAncestors(clean, token) {
		return Snapshot{}, ErrUnavailable
	}
	stabilizedDistinct, ok := stabilizeWindowsDistinct(distinctFrom)
	if !ok {
		return Snapshot{}, ErrUnavailable
	}

	file, ok := openWindowsKeyFile(clean)
	if !ok {
		return Snapshot{}, ErrUnavailable
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()

	before, ok := windowsKeyFileInformation(file)
	if !ok || !safeWindowsKeyNative(file, before, false) ||
		!safeWindowsKeyDACL(file, token, false) ||
		!windowsKeyPathMatches(clean, windowsIdentity(before), pathIdentity) {
		return Snapshot{}, ErrUnavailable
	}
	handleInfo, err := file.Stat()
	if err != nil || !distinctWindowsKeyIdentity(handleInfo, stabilizedDistinct) {
		return Snapshot{}, ErrUnavailable
	}

	snapshot, err := parse(file)
	if err != nil || !snapshot.Valid() || !snapshot.Enabled() {
		return Snapshot{}, ErrUnavailable
	}

	after, ok := windowsKeyFileInformation(file)
	if !ok || !sameWindowsKeyMetadata(before, after) ||
		!safeWindowsKeyNative(file, after, false) ||
		!safeWindowsKeyDACL(file, token, false) ||
		!windowsKeyPathMatches(clean, windowsIdentity(after), pathIdentity) ||
		!safeWindowsKeyAncestors(clean, token) {
		return Snapshot{}, ErrUnavailable
	}
	if err := file.Close(); err != nil {
		closed = true
		return Snapshot{}, ErrUnavailable
	}
	closed = true
	return snapshot, nil
}

func cleanWindowsKeyPath(path string) (string, bool) {
	return cleanWindowsKeyPathWithDriveType(path, windows.GetDriveType)
}

type windowsDriveType func(*uint16) uint32

func cleanWindowsKeyPathWithDriveType(
	path string,
	driveType windowsDriveType,
) (string, bool) {
	if path == "" || strings.IndexByte(path, 0) >= 0 ||
		windowsDevicePath(path) || driveType == nil {
		return "", false
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || windowsDevicePath(clean) || strings.HasPrefix(clean, `\\`) {
		return "", false
	}
	volume := filepath.VolumeName(clean)
	if len(volume) != 2 || volume[1] != ':' {
		return "", false
	}
	drive := volume[0]
	if !((drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')) {
		return "", false
	}
	if strings.Contains(clean[len(volume):], ":") {
		return "", false
	}
	rootPointer, err := windows.UTF16PtrFromString(
		volume + string(filepath.Separator),
	)
	if err != nil || driveType(rootPointer) != windows.DRIVE_FIXED {
		return "", false
	}
	return clean, true
}

func windowsDevicePath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	return strings.HasPrefix(normalized, `\\?\`) ||
		strings.HasPrefix(normalized, `\\.\`) ||
		strings.HasPrefix(normalized, `\??\`) ||
		strings.HasPrefix(normalized, `\\??\`) ||
		strings.HasPrefix(normalized, `\device\`)
}

func openWindowsKeyFile(path string) (*os.File, bool) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false
	}
	// A transaction-owned staged key retains its write handle until atomic
	// publication. The loader still detects content, metadata, and path changes
	// before and after parsing, so this compatible share does not authorize an
	// unstable snapshot.
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, false
	}
	file := os.NewFile(uintptr(handle), "gateway-key")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, false
	}
	return file, true
}

func openWindowsKeyDirectory(path string) (*os.File, bool) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, false
	}
	file := os.NewFile(uintptr(handle), "gateway-key-ancestor")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, false
	}
	return file, true
}

func windowsKeyFileInformation(file *os.File) (windows.ByHandleFileInformation, bool) {
	if file == nil {
		return windows.ByHandleFileInformation{}, false
	}
	var information windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(
		windows.Handle(file.Fd()),
		&information,
	)
	return information, err == nil
}

func safeWindowsKeyNative(
	file *os.File,
	information windows.ByHandleFileInformation,
	directory bool,
) bool {
	typeValue, err := windows.GetFileType(windows.Handle(file.Fd()))
	return err == nil && safeWindowsKeyNativeEvidence(typeValue, information, directory)
}

func safeWindowsKeyNativeEvidence(
	typeValue uint32,
	information windows.ByHandleFileInformation,
	directory bool,
) bool {
	if typeValue != windows.FILE_TYPE_DISK ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_DEVICE != 0 {
		return false
	}
	isDirectory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory || (!directory && information.NumberOfLinks != 1) {
		return false
	}
	identity := windowsIdentity(information)
	return identity.volume != 0 || identity.index != 0
}

func windowsIdentity(information windows.ByHandleFileInformation) windowsKeyIdentity {
	return windowsKeyIdentity{
		volume: information.VolumeSerialNumber,
		index: uint64(information.FileIndexHigh)<<32 |
			uint64(information.FileIndexLow),
	}
}

func sameWindowsKeyMetadata(
	left windows.ByHandleFileInformation,
	right windows.ByHandleFileInformation,
) bool {
	return windowsIdentity(left) == windowsIdentity(right) &&
		left.FileSizeHigh == right.FileSizeHigh &&
		left.FileSizeLow == right.FileSizeLow &&
		left.LastWriteTime.HighDateTime == right.LastWriteTime.HighDateTime &&
		left.LastWriteTime.LowDateTime == right.LastWriteTime.LowDateTime
}

func windowsKeyPathIdentity(path string) (windowsKeyIdentity, bool) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windowsKeyIdentity{}, false
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return windowsKeyIdentity{}, false
	}
	var information windows.ByHandleFileInformation
	informationErr := windows.GetFileInformationByHandle(handle, &information)
	typeValue, typeErr := windows.GetFileType(handle)
	closeErr := windows.CloseHandle(handle)
	if informationErr != nil || typeErr != nil || closeErr != nil ||
		!safeWindowsKeyNativeEvidence(typeValue, information, false) {
		return windowsKeyIdentity{}, false
	}
	return windowsIdentity(information), true
}

func windowsKeyPathMatches(
	path string,
	handle windowsKeyIdentity,
	pathIdentity windowsPathIdentity,
) bool {
	current, ok := pathIdentity(path)
	return ok && current == handle
}

func stabilizeWindowsDistinct(
	distinctFrom []fs.FileInfo,
) ([]fs.FileInfo, bool) {
	stabilized := make([]fs.FileInfo, 0, len(distinctFrom))
	for _, other := range distinctFrom {
		if other == nil {
			continue
		}
		if !os.SameFile(other, other) {
			return nil, false
		}
		stabilized = append(stabilized, other)
	}
	return stabilized, true
}

func distinctWindowsKeyIdentity(
	handle fs.FileInfo,
	distinctFrom []fs.FileInfo,
) bool {
	if handle == nil {
		return false
	}
	for _, other := range distinctFrom {
		if other != nil && os.SameFile(handle, other) {
			return false
		}
	}
	return true
}

func safeWindowsKeyAncestors(path string, token windowsKeyToken) bool {
	volume := filepath.VolumeName(path)
	root := filepath.Clean(volume + string(filepath.Separator))
	current := filepath.Dir(path)
	for {
		file, ok := openWindowsKeyDirectory(current)
		if !ok {
			return false
		}
		information, infoOK := windowsKeyFileInformation(file)
		safe := infoOK && safeWindowsKeyNative(file, information, true) &&
			safeWindowsKeyDACL(file, token, true)
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
	}
}

func currentWindowsKeyToken() (windowsKeyToken, bool) {
	token := windows.GetCurrentThreadEffectiveToken()
	restricted, err := token.IsRestricted()
	if err != nil || restricted {
		return windowsKeyToken{}, false
	}
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return windowsKeyToken{}, false
	}
	result := windowsKeyToken{userSID: user.User.Sid.String()}
	groups, err := token.GetTokenGroups()
	if err != nil || groups == nil || result.userSID == "" {
		return windowsKeyToken{}, false
	}
	for _, group := range groups.AllGroups() {
		if group.Sid == nil || !group.Sid.IsValid() {
			return windowsKeyToken{}, false
		}
		result.groups = append(result.groups, windowsKeyTokenGroup{
			sid:      group.Sid.String(),
			enabled:  group.Attributes&windows.SE_GROUP_ENABLED != 0,
			denyOnly: group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY != 0,
		})
	}
	return result, true
}

func safeWindowsKeyDACL(file *os.File, token windowsKeyToken, ancestor bool) bool {
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return false
	}
	evidence, ok := normalizeWindowsKeyACL(descriptor)
	return ok && evaluateWindowsKeyACL(evidence, token, ancestor)
}

func normalizeWindowsKeyACL(
	descriptor *windows.SECURITY_DESCRIPTOR,
) (windowsKeyACLEvidence, bool) {
	evidence := windowsKeyACLEvidence{}
	if descriptor == nil || !descriptor.IsValid() {
		return evidence, false
	}
	evidence.descriptorSupported = true
	control, _, err := descriptor.Control()
	if err != nil {
		return windowsKeyACLEvidence{}, false
	}
	evidence.daclPresent = control&windows.SE_DACL_PRESENT != 0
	evidence.daclProtected = control&windows.SE_DACL_PROTECTED != 0
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return windowsKeyACLEvidence{}, false
	}
	evidence.ownerSID = owner.String()
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return windowsKeyACLEvidence{}, false
	}
	if dacl == nil {
		evidence.daclNull = evidence.daclPresent
		return evidence, true
	}
	evidence.aces = make([]windowsKeyACE, 0, dacl.AceCount)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var native *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, index, &native) != nil || native == nil ||
			native.Header.AceFlags&^windowsACLACEValidFlags != 0 {
			return windowsKeyACLEvidence{}, false
		}
		kind := windowsKeyACEUnknown
		switch native.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			kind = windowsKeyACEAllow
		case windows.ACCESS_DENIED_ACE_TYPE:
			kind = windowsKeyACEDeny
		default:
			return windowsKeyACLEvidence{}, false
		}
		const minimumSIDBytes = uintptr(8)
		sidOffset := unsafe.Offsetof(native.SidStart)
		if uintptr(native.Header.AceSize) < sidOffset+minimumSIDBytes {
			return windowsKeyACLEvidence{}, false
		}
		sid := (*windows.SID)(unsafe.Pointer(&native.SidStart))
		if !sid.IsValid() || uintptr(sid.Len()) > uintptr(native.Header.AceSize)-sidOffset {
			return windowsKeyACLEvidence{}, false
		}
		evidence.aces = append(evidence.aces, windowsKeyACE{
			kind:  kind,
			flags: native.Header.AceFlags,
			mask:  uint32(native.Mask),
			sid:   sid.String(),
		})
	}
	return evidence, true
}

func evaluateWindowsKeyACL(
	evidence windowsKeyACLEvidence,
	token windowsKeyToken,
	ancestor bool,
) bool {
	if !evidence.descriptorSupported || !evidence.daclPresent ||
		evidence.daclNull || evidence.ownerSID == "" || token.userSID == "" ||
		(!ancestor && !evidence.daclProtected) ||
		(!ancestor && evidence.ownerSID != token.userSID) ||
		(ancestor && !trustedWindowsKeySID(evidence.ownerSID, token.userSID, true)) {
		return false
	}
	required := uint32(windowsACLReadData | windowsACLReadAttributes)
	forbidden := uint32(windowsACLForbiddenLeaf)
	if ancestor {
		required = windowsACLExecute
		forbidden = windowsACLForbiddenAncestor
	}
	remaining := required
	for _, ace := range evidence.aces {
		if (ace.kind != windowsKeyACEAllow && ace.kind != windowsKeyACEDeny) ||
			ace.flags&^windowsACLACEValidFlags != 0 || ace.sid == "" {
			return false
		}
		mask, ok := expandWindowsKeyGeneric(ace.mask)
		if !ok {
			return false
		}
		if ace.flags&windowsACLACEInheritOnly != 0 {
			continue
		}
		if ace.kind == windowsKeyACEAllow &&
			!trustedWindowsKeySID(ace.sid, token.userSID, ancestor) &&
			mask&forbidden != 0 {
			return false
		}
		if ace.kind == windowsKeyACEDeny &&
			windowsKeySIDApplies(token, ace.sid, true) && mask&remaining != 0 {
			return false
		}
		if ace.kind == windowsKeyACEAllow && windowsKeySIDApplies(token, ace.sid, false) {
			remaining &^= mask
		}
	}
	return remaining == 0
}

func trustedWindowsKeySID(sid, userSID string, ancestor bool) bool {
	return sid == userSID || sid == windowsLocalSystemSID ||
		sid == windowsBuiltinAdministratorsSID ||
		(ancestor && sid == windowsTrustedInstallerSID)
}

func windowsKeySIDApplies(token windowsKeyToken, sid string, deny bool) bool {
	if sid == token.userSID {
		return true
	}
	for _, group := range token.groups {
		if group.sid == sid && (group.enabled || (deny && group.denyOnly)) {
			return true
		}
	}
	return false
}

func expandWindowsKeyGeneric(mask uint32) (uint32, bool) {
	concrete := mask &^ (windowsACLGenericRead |
		windowsACLGenericWrite |
		windowsACLGenericExecute |
		windowsACLGenericAll)
	if mask&windowsACLGenericRead != 0 {
		concrete |= 0x00120089
	}
	if mask&windowsACLGenericWrite != 0 {
		concrete |= 0x00120116
	}
	if mask&windowsACLGenericExecute != 0 {
		concrete |= 0x001200a0
	}
	if mask&windowsACLGenericAll != 0 {
		concrete |= windowsACLConcreteRights
	}
	return concrete, concrete&^windowsACLConcreteRights == 0
}
