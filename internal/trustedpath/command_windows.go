//go:build windows

package trustedpath

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsCommandMask uint32

const (
	windowsCommandReadData        windowsCommandMask = 0x00000001
	windowsCommandWriteData       windowsCommandMask = 0x00000002
	windowsCommandAppendData      windowsCommandMask = 0x00000004
	windowsCommandReadEA          windowsCommandMask = 0x00000008
	windowsCommandWriteEA         windowsCommandMask = 0x00000010
	windowsCommandExecute         windowsCommandMask = 0x00000020
	windowsCommandDeleteChild     windowsCommandMask = 0x00000040
	windowsCommandReadAttributes  windowsCommandMask = 0x00000080
	windowsCommandWriteAttributes windowsCommandMask = 0x00000100
	windowsCommandDelete          windowsCommandMask = 0x00010000
	windowsCommandReadControl     windowsCommandMask = 0x00020000
	windowsCommandWriteDAC        windowsCommandMask = 0x00040000
	windowsCommandWriteOwner      windowsCommandMask = 0x00080000
	windowsCommandSynchronize     windowsCommandMask = 0x00100000
	windowsCommandGenericAll      windowsCommandMask = 0x10000000
	windowsCommandGenericExecute  windowsCommandMask = 0x20000000
	windowsCommandGenericWrite    windowsCommandMask = 0x40000000
	windowsCommandGenericRead     windowsCommandMask = 0x80000000

	windowsCommandConcreteRights   windowsCommandMask = 0x001f01ff
	windowsCommandLeafRequired     windowsCommandMask = 0x000000a1
	windowsCommandDirectoryRequire windowsCommandMask = 0x000000a1
	windowsCommandLeafForbidden    windowsCommandMask = 0x000d0156
	windowsCommandAncestorForbid   windowsCommandMask = 0x000d0040
)

const (
	windowsCommandACEObjectInherit    uint8 = 0x01
	windowsCommandACEContainerInherit uint8 = 0x02
	windowsCommandACENoPropagate      uint8 = 0x04
	windowsCommandACEInheritOnly      uint8 = 0x08
	windowsCommandACEInherited        uint8 = 0x10
	windowsCommandACEValidFlags             = windowsCommandACEObjectInherit |
		windowsCommandACEContainerInherit |
		windowsCommandACENoPropagate |
		windowsCommandACEInheritOnly |
		windowsCommandACEInherited
)

const (
	windowsCommandLocalSystemSID = "S-1-5-18"
	windowsCommandAdminsSID      = "S-1-5-32-544"
	windowsCommandInstallerSID   = "S-1-5-80-956008885-3418522649-" +
		"1831038044-1853292631-2271478464"
)

type windowsCommandObject uint8

const (
	windowsCommandObjectUnknown windowsCommandObject = iota
	windowsCommandObjectFile
	windowsCommandObjectDirectory
)

type windowsCommandACEKind uint8

const (
	windowsCommandACEUnknown windowsCommandACEKind = iota
	windowsCommandACEAllow
	windowsCommandACEDeny
)

type windowsCommandACE struct {
	kind  windowsCommandACEKind
	flags uint8
	mask  windowsCommandMask
	sid   string
}

type windowsCommandTokenGroup struct {
	sid      string
	enabled  bool
	denyOnly bool
}

type windowsCommandToken struct {
	userSID string
	groups  []windowsCommandTokenGroup
}

type windowsCommandFileID struct {
	volume uint32
	index  uint64
}

type windowsCommandEvidence struct {
	descriptor bool
	dacl       bool
	daclNull   bool
	object     windowsCommandObject
	ownerSID   string
	token      windowsCommandToken
	aces       []windowsCommandACE
	reparse    bool
	openedID   windowsCommandFileID
	reopenedID windowsCommandFileID
	canonical  string
}

type windowsCommandInspection struct {
	mu      sync.Mutex
	file    *os.File
	path    CommandPath
	info    fs.FileInfo
	id      windowsCommandFileID
	bytes   []byte
	userSID string
	closed  bool
}

