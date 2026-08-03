//go:build windows

package process

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	fileDeleteChild = windows.ACCESS_MASK(0x00000040)
)

const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-" +
	"1853292631-2271478464"

func canonicalizeRootPath(path string) (string, error) {
	return path, nil
}

func createSecureDirectory(path string) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, _, err := secureAttributes()
	if err != nil {
		return err
	}
	return windows.CreateDirectory(pathPtr, attributes)
}

func bootstrapCreatedRootMode(_ string) error {
	return nil
}

func validateImmediateParentSecurity(path string, info fs.FileInfo) error {
	if err := validateWindowsFileType(info, true); err != nil {
		return err
	}
	return validateWindowsAncestorPath(path)
}

func validateRootAncestorSecurity(path string) error {
	for ancestor := filepath.Dir(path); ; ancestor = filepath.Dir(ancestor) {
		info, err := lstatWindowsDirectoryComponent(ancestor)
		if err != nil {
			return err
		}
		if err := validateImmediateParentSecurity(ancestor, info); err != nil {
			return err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil
		}
	}
}

func lstatNoLinkDirectory(path string) (fs.FileInfo, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("public directory path must be absolute")
	}
	var leaf fs.FileInfo
	for current := clean; ; current = filepath.Dir(current) {
		info, err := lstatWindowsDirectoryComponent(current)
		if err != nil {
			return nil, err
		}
		if current == clean {
			leaf = info
		}
		parent := filepath.Dir(current)
		if parent == current {
			return leaf, nil
		}
	}
}

func lstatWindowsDirectoryComponent(path string) (fs.FileInfo, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attributes, err := windows.GetFileAttributes(pathPtr)
	if err != nil {
		return nil, fmt.Errorf("inspect public directory attributes: %w", err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, errors.New("public directory path traverses a reparse point")
	}
	if attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return nil, errors.New("public directory path component is not a directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect public directory path: %w", err)
	}
	return info, nil
}

func validateWindowsAncestorPath(path string) error {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read Windows ancestor security: %w", err)
	}
	return validateWindowsAncestorDescriptor(sd)
}

func validateWindowsAncestorDescriptor(
	sd *windows.SECURITY_DESCRIPTOR,
) error {
	if sd == nil {
		return errors.New("missing Windows ancestor security descriptor")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read Windows ancestor owner: %w", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read process token user: %w", err)
	}
	ownerTrusted, err := trustedWindowsPrincipal(owner, user.User.Sid)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read Windows ancestor DACL: %w", err)
	}
	if dacl == nil {
		return errors.New("Windows ancestor has a permissive null DACL")
	}
	grants := make([]windowsAncestorGrant, 0, dacl.AceCount)
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read Windows ancestor DACL ACE: %w", err)
		}
		if ace == nil {
			return errors.New("missing Windows ancestor DACL ACE")
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
				continue
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			trusted, err := trustedWindowsPrincipal(sid, user.User.Sid)
			if err != nil {
				return err
			}
			grants = append(grants, windowsAncestorGrant{
				access:  uint32(ace.Mask),
				trusted: trusted,
			})
		default:
			return errors.New("unsupported Windows ancestor DACL ACE type")
		}
	}
	return validateWindowsAncestorAuthority(ownerTrusted, grants)
}

func trustedWindowsPrincipal(
	sid *windows.SID,
	user *windows.SID,
) (bool, error) {
	if sid == nil || user == nil {
		return false, nil
	}
	if sid.Equals(user) {
		return true, nil
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, err
	}
	if sid.Equals(system) {
		return true, nil
	}
	administrators, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		return false, err
	}
	if sid.Equals(administrators) {
		return true, nil
	}
	trustedInstaller, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		return false, err
	}
	return sid.Equals(trustedInstaller), nil
}

func validateOwnedPath(
	path string,
	info fs.FileInfo,
	wantDirectory bool,
	_ fs.FileMode,
) error {
	if err := validateWindowsFileType(info, wantDirectory); err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	return validateSecurityDescriptor(sd)
}

func validateOwnedFile(
	file *os.File,
	info fs.FileInfo,
	wantDirectory bool,
	_ fs.FileMode,
) error {
	if err := validateWindowsFileType(info, wantDirectory); err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	return validateSecurityDescriptor(sd)
}

func validateWindowsFileType(info fs.FileInfo, wantDirectory bool) error {
	if info == nil {
		return errors.New("missing file information")
	}
	if wantDirectory {
		if !info.IsDir() {
			return errors.New("path is not a directory")
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	return nil
}

func validateSecurityDescriptor(sd *windows.SECURITY_DESCRIPTOR) error {
	if sd == nil {
		return errors.New("missing Windows security descriptor")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("read Windows owner: %w", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read process token user: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		return err
	}
	if owner == nil ||
		(!owner.Equals(user.User.Sid) && !owner.Equals(administrators)) {
		return errors.New("path is not owned by a trusted runtime principal")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read Windows DACL: %w", err)
	}
	if dacl == nil {
		return errors.New("path has a permissive null DACL")
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	dangerous := windows.ACCESS_MASK(
		windows.GENERIC_WRITE|
			windows.GENERIC_ALL|
			windows.MAXIMUM_ALLOWED|
			windows.DELETE|
			windows.WRITE_DAC|
			windows.WRITE_OWNER|
			windows.FILE_WRITE_DATA|
			windows.FILE_APPEND_DATA|
			windows.FILE_WRITE_EA|
			windows.FILE_WRITE_ATTRIBUTES,
	) | fileDeleteChild
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read Windows DACL ACE: %w", err)
		}
		if ace == nil || ace.Mask&dangerous == 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if sid.Equals(user.User.Sid) ||
				sid.Equals(system) ||
				sid.Equals(administrators) {
				continue
			}
		}
		return errors.New("unsafe Windows DACL write grant")
	}
	return nil
}

func secureAttributes() (*windows.SecurityAttributes, *windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	sddl := fmt.Sprintf(
		"O:%[1]sD:P(A;OICI;FA;;;%[1]s)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
		user.User.Sid.String(),
	)
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	return attributes, sd, nil
}

func sameFilesystem(first, second *os.File) (bool, error) {
	if first == nil || second == nil {
		return false, errors.New("missing directory handle")
	}
	var firstInfo, secondInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(
		windows.Handle(first.Fd()),
		&firstInfo,
	); err != nil {
		return false, err
	}
	if err := windows.GetFileInformationByHandle(
		windows.Handle(second.Fd()),
		&secondInfo,
	); err != nil {
		return false, err
	}
	return firstInfo.VolumeSerialNumber == secondInfo.VolumeSerialNumber, nil
}

func forceCreatedMode(file *os.File, _ fs.FileMode) error {
	if file == nil {
		return errors.New("missing created file handle")
	}
	return nil
}

func lockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return classifyLockError(windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	))
}

func classifyLockError(err error) error {
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrRootLocked
	}
	return err
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&overlapped,
	)
}
