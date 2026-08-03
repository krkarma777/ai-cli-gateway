package doctor

import (
	"errors"
	"strings"
)

var (
	errUnsafeWindowsACL      = errors.New("unsafe Windows path ACL")
	errInvalidWindowsPathKey = errors.New("invalid Windows path key")
)

type aclMask uint32

const (
	aclReadData        aclMask = 0x00000001
	aclWriteData       aclMask = 0x00000002
	aclAppendData      aclMask = 0x00000004
	aclReadEA          aclMask = 0x00000008
	aclWriteEA         aclMask = 0x00000010
	aclExecute         aclMask = 0x00000020
	aclDeleteChild     aclMask = 0x00000040
	aclReadAttributes  aclMask = 0x00000080
	aclWriteAttributes aclMask = 0x00000100

	aclDelete      aclMask = 0x00010000
	aclReadControl aclMask = 0x00020000
	aclWriteDAC    aclMask = 0x00040000
	aclWriteOwner  aclMask = 0x00080000
	aclSynchronize aclMask = 0x00100000

	aclAccessSystemSecurity aclMask = 0x01000000
	aclMaximumAllowed       aclMask = 0x02000000
	aclGenericAll           aclMask = 0x10000000
	aclGenericExecute       aclMask = 0x20000000
	aclGenericWrite         aclMask = 0x40000000
	aclGenericRead          aclMask = 0x80000000
)

const (
	aclConcreteRights aclMask = 0x001f01ff

	aclIntegrityForbidden       aclMask = 0x000d0156
	aclAncestorForbidden        aclMask = 0x000d0040
	aclConfidentialityForbidden aclMask = 0x000d01ff

	aclExecutableRequired      aclMask = 0x000000a1
	aclPathDirectoryRequired   aclMask = 0x000000a1
	aclPrivateAncestorRequired aclMask = 0x00000020
	aclConfigHomeRequired      aclMask = 0x000000e7
	aclCredentialRequired      aclMask = 0x00000081
)

const (
	aclACEObjectInherit    uint8 = 0x01
	aclACEContainerInherit uint8 = 0x02
	aclACENoPropagate      uint8 = 0x04
	aclACEInheritOnly      uint8 = 0x08
	aclACEInherited        uint8 = 0x10
	aclACEValidFlags             = aclACEObjectInherit |
		aclACEContainerInherit |
		aclACENoPropagate |
		aclACEInheritOnly |
		aclACEInherited
)

const (
	aclLocalSystemSID           = "S-1-5-18"
	aclBuiltinAdministratorsSID = "S-1-5-32-544"
	aclTrustedInstallerSID      = "S-1-5-80-956008885-3418522649-" +
		"1831038044-1853292631-2271478464"
)

type aclObject uint8

const (
	aclObjectUnknown aclObject = iota
	aclObjectFile
	aclObjectDirectory
)

type aclACEKind uint8

const (
	aclACEUnknown aclACEKind = iota
	aclACEAllow
	aclACEDeny
)

type aclACE struct {
	Kind  aclACEKind
	Flags uint8
	Mask  aclMask
	SID   string
}

type aclTokenGroup struct {
	SID      string
	Enabled  bool
	DenyOnly bool
}

type aclTokenSnapshot struct {
	Supported bool
	UserSID   string
	Groups    []aclTokenGroup
}

type windowsFileID struct {
	Volume uint32
	Index  uint64
}

type windowsACLSnapshot struct {
	DescriptorSupported bool
	DACLPresent         bool
	DACLNull            bool
	Object              aclObject
	OwnerSID            string
	Token               aclTokenSnapshot
	ACEs                []aclACE
	Reparse             bool
	OpenedID            windowsFileID
	ReopenedID          windowsFileID
	CanonicalPath       string
}

type windowsACLPolicy uint8

const (
	windowsACLPolicyUnknown windowsACLPolicy = iota
	windowsExecutablePolicy
	windowsPathDirectoryPolicy
	windowsPrivateAncestorPolicy
	windowsConfigHomePolicy
	windowsCredentialPolicy
	windowsPathAncestorPolicy
)

type windowsACLRule struct {
	object                aclObject
	required              aclMask
	forbidden             aclMask
	ownerMustBeTokenUser  bool
	trustTrustedInstaller bool
}

func windowsPoliciesForPathKind(
	kind pathKind,
) (windowsACLPolicy, windowsACLPolicy, bool) {
	switch kind {
	case pathKindExecutable, pathKindEntrypoint:
		return windowsExecutablePolicy, windowsPathAncestorPolicy, true
	case pathKindConfigHome:
		return windowsConfigHomePolicy, windowsPrivateAncestorPolicy, true
	case pathKindCredential:
		return windowsCredentialPolicy, windowsPrivateAncestorPolicy, true
	case pathKindSafeDirectory:
		return windowsPathDirectoryPolicy, windowsPathAncestorPolicy, true
	default:
		return windowsACLPolicyUnknown, windowsACLPolicyUnknown, false
	}
}

