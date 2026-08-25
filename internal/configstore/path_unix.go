//go:build !windows

package configstore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

type nativeFileMetadata struct {
	device       uint64
	inode        uint64
	mode         uint32
	uid          uint32
	gid          uint32
	nlink        uint64
	size         int64
	modifiedSec  int64
	modifiedNsec int64
	changedSec   int64
	changedNsec  int64
}

type nativeDirectoryEvidence struct {
	path     string
	metadata nativeFileMetadata
}

func sameNativeDirectoryIdentity(left nativeFileMetadata, right nativeFileMetadata) bool {
	return left.device == right.device && left.inode == right.inode
}

func openNativeLoadTarget(input string) (nativeLoadTarget, error) {
	path, ok := cleanUnixStorePath(input)
	if !ok {
		return nativeLoadTarget{}, ErrUnsafePath
	}
	var selected unix.Stat_t
	err := unix.Lstat(path, &selected)
	if errors.Is(err, unix.ENOENT) {
		parent, missing, inspectErr := inspectUnixMissingPath(path)
		if inspectErr != nil {
			return nativeLoadTarget{}, inspectErr
		}
		return nativeLoadTarget{
			path: path, parent: parent, missing: missing,
		}, nil
	}
	if err != nil {
		return nativeLoadTarget{}, ErrStore
	}
	selectedMetadata, ok := unixStoreMetadata(selected)
	if !ok || !safeUnixStoreFile(selectedMetadata) {
		return nativeLoadTarget{}, ErrUnsafePath
	}
	parent, err := openUnixStoreDirectory(filepath.Dir(path), true)
	if err != nil {
		return nativeLoadTarget{}, err
	}
	parentFD, err := unix.Open(
		parent.path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nativeLoadTarget{}, ErrStore
	}
	defer func() { _ = unix.Close(parentFD) }()
	fd, err := unix.Openat(
		parentFD,
		filepath.Base(path),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nativeLoadTarget{}, ErrUnsafePath
	}
	file := os.NewFile(uintptr(fd), "configstore-source")
	if file == nil {
		_ = unix.Close(fd)
		return nativeLoadTarget{}, ErrStore
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = file.Close()
		return nativeLoadTarget{}, ErrStore
	}
	openedMetadata, ok := unixStoreMetadata(opened)
	if !ok || openedMetadata != selectedMetadata || !safeUnixStoreFile(openedMetadata) ||
		!revalidateUnixDirectory(parent, true) || !safeUnixStoreAncestors(path) {
		_ = file.Close()
		return nativeLoadTarget{}, ErrUnsafePath
	}
	return nativeLoadTarget{
		path: path, exists: true, file: file,
		metadata: openedMetadata, parent: parent,
	}, nil
}

func revalidateNativeLoadTarget(target nativeLoadTarget) bool {
	if target.path == "" || target.parent.path == "" {
		return false
	}
	if !target.exists {
		for _, path := range target.missing {
			var stat unix.Stat_t
			if err := unix.Lstat(path, &stat); !errors.Is(err, unix.ENOENT) {
				return false
			}
		}
		privateParent := target.parent.path == filepath.Dir(target.path)
		return revalidateUnixDirectory(target.parent, privateParent) &&
			safeUnixStoreAncestorsFrom(target.parent.path, privateParent)
	}
	if target.file == nil || !revalidateUnixDirectory(target.parent, true) ||
		!safeUnixStoreAncestors(target.path) {
		return false
	}
	var handle unix.Stat_t
	if err := unix.Fstat(int(target.file.Fd()), &handle); err != nil {
		return false
	}
	handleMetadata, ok := unixStoreMetadata(handle)
	if !ok || handleMetadata != target.metadata || !safeUnixStoreFile(handleMetadata) {
		return false
	}
	var selected unix.Stat_t
	if err := unix.Lstat(target.path, &selected); err != nil {
		return false
	}
	selectedMetadata, ok := unixStoreMetadata(selected)
	return ok && selectedMetadata == target.metadata && safeUnixStoreFile(selectedMetadata)
}

