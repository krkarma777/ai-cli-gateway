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
		uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return sourceMetadata{}, false
	}
	changeSeconds, changeNanos, changeSeen := unixSourceChangeTime(stat)
	if !changeSeen {
		return sourceMetadata{}, false
	}
	return sourceMetadata{
		device: uint64(stat.Dev), inode: uint64(stat.Ino), mode: uint32(stat.Mode),
		uid: uint32(stat.Uid), gid: uint32(stat.Gid), nlink: uint64(stat.Nlink),
		size: info.Size(), modTimeNanos: info.ModTime().UnixNano(),
		changeSeconds: changeSeconds, changeNanos: changeNanos,
	}, true
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
	default:
		return 0, false
	}
}
