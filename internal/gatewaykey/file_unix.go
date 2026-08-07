//go:build !windows

package gatewaykey

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func loadFile(
	path string,
	distinctFrom []fs.FileInfo,
	parse snapshotParser,
) (Snapshot, error) {
	clean, ok := cleanUnixKeyPath(path)
	if !ok || parse == nil || !safeUnixKeyAncestors(clean) ||
		!safeUnixKeyPath(clean) {
		return Snapshot{}, ErrUnavailable
	}

	fd, err := unix.Open(
		clean,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
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

	snapshot, err := parse(file)
	if err != nil || !snapshot.Valid() || !snapshot.Enabled() {
		return Snapshot{}, ErrUnavailable
	}

	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil ||
		!sameUnixIdentity(opened, after) ||
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
		uint32(stat.Uid) == uint32(os.Geteuid()) && //nolint:gosec // Kernel UIDs use uint32.
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
			(uint32(stat.Uid) != 0 && uint32(stat.Uid) != uint32(os.Geteuid())) || //nolint:gosec // Kernel UIDs use uint32.
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
	return uint64(left.Dev) == uint64(right.Dev) &&
		uint64(left.Ino) == uint64(right.Ino)
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
