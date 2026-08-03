//go:build linux

package process

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func sameFilesystem(first, second *os.File) (bool, error) {
	if first == nil || second == nil {
		return false, errors.New("missing directory handle")
	}
	var firstStat, secondStat unix.Stat_t
	if err := unix.Fstat(int(first.Fd()), &firstStat); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(second.Fd()), &secondStat); err != nil {
		return false, err
	}
	if firstStat.Dev != secondStat.Dev {
		return false, nil
	}
	firstMount, firstOK, err := linuxMountID(first)
	if err != nil {
		return false, err
	}
	secondMount, secondOK, err := linuxMountID(second)
	if err != nil {
		return false, err
	}
	if firstOK && secondOK {
		return firstMount == secondMount, nil
	}
	return true, nil
}

func linuxMountID(file *os.File) (uint64, bool, error) {
	var stat unix.Statx_t
	err := unix.Statx(
		int(file.Fd()),
		"",
		unix.AT_EMPTY_PATH|unix.AT_STATX_DONT_SYNC,
		unix.STATX_MNT_ID,
		&stat,
	)
	if errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.EOPNOTSUPP) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, false, nil
	}
	return stat.Mnt_id, true, nil
}
