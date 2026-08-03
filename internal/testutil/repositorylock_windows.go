//go:build windows

package testutil

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func lockRepositoryScanFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		&overlapped,
	)
}

func unlockRepositoryScanFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&overlapped,
	)
}

func sameRepositoryVolumeAlias(first, second string) bool {
	return strings.EqualFold(
		normalizeRepositoryWindowsVolume(first),
		normalizeRepositoryWindowsVolume(second),
	)
}

func normalizeRepositoryWindowsVolume(volume string) string {
	normalized := strings.ReplaceAll(volume, "/", `\`)
	lower := strings.ToLower(normalized)
	const (
		extendedPrefix    = `\\?\`
		extendedUNCPrefix = `\\?\unc\`
	)
	switch {
	case strings.HasPrefix(lower, extendedUNCPrefix):
		return `\\` + normalized[len(extendedUNCPrefix):]
	case strings.HasPrefix(lower, extendedPrefix):
		return normalized[len(extendedPrefix):]
	default:
		return normalized
	}
}