func windowsLeafShapeAllowed(kind pathKind, path string) bool {
	extension := windowsPathExtension(path)
	switch kind {
	case pathKindExecutable:
		return extension != ".cmd" && extension != ".bat"
	case pathKindEntrypoint:
		return extension == ".js" || extension == ".mjs"
	case pathKindConfigHome, pathKindCredential, pathKindSafeDirectory:
		return true
	default:
		return false
	}
}

func windowsPathExtension(path string) string {
	normalized := strings.ReplaceAll(path, "/", `\`)
	if separator := strings.LastIndexByte(normalized, '\\'); separator >= 0 {
		normalized = normalized[separator+1:]
	}
	dot := strings.LastIndexByte(normalized, '.')
	if dot < 0 {
		return ""
	}
	return strings.ToLower(normalized[dot:])
}

func evaluateWindowsACL(
	snapshot windowsACLSnapshot,
	policy windowsACLPolicy,
) error {
	rule, ok := windowsPolicyRule(policy)
	if !ok || !validWindowsSnapshot(snapshot, rule) {
		return errUnsafeWindowsACL
	}

	normalized := make([]aclACE, len(snapshot.ACEs))
	for index, ace := range snapshot.ACEs {
		if (ace.Kind != aclACEAllow && ace.Kind != aclACEDeny) ||
			ace.Flags&^aclACEValidFlags != 0 ||
			!validACLSID(ace.SID) {
			return errUnsafeWindowsACL
		}
		expanded, supported := expandWindowsGeneric(ace.Mask, snapshot.Object)
		if !supported {
			return errUnsafeWindowsACL
		}
		ace.Mask = expanded
		normalized[index] = ace

		if ace.Flags&aclACEInheritOnly != 0 {
			continue
		}
		if ace.Kind == aclACEAllow &&
			!trustedWindowsGrant(ace.SID, snapshot.Token.UserSID, rule) &&
			ace.Mask&rule.forbidden != 0 {
			return errUnsafeWindowsACL
		}
	}

	remaining := rule.required
	for _, ace := range normalized {
		if ace.Flags&aclACEInheritOnly != 0 {
			continue
		}
		switch ace.Kind {
		case aclACEUnknown:
			return errUnsafeWindowsACL
		case aclACEDeny:
			if tokenSIDAppliesToDeny(snapshot.Token, ace.SID) &&
				ace.Mask&remaining != 0 {
				return errUnsafeWindowsACL
			}
		case aclACEAllow:
			if tokenSIDAppliesToAllow(snapshot.Token, ace.SID) {
				remaining &^= ace.Mask
			}
		}
	}
	if remaining != 0 {
		return errUnsafeWindowsACL
	}
	return nil
}

func validWindowsSnapshot(
	snapshot windowsACLSnapshot,
	rule windowsACLRule,
) bool {
	if !snapshot.DescriptorSupported ||
		!snapshot.Token.Supported ||
		!snapshot.DACLPresent ||
		snapshot.DACLNull ||
		snapshot.Reparse ||
		snapshot.Object != rule.object ||
		!validACLSID(snapshot.OwnerSID) ||
		!validWindowsToken(snapshot.Token) ||
		!validWindowsFileID(snapshot.OpenedID) ||
		snapshot.OpenedID != snapshot.ReopenedID {
		return false
	}
	if _, err := windowsPathCaseKey(snapshot.CanonicalPath); err != nil {
		return false
	}
	if rule.ownerMustBeTokenUser {
		return snapshot.OwnerSID == snapshot.Token.UserSID
	}
	return trustedWindowsGrant(
		snapshot.OwnerSID,
		snapshot.Token.UserSID,
		rule,
	)
}

func validWindowsFileID(identity windowsFileID) bool {
	return identity.Volume != 0 || identity.Index != 0
}

func validWindowsToken(token aclTokenSnapshot) bool {
	if !validACLSID(token.UserSID) {
		return false
	}
	seen := make(map[string]struct{}, len(token.Groups))
	for _, group := range token.Groups {
		if !validACLSID(group.SID) ||
			(group.Enabled && group.DenyOnly) {
			return false
		}
		if _, duplicate := seen[group.SID]; duplicate {
			return false
		}
		seen[group.SID] = struct{}{}
	}
	return true
}

func validACLSID(sid string) bool {
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

func windowsPolicyRequired(
	policy windowsACLPolicy,
) (aclMask, aclObject, bool) {
	rule, ok := windowsPolicyRule(policy)
	return rule.required, rule.object, ok
}

func windowsPolicyRule(policy windowsACLPolicy) (windowsACLRule, bool) {
	switch policy {
	case windowsACLPolicyUnknown:
		return windowsACLRule{}, false
	case windowsExecutablePolicy:
		return windowsACLRule{
			object:                aclObjectFile,
			required:              aclExecutableRequired,
			forbidden:             aclIntegrityForbidden,
			trustTrustedInstaller: true,
		}, true
	case windowsPathDirectoryPolicy:
		return windowsACLRule{
			object:                aclObjectDirectory,
			required:              aclPathDirectoryRequired,
			forbidden:             aclIntegrityForbidden,
			trustTrustedInstaller: true,
		}, true
	case windowsPrivateAncestorPolicy:
		return windowsACLRule{
			object:                aclObjectDirectory,
			required:              aclPrivateAncestorRequired,
			forbidden:             aclAncestorForbidden,
			trustTrustedInstaller: true,
		}, true
	case windowsPathAncestorPolicy:
		return windowsACLRule{
			object:                aclObjectDirectory,
			required:              aclPathDirectoryRequired,
			forbidden:             aclAncestorForbidden,
			trustTrustedInstaller: true,
		}, true
	case windowsConfigHomePolicy:
		return windowsACLRule{
			object:               aclObjectDirectory,
			required:             aclConfigHomeRequired,
			forbidden:            aclConfidentialityForbidden,
			ownerMustBeTokenUser: true,
		}, true
	case windowsCredentialPolicy:
		return windowsACLRule{
			object:               aclObjectFile,
			required:             aclCredentialRequired,
			forbidden:            aclConfidentialityForbidden,
			ownerMustBeTokenUser: true,
		}, true
	default:
		return windowsACLRule{}, false
	}
}

func trustedWindowsGrant(
	sid string,
	tokenUser string,
	rule windowsACLRule,
) bool {
	if sid == tokenUser ||
		sid == aclLocalSystemSID ||
		sid == aclBuiltinAdministratorsSID {
		return true
	}
	return rule.trustTrustedInstaller && sid == aclTrustedInstallerSID
}

func tokenSIDAppliesToAllow(token aclTokenSnapshot, sid string) bool {
	if sid == token.UserSID {
		return true
	}
	for _, group := range token.Groups {
		if group.SID == sid && group.Enabled {
			return true
		}
	}
	return false
}

func tokenSIDAppliesToDeny(token aclTokenSnapshot, sid string) bool {
	if sid == token.UserSID {
		return true
	}
	for _, group := range token.Groups {
		if group.SID == sid && (group.Enabled || group.DenyOnly) {
			return true
		}
	}
	return false
}

func expandWindowsGeneric(mask aclMask, object aclObject) (aclMask, bool) {
	if object != aclObjectFile && object != aclObjectDirectory {
		return 0, false
	}
	concrete := mask &^ (aclGenericRead |
		aclGenericWrite |
		aclGenericExecute |
		aclGenericAll)
	if mask&aclGenericRead != 0 {
		concrete |= aclReadControl |
			aclReadData |
			aclReadEA |
			aclReadAttributes |
			aclSynchronize
	}
	if mask&aclGenericWrite != 0 {
		concrete |= aclReadControl |
			aclWriteData |
			aclAppendData |
			aclWriteEA |
			aclWriteAttributes |
			aclSynchronize
	}
	if mask&aclGenericExecute != 0 {
		concrete |= aclReadControl |
			aclExecute |
			aclReadAttributes |
			aclSynchronize
	}
	if mask&aclGenericAll != 0 {
		concrete |= aclConcreteRights
	}
	if concrete&^aclConcreteRights != 0 {
		return 0, false
	}
	return concrete, true
}

func windowsPathCaseKey(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", errInvalidWindowsPathKey
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
		return "", errInvalidWindowsPathKey
	}

	if isWindowsDrivePath(path) {
		components, ok := cleanWindowsPathComponents(
			strings.Split(path[3:], `\`),
		)
		if !ok {
			return "", errInvalidWindowsPathKey
		}
		root := strings.ToLower(path[:2]) + `\`
		if len(components) == 0 {
			return root, nil
		}
		return root + strings.Join(components, `\`), nil
	}

	if len(path) < 3 || !strings.HasPrefix(path, `\\`) || path[2] == '\\' {
		return "", errInvalidWindowsPathKey
	}
	parts := strings.Split(path[2:], `\`)
	rootParts := make([]string, 0, 2)
	tailStart := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		if len(rootParts) < 2 {
			if part == "." || part == ".." ||
				strings.ContainsRune(part, ':') {
				return "", errInvalidWindowsPathKey
			}
			rootParts = append(rootParts, strings.ToLower(part))
			tailStart = index + 1
			continue
		}
		break
	}
	if len(rootParts) != 2 {
		return "", errInvalidWindowsPathKey
	}
	tail, ok := cleanWindowsPathComponents(parts[tailStart:])
	if !ok {
		return "", errInvalidWindowsPathKey
	}
	key := `\\` + rootParts[0] + `\` + rootParts[1]
	if len(tail) != 0 {
		key += `\` + strings.Join(tail, `\`)
	}
	return key, nil
}

func isWindowsDrivePath(path string) bool {
	if len(path) < 3 || path[1] != ':' || path[2] != '\\' {
		return false
	}
	drive := path[0]
	return (drive >= 'a' && drive <= 'z') ||
		(drive >= 'A' && drive <= 'Z')
}

func cleanWindowsPathComponents(parts []string) ([]string, bool) {
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
			if strings.ContainsRune(part, ':') ||
				strings.IndexByte(part, 0) >= 0 {
				return nil, false
			}
			clean = append(clean, strings.ToLower(part))
		}
	}
	return clean, true
}