func cleanUnixStorePath(path string) (string, bool) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return "", false
	}
	clean := filepath.Clean(path)
	return clean, clean == path && filepath.Base(clean) != "." && filepath.Base(clean) != string(filepath.Separator)
}

func nativeStorePathKey(path string) (string, bool) {
	return cleanUnixStorePath(path)
}

func inspectNativePrivateDirectory(path string) (bool, error) {
	clean, ok := cleanUnixStorePath(path)
	if !ok {
		return false, ErrUnsafePath
	}
	var selected unix.Stat_t
	err := unix.Lstat(clean, &selected)
	if err == nil {
		evidence, openErr := openUnixStoreDirectory(clean, true)
		if openErr != nil {
			return false, openErr
		}
		if !revalidateUnixDirectory(evidence, true) ||
			!safeUnixStoreAncestorsFrom(clean, true) {
			return false, ErrUnsafePath
		}
		return true, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return false, ErrStore
	}
	probe := filepath.Join(clean, ".configstore-probe")
	evidence, missing, inspectErr := inspectUnixMissingPath(probe)
	if inspectErr != nil {
		return false, inspectErr
	}
	target := nativeLoadTarget{path: probe, parent: evidence, missing: missing}
	if !revalidateNativeLoadTarget(target) {
		return false, ErrUnsafePath
	}
	return false, nil
}

func inspectUnixMissingPath(path string) (nativeDirectoryEvidence, []string, error) {
	missing := []string{path}
	current := filepath.Dir(path)
	private := true
	for {
		var selected unix.Stat_t
		err := unix.Lstat(current, &selected)
		if err == nil {
			evidence, openErr := openUnixStoreDirectory(current, private)
			if openErr != nil {
				return nativeDirectoryEvidence{}, nil, openErr
			}
			if !safeUnixStoreAncestorsFrom(current, private) {
				return nativeDirectoryEvidence{}, nil, ErrUnsafePath
			}
			return evidence, missing, nil
		}
		if !errors.Is(err, unix.ENOENT) {
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

func openUnixStoreDirectory(path string, private bool) (nativeDirectoryEvidence, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nativeDirectoryEvidence{}, ErrUnsafePath
	}
	defer func() { _ = unix.Close(fd) }()
	var handle unix.Stat_t
	var selected unix.Stat_t
	if unix.Fstat(fd, &handle) != nil || unix.Lstat(path, &selected) != nil {
		return nativeDirectoryEvidence{}, ErrStore
	}
	handleMetadata, handleOK := unixStoreMetadata(handle)
	selectedMetadata, selectedOK := unixStoreMetadata(selected)
	if !handleOK || !selectedOK || handleMetadata != selectedMetadata ||
		!safeUnixStoreDirectory(handleMetadata, private) {
		return nativeDirectoryEvidence{}, ErrUnsafePath
	}
	return nativeDirectoryEvidence{path: path, metadata: handleMetadata}, nil
}

func revalidateUnixDirectory(evidence nativeDirectoryEvidence, private bool) bool {
	if evidence.path == "" {
		return false
	}
	var selected unix.Stat_t
	if unix.Lstat(evidence.path, &selected) != nil {
		return false
	}
	metadata, ok := unixStoreMetadata(selected)
	return ok && metadata == evidence.metadata && safeUnixStoreDirectory(metadata, private)
}

func safeUnixStoreAncestors(path string) bool {
	return safeUnixStoreAncestorsFrom(filepath.Dir(path), true)
}

func safeUnixStoreAncestorsFrom(start string, privateFirst bool) bool {
	current := start
	first := true
	for {
		var stat unix.Stat_t
		if unix.Lstat(current, &stat) != nil {
			return false
		}
		metadata, ok := unixStoreMetadata(stat)
		if !ok || !safeUnixStoreDirectory(metadata, privateFirst && first) {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return true
		}
		current = parent
		first = false
	}
}

func safeUnixStoreFile(metadata nativeFileMetadata) bool {
	permissions := metadata.mode & 0o777
	return metadata.mode&unix.S_IFMT == unix.S_IFREG &&
		metadata.uid == uint32(os.Geteuid()) && //nolint:gosec // Kernel UIDs use uint32.
		(permissions == 0o400 || permissions == 0o600) &&
		metadata.mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) == 0 &&
		metadata.nlink == 1
}

func safeUnixStoreDirectory(metadata nativeFileMetadata, private bool) bool {
	if metadata.mode&unix.S_IFMT != unix.S_IFDIR ||
		metadata.mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return false
	}
	if private {
		return metadata.uid == uint32(os.Geteuid()) && //nolint:gosec // Kernel UIDs use uint32.
			metadata.mode&0o777 == 0o700
	}
	return (metadata.uid == 0 || metadata.uid == uint32(os.Geteuid())) && //nolint:gosec // Kernel UIDs use uint32.
		metadata.mode&0o022 == 0
}

