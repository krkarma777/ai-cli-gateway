//go:build !windows

package gatewaykey

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

func loadFile(
	path string,
	distinctFrom []fs.FileInfo,
	parse snapshotParser,
) (Snapshot, error) {
	return loadFileWithUnixOpen(path, distinctFrom, parse, unix.Open)
}

type unixOpenFile func(string, int, uint32) (int, error)

func loadFileWithUnixOpen(
	path string,
	distinctFrom []fs.FileInfo,
	parse snapshotParser,
	open unixOpenFile,
) (Snapshot, error) {
	clean, ok := cleanUnixKeyPath(path)
	if !ok || parse == nil || open == nil || !safeUnixKeyAncestors(clean) ||
		!safeUnixKeyPath(clean) {
		return Snapshot{}, ErrUnavailable
	}

	fd, err := open(
		clean,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "gateway-key")
	if file == nil {
		_ = unix.Close(fd)
		return Snapshot{}, ErrUnavailable
	}

	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()

	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil ||
		!safeUnixKeyStat(opened) ||
		!sameUnixPathIdentity(clean, opened) ||
		!safeUnixKeyAncestors(clean) {
		return Snapshot{}, ErrUnavailable
	}
	handleInfo, err := file.Stat()
	if err != nil || !distinctUnixKeyIdentity(handleInfo, distinctFrom) {
		return Snapshot{}, ErrUnavailable
	}
	openedMetadata := unixKeyMetadataFrom(opened, handleInfo)

	snapshot, err := parse(file)
	if err != nil || !snapshot.Valid() || !snapshot.Enabled() {
		return Snapshot{}, ErrUnavailable
	}

	var after unix.Stat_t
	afterInfo, statErr := file.Stat()
	if err := unix.Fstat(fd, &after); err != nil ||
		!sameUnixIdentity(opened, after) ||
		statErr != nil ||
		!sameUnixKeyMetadata(openedMetadata, unixKeyMetadataFrom(after, afterInfo)) ||
		!safeUnixKeyStat(after) ||
		!sameUnixPathIdentity(clean, after) ||
		!safeUnixKeyAncestors(clean) {
		return Snapshot{}, ErrUnavailable
	}
	if err := file.Close(); err != nil {
		closed = true
		return Snapshot{}, ErrUnavailable
	}
	closed = true
	return snapshot, nil
}

type unixKeyMetadata struct {
	mode           uint32
	uid            uint32
	nlink          uint64
	size           int64
	modTimeNanos   int64
	changeSeconds  int64
	changeNanos    int64
	changeTimeSeen bool
}

func unixKeyMetadataFrom(stat unix.Stat_t, info fs.FileInfo) unixKeyMetadata {
	metadata := unixKeyMetadata{
		mode:  uint32(stat.Mode),
		uid:   stat.Uid,
		nlink: uint64(stat.Nlink),
	}
	if info != nil {
		metadata.size = info.Size()
		metadata.modTimeNanos = info.ModTime().UnixNano()
	}
	metadata.changeSeconds, metadata.changeNanos, metadata.changeTimeSeen =
		unixKeyChangeTime(stat)
	return metadata
}

func sameUnixKeyMetadata(left, right unixKeyMetadata) bool {
	if left.mode != right.mode || left.uid != right.uid ||
		left.nlink != right.nlink || left.size != right.size ||
		left.modTimeNanos != right.modTimeNanos ||
		left.changeTimeSeen != right.changeTimeSeen {
		return false
	}
	return !left.changeTimeSeen ||
		(left.changeSeconds == right.changeSeconds &&
			left.changeNanos == right.changeNanos)
}

func unixKeyChangeTime(stat unix.Stat_t) (int64, int64, bool) {
	value := reflect.ValueOf(stat)
	for _, name := range []string{"Ctim", "Ctimespec"} {
		field := value.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.Struct {
			continue
		}
		seconds, secondsOK := unixKeyIntegerField(field.FieldByName("Sec"))
		nanos, nanosOK := unixKeyIntegerField(field.FieldByName("Nsec"))
		if secondsOK && nanosOK {
			return seconds, nanos, true
		}
	}
	seconds, secondsOK := unixKeyIntegerField(value.FieldByName("Ctime"))
	for _, name := range []string{"Ctimensec", "CtimeNsec"} {
		nanos, nanosOK := unixKeyIntegerField(value.FieldByName(name))
		if secondsOK && nanosOK {
			return seconds, nanos, true
		}
	}
	return 0, 0, false
}

func unixKeyIntegerField(value reflect.Value) (int64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(value.Uint()), true //nolint:gosec // Native time fields fit in int64.
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

func safeUnixKeyPath(path string) bool {
	var stat unix.Stat_t
	return unix.Lstat(path, &stat) == nil && safeUnixKeyStat(stat)
}

func cleanUnixKeyPath(path string) (string, bool) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return "", false
	}
	clean := filepath.Clean(path)
	return clean, filepath.IsAbs(clean)
}

func safeUnixKeyStat(stat unix.Stat_t) bool {
	permissions := uint32(stat.Mode) & 0o777
	return uint32(stat.Mode)&unix.S_IFMT == unix.S_IFREG &&
		stat.Uid == uint32(os.Geteuid()) && //nolint:gosec // Kernel UIDs use uint32.
		(permissions == 0o400 || permissions == 0o600) &&
		uint32(stat.Mode)&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) == 0 &&
		uint64(stat.Nlink) == 1
}

func safeUnixKeyAncestors(path string) bool {
	current := filepath.Dir(path)
	for {
		var stat unix.Stat_t
		if err := unix.Lstat(current, &stat); err != nil ||
			uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR ||
			(stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || //nolint:gosec // Kernel UIDs use uint32.
			uint32(stat.Mode)&0o022 != 0 {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return true
		}
		current = parent
	}
}

func sameUnixPathIdentity(path string, handle unix.Stat_t) bool {
	var current unix.Stat_t
	return unix.Lstat(path, &current) == nil &&
		safeUnixKeyStat(current) &&
		sameUnixIdentity(handle, current)
}

func sameUnixIdentity(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}

func distinctUnixKeyIdentity(
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