// OpenCommandPath validates, retains, and optionally reads one Windows command.
func OpenCommandPath(
	path string,
	mode CommandReadMode,
	limit int64,
) (CommandFileInspection, error) {
	if !validCommandRead(mode, limit) {
		return nil, ErrUnsafe
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	clean, _, err := normalizeWindowsCommandInput(path)
	if err != nil {
		return nil, ErrUnsafe
	}
	token, err := acquireWindowsCommandToken()
	if err != nil {
		return nil, ErrUnsafe
	}
	if err := validateWindowsCommandAncestors(clean, token); err != nil {
		return nil, classifyWindowsCommandError(err)
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES)
	if mode == CommandBoundedContent {
		access |= windows.FILE_READ_DATA
	}
	file, evidence, info, err := acquireWindowsCommandSnapshot(clean, token, access)
	if err != nil {
		return nil, classifyWindowsCommandError(err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	if err := evaluateWindowsCommandEvidence(
		evidence,
		windowsCommandObjectFile,
		windowsCommandLeafRequired,
		windowsCommandLeafForbidden,
	); err != nil {
		return nil, ErrUnsafe
	}
	if err := validateWindowsCommandAncestors(evidence.canonical, token); err != nil {
		return nil, ErrUnsafe
	}
	payload := []byte(nil)
	if mode == CommandBoundedContent {
		if info.Size() < 0 || info.Size() > limit {
			return nil, ErrUnsafe
		}
		payload, err = io.ReadAll(io.LimitReader(file, limit+1))
		if err != nil || int64(len(payload)) > limit {
			return nil, ErrUnsafe
		}
		afterRead, statErr := file.Stat()
		if statErr != nil || !sameWindowsCommandInfo(info, afterRead) {
			return nil, ErrUnsafe
		}
		info = afterRead
	}
	keep = true
	return &windowsCommandInspection{
		file: file,
		path: CommandPath{
			Clean:        clean,
			Resolved:     evidence.canonical,
			CanonicalKey: mustWindowsCommandKey(evidence.canonical),
		},
		info:    info,
		id:      evidence.reopenedID,
		bytes:   append([]byte(nil), payload...),
		userSID: token.userSID,
	}, nil
}

func (i *windowsCommandInspection) Bytes() []byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]byte(nil), i.bytes...)
}

func (i *windowsCommandInspection) FileInfo() fs.FileInfo {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.info
}

func (i *windowsCommandInspection) Revalidate() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.file == nil || i.info == nil {
		return ErrUnsafe
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	token, err := acquireWindowsCommandToken()
	if err != nil || token.userSID != i.userSID {
		return ErrUnsafe
	}
	if err := validateWindowsCommandAncestors(i.path.Clean, token); err != nil {
		return ErrUnsafe
	}
	info, err := i.file.Stat()
	if err != nil || !sameWindowsCommandInfo(i.info, info) {
		return ErrUnsafe
	}
	evidence, err := windowsCommandEvidenceFromHandle(i.file, token)
	if err != nil || evidence.reopenedID != i.id ||
		mustWindowsCommandKey(evidence.canonical) != i.path.CanonicalKey {
		return ErrUnsafe
	}
	if err := evaluateWindowsCommandEvidence(
		evidence,
		windowsCommandObjectFile,
		windowsCommandLeafRequired,
		windowsCommandLeafForbidden,
	); err != nil {
		return ErrUnsafe
	}
	if err := validateWindowsCommandAncestors(i.path.Resolved, token); err != nil {
		return ErrUnsafe
	}
	fresh, freshEvidence, freshInfo, err := acquireWindowsCommandSnapshot(
		i.path.Clean,
		token,
		windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
	)
	if err != nil {
		return ErrUnsafe
	}
	freshEvidenceErr := validateWindowsCommandFreshEvidence(
		freshEvidence,
		i.id,
		i.path.CanonicalKey,
	)
	freshInfoMatches := sameWindowsCommandInfo(info, freshInfo)
	closeErr := fresh.Close()
	if closeErr != nil || freshEvidenceErr != nil || !freshInfoMatches {
		return ErrUnsafe
	}
	return nil
}

