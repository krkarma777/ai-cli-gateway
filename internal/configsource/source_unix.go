//go:build !windows

package configsource

import (
	"io/fs"
	"os"
	"reflect"

	"golang.org/x/sys/unix"
)

type sourceMetadata struct {
	device        uint64
	inode         uint64
	mode          uint32
	uid           uint32
	gid           uint32
	nlink         uint64
	size          int64
	modTimeNanos  int64
	changeSeconds int64
	changeNanos   int64
}

type unixModeField interface {
	~uint16 | ~uint32
}

type unixLinkCountField interface {
	~int16 | ~uint16 | ~uint32 | ~uint64
}

func normalizedUnixMode[T unixModeField](mode T) uint32 {
	return uint32(mode)
}

func normalizedUnixLinkCount[T unixLinkCountField](count T) uint64 {
	return uint64(count) //nolint:gosec // AIX nlink_t is signed, but successful stat link counts are nonnegative.
}

func openSourceFile(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "configuration-source")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrUnavailable
	}
	return file, nil
}

func platformSourceMetadata(path string, file *os.File) (sourceMetadata, bool) {
	if file == nil {
		return sourceMetadata{}, false
	}
	var handle unix.Stat_t
	handleInfo, infoErr := file.Stat()
	if err := unix.Fstat(int(file.Fd()), &handle); err != nil || infoErr != nil {
		return sourceMetadata{}, false
	}
	var selected unix.Stat_t
	selectedInfo, pathErr := os.Lstat(path)
	if err := unix.Lstat(path, &selected); err != nil || pathErr != nil {
		return sourceMetadata{}, false
	}
	handleMetadata, handleOK := unixSourceMetadata(handle, handleInfo)
	selectedMetadata, selectedOK := unixSourceMetadata(selected, selectedInfo)
	if !handleOK || !selectedOK || !sameSourceMetadata(handleMetadata, selectedMetadata) {
		return sourceMetadata{}, false
	}
	return handleMetadata, true
}

func unixSourceMetadata(stat unix.Stat_t, info fs.FileInfo) (sourceMetadata, bool) {
	if info == nil || !info.Mode().IsRegular() ||
		normalizedUnixMode(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return sourceMetadata{}, false
	}
	device, deviceOK := checkedUnixSourceDevice(stat.Dev)
	if !deviceOK {
		return sourceMetadata{}, false
	}
	changeSeconds, changeNanos, changeSeen := unixSourceChangeTime(stat)
	if !changeSeen {
		return sourceMetadata{}, false
	}
	return sourceMetadata{
		device: device, inode: stat.Ino, mode: normalizedUnixMode(stat.Mode),
		uid: stat.Uid, gid: stat.Gid, nlink: normalizedUnixLinkCount(stat.Nlink),
		size: info.Size(), modTimeNanos: info.ModTime().UnixNano(),
		changeSeconds: changeSeconds, changeNanos: changeNanos,
	}, true
}

func checkedUnixSourceDevice[
	T ~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr,
](device T) (uint64, bool) {
	value := reflect.ValueOf(device)
	switch value.Kind() {
	case reflect.Int32:
		return uint64(uint32(value.Int())), true // #nosec G115 -- Darwin dev_t is signed int32; uint32 preserves its kernel identity bit pattern.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int64:
		signed := value.Int()
		if signed < 0 {
			return 0, false
		}
		return uint64(signed), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Uint64, reflect.Uintptr:
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

func sameSourceMetadata(left, right sourceMetadata) bool {
	return left == right
}

func unixSourceChangeTime(stat unix.Stat_t) (int64, int64, bool) {
	value := reflect.ValueOf(stat)
	for _, name := range []string{"Ctim", "Ctimespec"} {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		seconds, secondsOK := unixSourceIntegerField(field.FieldByName("Sec"))
		nanos, nanosOK := unixSourceIntegerField(field.FieldByName("Nsec"))
		if secondsOK && nanosOK {
			return seconds, nanos, true
		}
	}
	seconds, secondsOK := unixSourceIntegerField(value.FieldByName("Ctime"))
	for _, name := range []string{"Ctimensec", "CtimeNsec"} {
		nanos, nanosOK := unixSourceIntegerField(value.FieldByName(name))
		if secondsOK && nanosOK {
			return seconds, nanos, true
		}
	}
	return 0, 0, false
}

func unixSourceIntegerField(value reflect.Value) (int64, bool) {
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
