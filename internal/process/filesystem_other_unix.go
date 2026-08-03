//go:build !windows && !linux

package process

import (
	"errors"
	"os"

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
	return firstStat.Dev == secondStat.Dev, nil
}