func validateWindowsCommandFreshEvidence(
	evidence windowsCommandEvidence,
	expectedID windowsCommandFileID,
	expectedCanonicalKey string,
) error {
	if !validWindowsCommandFileID(expectedID) || expectedCanonicalKey == "" ||
		evaluateWindowsCommandEvidence(
			evidence,
			windowsCommandObjectFile,
			windowsCommandLeafRequired,
			windowsCommandLeafForbidden,
		) != nil || evidence.reopenedID != expectedID ||
		mustWindowsCommandKey(evidence.canonical) != expectedCanonicalKey {
		return ErrUnsafe
	}
	return nil
}

func (i *windowsCommandInspection) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	i.closed = true
	if i.file == nil {
		return ErrUnsafe
	}
	err := i.file.Close()
	i.file = nil
	if err != nil {
		return ErrUnsafe
	}
	return nil
}

func (i *windowsCommandInspection) commandPath() CommandPath {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.path
}

func validateWindowsCommandAncestors(
	path string,
	token windowsCommandToken,
) error {
	ancestors, err := windowsCommandAncestors(path)
	if err != nil {
		return ErrUnsafe
	}
	for _, ancestor := range ancestors {
		file, evidence, _, err := acquireWindowsCommandSnapshot(
			ancestor,
			token,
			windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		)
		if err != nil {
			return err
		}
		closeErr := file.Close()
		if closeErr != nil || evaluateWindowsCommandEvidence(
			evidence,
			windowsCommandObjectDirectory,
			windowsCommandDirectoryRequire,
			windowsCommandAncestorForbid,
		) != nil {
			return ErrUnsafe
		}
	}
	return nil
}