func unixStoreMetadata(stat unix.Stat_t) (nativeFileMetadata, bool) {
	device, ok := nativeUnsignedInteger(reflect.ValueOf(stat.Dev))
	if !ok {
		return nativeFileMetadata{}, false
	}
	modifiedSec, modifiedNsec, modifiedOK := nativeUnixTime(stat, "Mtim", "Mtimespec", "Mtime", "Mtimensec", "MtimeNsec")
	changedSec, changedNsec, changedOK := nativeUnixTime(stat, "Ctim", "Ctimespec", "Ctime", "Ctimensec", "CtimeNsec")
	if !modifiedOK || !changedOK {
		return nativeFileMetadata{}, false
	}
	return nativeFileMetadata{
		device:       device,
		inode:        stat.Ino,
		mode:         uint32(stat.Mode),
		uid:          stat.Uid,
		gid:          stat.Gid,
		nlink:        uint64(stat.Nlink), //nolint:gosec // Successful native stat link counts are nonnegative.
		size:         stat.Size,
		modifiedSec:  modifiedSec,
		modifiedNsec: modifiedNsec,
		changedSec:   changedSec,
		changedNsec:  changedNsec,
	}, true
}

func nativeUnixTime(
	stat unix.Stat_t,
	structName string,
	alternateStructName string,
	secondsName string,
	nanosName string,
	alternateNanosName string,
) (int64, int64, bool) {
	value := reflect.ValueOf(stat)
	for _, name := range []string{structName, alternateStructName} {
		field := value.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.Struct {
			continue
		}
		seconds, secondsOK := nativeSignedInteger(field.FieldByName("Sec"))
		nanos, nanosOK := nativeSignedInteger(field.FieldByName("Nsec"))
		if secondsOK && nanosOK {
			return seconds, nanos, true
		}
	}
	seconds, secondsOK := nativeSignedInteger(value.FieldByName(secondsName))
	for _, name := range []string{nanosName, alternateNanosName} {
		nanos, nanosOK := nativeSignedInteger(value.FieldByName(name))
		if secondsOK && nanosOK {
			return seconds, nanos, true
		}
	}
	return 0, 0, false
}

func nativeUnsignedInteger(value reflect.Value) (uint64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Int() < 0 {
			if value.Kind() == reflect.Int32 {
				return uint64(uint32(value.Int())), true // #nosec G115 -- preserves Darwin dev_t identity bits.
			}
			return 0, false
		}
		return uint64(value.Int()), true //nolint:gosec // Negative values were rejected above.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Invalid, reflect.Bool,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Pointer, reflect.Slice, reflect.String,
		reflect.Struct, reflect.UnsafePointer:
		return 0, false
	default:
		return 0, false
	}
}

func nativeSignedInteger(value reflect.Value) (int64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(value.Uint()), true //nolint:gosec // Native time fields fit int64.
	case reflect.Invalid, reflect.Bool, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Pointer, reflect.Slice, reflect.String,
		reflect.Struct, reflect.UnsafePointer:
		return 0, false
	default:
		return 0, false
	}
}