func acquireWindowsCommandSnapshot(
	path string,
	token windowsCommandToken,
	access uint32,
) (*os.File, windowsCommandEvidence, fs.FileInfo, error) {
	first, err := openWindowsCommandHandle(path, access)
	if err != nil {
		return nil, windowsCommandEvidence{}, nil, err
	}
	firstInfo, err := first.Stat()
	if err != nil {
		_ = first.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	firstNative, err := windowsCommandFileInformation(first)
	if err != nil {
		_ = first.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	firstObject, firstReparse, firstID := normalizeWindowsCommandFile(firstNative)
	firstFinal, err := windowsCommandFinalPath(first)
	if err != nil {
		_ = first.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	canonical, canonicalKey, err := normalizeWindowsCommandFinal(firstFinal)
	if err != nil {
		_ = first.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}

	second, err := openWindowsCommandHandle(canonical, access)
	if err != nil {
		_ = first.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	secondInfo, err := second.Stat()
	if err != nil {
		_ = first.Close()
		_ = second.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	secondNative, err := windowsCommandFileInformation(second)
	if err != nil {
		_ = first.Close()
		_ = second.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	secondObject, secondReparse, secondID := normalizeWindowsCommandFile(secondNative)
	secondFinal, err := windowsCommandFinalPath(second)
	if err != nil {
		_ = first.Close()
		_ = second.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	secondCanonical, secondKey, err := normalizeWindowsCommandFinal(secondFinal)
	closeFirstErr := first.Close()
	if err != nil || closeFirstErr != nil || secondKey != canonicalKey ||
		secondCanonical != canonical || firstObject != secondObject ||
		firstID != secondID || !sameWindowsCommandInfo(firstInfo, secondInfo) {
		_ = second.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	evidence, err := windowsCommandEvidenceFromHandle(second, token)
	if err != nil {
		_ = second.Close()
		return nil, windowsCommandEvidence{}, nil, ErrUnsafe
	}
	evidence.openedID = firstID
	evidence.reparse = firstReparse || secondReparse || evidence.reparse
	return second, evidence, secondInfo, nil
}

func windowsCommandEvidenceFromHandle(
	file *os.File,
	token windowsCommandToken,
) (windowsCommandEvidence, error) {
	native, err := windowsCommandFileInformation(file)
	if err != nil {
		return windowsCommandEvidence{}, ErrUnsafe
	}
	object, reparse, identity := normalizeWindowsCommandFile(native)
	final, err := windowsCommandFinalPath(file)
	if err != nil {
		return windowsCommandEvidence{}, ErrUnsafe
	}
	canonical, _, err := normalizeWindowsCommandFinal(final)
	if err != nil {
		return windowsCommandEvidence{}, ErrUnsafe
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return windowsCommandEvidence{}, ErrUnsafe
	}
	descriptorSupported, daclPresent, daclNull, ownerSID, aces, err :=
		normalizeWindowsCommandDescriptor(descriptor)
	if err != nil {
		return windowsCommandEvidence{}, ErrUnsafe
	}
	return windowsCommandEvidence{
		descriptor: descriptorSupported,
		dacl:       daclPresent,
		daclNull:   daclNull,
		object:     object,
		ownerSID:   ownerSID,
		token:      cloneWindowsCommandToken(token),
		aces:       aces,
		reparse:    reparse,
		openedID:   identity,
		reopenedID: identity,
		canonical:  canonical,
	}, nil
}

func openWindowsCommandHandle(path string, access uint32) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrUnsafe
	}
	handle, err := windows.CreateFile(
		pointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), "trusted-windows-command")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, ErrUnsafe
	}
	return file, nil
}

func windowsCommandFileInformation(
	file *os.File,
) (windows.ByHandleFileInformation, error) {
	if file == nil {
		return windows.ByHandleFileInformation{}, ErrUnsafe
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

func windowsCommandFinalPath(file *os.File) (string, error) {
	if file == nil {
		return "", ErrUnsafe
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
		if err != nil || length == 0 {
			return "", ErrUnsafe
		}
		if length < size {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		if length >= 1<<15 {
			return "", ErrUnsafe
		}
		size = length + 1
	}
}

func normalizeWindowsCommandInput(path string) (string, string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", "", ErrUnsafe
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || windowsCommandDeviceNamespace(clean) {
		return "", "", ErrUnsafe
	}
	key, err := windowsCommandPathKey(clean)
	if err != nil {
		return "", "", ErrUnsafe
	}
	return clean, key, nil
}

func normalizeWindowsCommandFinal(path string) (string, string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", "", ErrUnsafe
	}
	normalized := strings.ReplaceAll(path, "/", `\`)
	lower := strings.ToLower(normalized)
	const (
		extendedPrefix    = `\\?\`
		extendedUNCPrefix = `\\?\unc\`
	)
	switch {
	case strings.HasPrefix(lower, extendedUNCPrefix):
		normalized = `\\` + normalized[len(extendedUNCPrefix):]
	case strings.HasPrefix(lower, extendedPrefix):
		normalized = normalized[len(extendedPrefix):]
	case windowsCommandDeviceNamespace(normalized):
		return "", "", ErrUnsafe
	}
	clean := filepath.Clean(normalized)
	if !filepath.IsAbs(clean) || windowsCommandDeviceNamespace(clean) {
		return "", "", ErrUnsafe
	}
	key, err := windowsCommandPathKey(clean)
	if err != nil {
		return "", "", ErrUnsafe
	}
	return clean, key, nil
}

func windowsCommandDeviceNamespace(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	return strings.HasPrefix(normalized, `\\?\`) ||
		strings.HasPrefix(normalized, `\\.\`) ||
		strings.HasPrefix(normalized, `\??\`) ||
		strings.HasPrefix(normalized, `\\??\`) ||
		strings.HasPrefix(normalized, `\device\`)
}

func windowsCommandAncestors(path string) ([]string, error) {
	clean, cleanKey, err := normalizeWindowsCommandInput(path)
	if err != nil {
		return nil, ErrUnsafe
	}
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return nil, ErrUnsafe
	}
	root := filepath.Clean(volume + string(filepath.Separator))
	rootKey, err := windowsCommandPathKey(root)
	if err != nil {
		return nil, ErrUnsafe
	}
	if cleanKey == rootKey {
		return nil, nil
	}
	current := filepath.Dir(clean)
	reversed := make([]string, 0, 8)
	for {
		currentKey, err := windowsCommandPathKey(current)
		if err != nil {
			return nil, ErrUnsafe
		}
		reversed = append(reversed, current)
		if currentKey == rootKey {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, ErrUnsafe
		}
		current = parent
	}
	ancestors := make([]string, len(reversed))
	for index := range reversed {
		ancestors[index] = reversed[len(reversed)-1-index]
	}
	return ancestors, nil
}

func normalizeWindowsCommandFile(
	information windows.ByHandleFileInformation,
) (windowsCommandObject, bool, windowsCommandFileID) {
	reparse := information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	identity := windowsCommandFileID{
		volume: information.VolumeSerialNumber,
		index: uint64(information.FileIndexHigh)<<32 |
			uint64(information.FileIndexLow),
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DEVICE != 0 {
		return windowsCommandObjectUnknown, reparse, identity
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return windowsCommandObjectDirectory, reparse, identity
	}
	return windowsCommandObjectFile, reparse, identity
}

func normalizeWindowsCommandDescriptor(
	descriptor *windows.SECURITY_DESCRIPTOR,
) (bool, bool, bool, string, []windowsCommandACE, error) {
	if descriptor == nil || !descriptor.IsValid() {
		return false, false, false, "", nil, nil
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return false, false, false, "", nil, err
	}
	daclPresent := control&windows.SE_DACL_PRESENT != 0
	owner, _, err := descriptor.Owner()
	if err != nil {
		return false, false, false, "", nil, err
	}
	if owner == nil || !owner.IsValid() || owner.String() == "" {
		return false, daclPresent, false, "", nil, nil
	}
	ownerSID := owner.String()
	if !daclPresent {
		return true, false, false, ownerSID, nil, nil
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return false, false, false, "", nil, err
	}
	if dacl == nil {
		return true, true, true, ownerSID, nil, nil
	}
	aces := make([]windowsCommandACE, 0, dacl.AceCount)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var native *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &native); err != nil {
			return false, false, false, "", nil, err
		}
		ace, ok := normalizeWindowsCommandACE(native)
		if !ok {
			return false, true, false, ownerSID, nil, nil
		}
		aces = append(aces, ace)
	}
	return true, true, false, ownerSID, aces, nil
}

func normalizeWindowsCommandACE(
	native *windows.ACCESS_ALLOWED_ACE,
) (windowsCommandACE, bool) {
	if native == nil {
		return windowsCommandACE{}, false
	}
	kind := windowsCommandACEUnknown
	switch native.Header.AceType {
	case windows.ACCESS_ALLOWED_ACE_TYPE:
		kind = windowsCommandACEAllow
	case windows.ACCESS_DENIED_ACE_TYPE:
		kind = windowsCommandACEDeny
	default:
		return windowsCommandACE{}, false
	}
	const minimumSIDBytes = uintptr(8)
	sidOffset := unsafe.Offsetof(native.SidStart)
	if uintptr(native.Header.AceSize) < sidOffset+minimumSIDBytes {
		return windowsCommandACE{}, false
	}
	sid := (*windows.SID)(unsafe.Pointer(&native.SidStart))
	if !sid.IsValid() || uintptr(sid.Len()) > uintptr(native.Header.AceSize)-sidOffset {
		return windowsCommandACE{}, false
	}
	sidString := sid.String()
	if sidString == "" {
		return windowsCommandACE{}, false
	}
	return windowsCommandACE{
		kind:  kind,
		flags: native.Header.AceFlags,
		mask:  windowsCommandMask(native.Mask),
		sid:   sidString,
	}, true
}

func acquireWindowsCommandToken() (windowsCommandToken, error) {
	token := windows.GetCurrentThreadEffectiveToken()
	restricted, err := token.IsRestricted()
	if err != nil || restricted {
		return windowsCommandToken{}, ErrUnsafe
	}
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil ||
		!user.User.Sid.IsValid() || user.User.Sid.String() == "" {
		return windowsCommandToken{}, ErrUnsafe
	}
	groups, err := token.GetTokenGroups()
	if err != nil || groups == nil {
		return windowsCommandToken{}, ErrUnsafe
	}
	snapshot := windowsCommandToken{
		userSID: user.User.Sid.String(),
		groups:  make([]windowsCommandTokenGroup, 0, groups.GroupCount),
	}
	for _, group := range groups.AllGroups() {
		if group.Sid == nil || !group.Sid.IsValid() || group.Sid.String() == "" {
			return windowsCommandToken{}, ErrUnsafe
		}
		snapshot.groups = append(snapshot.groups, windowsCommandTokenGroup{
			sid:      group.Sid.String(),
			enabled:  group.Attributes&windows.SE_GROUP_ENABLED != 0,
			denyOnly: group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY != 0,
		})
	}
	if !validWindowsCommandToken(snapshot) {
		return windowsCommandToken{}, ErrUnsafe
	}
	return snapshot, nil
}

func cloneWindowsCommandToken(token windowsCommandToken) windowsCommandToken {
	cloned := token
	cloned.groups = append([]windowsCommandTokenGroup(nil), token.groups...)
	return cloned
}

func evaluateWindowsCommandEvidence(
	evidence windowsCommandEvidence,
	object windowsCommandObject,
	required windowsCommandMask,
	forbidden windowsCommandMask,
) error {
	if !evidence.descriptor || !evidence.dacl || evidence.daclNull ||
		evidence.reparse || evidence.object != object ||
		!validWindowsCommandSID(evidence.ownerSID) ||
		!validWindowsCommandToken(evidence.token) ||
		!validWindowsCommandFileID(evidence.openedID) ||
		evidence.openedID != evidence.reopenedID ||
		!trustedWindowsCommandSID(evidence.ownerSID, evidence.token.userSID) {
		return ErrUnsafe
	}
	if _, err := windowsCommandPathKey(evidence.canonical); err != nil {
		return ErrUnsafe
	}
	normalized := make([]windowsCommandACE, len(evidence.aces))
	for index, ace := range evidence.aces {
		if (ace.kind != windowsCommandACEAllow && ace.kind != windowsCommandACEDeny) ||
			ace.flags&^windowsCommandACEValidFlags != 0 ||
			!validWindowsCommandSID(ace.sid) {
			return ErrUnsafe
		}
		expanded, ok := expandWindowsCommandGeneric(ace.mask, object)
		if !ok {
			return ErrUnsafe
		}
		ace.mask = expanded
		normalized[index] = ace
		if ace.flags&windowsCommandACEInheritOnly == 0 &&
			ace.kind == windowsCommandACEAllow &&
			!trustedWindowsCommandSID(ace.sid, evidence.token.userSID) &&
			ace.mask&forbidden != 0 {
			return ErrUnsafe
		}
	}
	remaining := required
	for _, ace := range normalized {
		if ace.flags&windowsCommandACEInheritOnly != 0 {
			continue
		}
		switch ace.kind {
		case windowsCommandACEDeny:
			if windowsCommandSIDAppliesToDeny(evidence.token, ace.sid) &&
				ace.mask&remaining != 0 {
				return ErrUnsafe
			}
		case windowsCommandACEAllow:
			if windowsCommandSIDAppliesToAllow(evidence.token, ace.sid) {
				remaining &^= ace.mask
			}
		default:
			return ErrUnsafe
		}
	}
	if remaining != 0 {
		return ErrUnsafe
	}
	return nil
}

func validWindowsCommandFileID(identity windowsCommandFileID) bool {
	return identity.volume != 0 || identity.index != 0
}

func validWindowsCommandToken(token windowsCommandToken) bool {
	if !validWindowsCommandSID(token.userSID) {
		return false
	}
	seen := make(map[string]struct{}, len(token.groups))
	for _, group := range token.groups {
		if !validWindowsCommandSID(group.sid) || group.enabled && group.denyOnly {
			return false
		}
		if _, duplicate := seen[group.sid]; duplicate {
			return false
		}
		seen[group.sid] = struct{}{}
	}
	return true
}

func validWindowsCommandSID(sid string) bool {
	if !strings.HasPrefix(sid, "S-") {
		return false
	}
	parts := strings.Split(sid[2:], "-")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func trustedWindowsCommandSID(sid, userSID string) bool {
	return sid == userSID || sid == windowsCommandLocalSystemSID ||
		sid == windowsCommandAdminsSID || sid == windowsCommandInstallerSID
}

func windowsCommandSIDAppliesToAllow(token windowsCommandToken, sid string) bool {
	if sid == token.userSID {
		return true
	}
	for _, group := range token.groups {
		if group.sid == sid && group.enabled {
			return true
		}
	}
	return false
}

func windowsCommandSIDAppliesToDeny(token windowsCommandToken, sid string) bool {
	if sid == token.userSID {
		return true
	}
	for _, group := range token.groups {
		if group.sid == sid && (group.enabled || group.denyOnly) {
			return true
		}
	}
	return false
}

func expandWindowsCommandGeneric(
	mask windowsCommandMask,
	object windowsCommandObject,
) (windowsCommandMask, bool) {
	if object != windowsCommandObjectFile && object != windowsCommandObjectDirectory {
		return 0, false
	}
	concrete := mask &^ (windowsCommandGenericRead |
		windowsCommandGenericWrite |
		windowsCommandGenericExecute |
		windowsCommandGenericAll)
	if mask&windowsCommandGenericRead != 0 {
		concrete |= windowsCommandReadControl | windowsCommandReadData |
			windowsCommandReadEA | windowsCommandReadAttributes |
			windowsCommandSynchronize
	}
	if mask&windowsCommandGenericWrite != 0 {
		concrete |= windowsCommandReadControl | windowsCommandWriteData |
			windowsCommandAppendData | windowsCommandWriteEA |
			windowsCommandWriteAttributes | windowsCommandSynchronize
	}
	if mask&windowsCommandGenericExecute != 0 {
		concrete |= windowsCommandReadControl | windowsCommandExecute |
			windowsCommandReadAttributes | windowsCommandSynchronize
	}
	if mask&windowsCommandGenericAll != 0 {
		concrete |= windowsCommandConcreteRights
	}
	if concrete&^windowsCommandConcreteRights != 0 {
		return 0, false
	}
	return concrete, true
}

func windowsCommandPathKey(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", ErrUnsafe
	}
	path = strings.ReplaceAll(path, "/", `\`)
	lower := strings.ToLower(path)
	const (
		extendedPrefix    = `\\?\`
		extendedUNCPrefix = `\\?\unc\`
		devicePrefix      = `\\.\`
	)
	switch {
	case strings.HasPrefix(lower, extendedUNCPrefix):
		path = `\\` + path[len(extendedUNCPrefix):]
	case strings.HasPrefix(lower, extendedPrefix):
		path = path[len(extendedPrefix):]
	case strings.HasPrefix(lower, devicePrefix):
		return "", ErrUnsafe
	}
	if isWindowsCommandDrivePath(path) {
		components, ok := cleanWindowsCommandComponents(strings.Split(path[3:], `\`))
		if !ok {
			return "", ErrUnsafe
		}
		root := strings.ToLower(path[:2]) + `\`
		if len(components) == 0 {
			return root, nil
		}
		return root + strings.Join(components, `\`), nil
	}
	if len(path) < 3 || !strings.HasPrefix(path, `\\`) || path[2] == '\\' {
		return "", ErrUnsafe
	}
	parts := strings.Split(path[2:], `\`)
	rootParts := make([]string, 0, 2)
	tailStart := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		if len(rootParts) < 2 {
			if part == "." || part == ".." || strings.ContainsRune(part, ':') {
				return "", ErrUnsafe
			}
			rootParts = append(rootParts, strings.ToLower(part))
			tailStart = index + 1
			continue
		}
		break
	}
	if len(rootParts) != 2 {
		return "", ErrUnsafe
	}
	tail, ok := cleanWindowsCommandComponents(parts[tailStart:])
	if !ok {
		return "", ErrUnsafe
	}
	key := `\\` + rootParts[0] + `\` + rootParts[1]
	if len(tail) != 0 {
		key += `\` + strings.Join(tail, `\`)
	}
	return key, nil
}

func isWindowsCommandDrivePath(path string) bool {
	if len(path) < 3 || path[1] != ':' || path[2] != '\\' {
		return false
	}
	drive := path[0]
	return drive >= 'a' && drive <= 'z' || drive >= 'A' && drive <= 'Z'
}

func cleanWindowsCommandComponents(parts []string) ([]string, bool) {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(clean) == 0 {
				return nil, false
			}
			clean = clean[:len(clean)-1]
		default:
			if strings.ContainsRune(part, ':') || strings.IndexByte(part, 0) >= 0 {
				return nil, false
			}
			clean = append(clean, strings.ToLower(part))
		}
	}
	return clean, true
}

func mustWindowsCommandKey(path string) string {
	key, err := windowsCommandPathKey(path)
	if err != nil {
		return ""
	}
	return key
}

func classifyWindowsCommandError(err error) error {
	if errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return ErrMissing
	}
	return ErrUnsafe
}

func sameWindowsCommandInfo(left, right fs.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime())
}
