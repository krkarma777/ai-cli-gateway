//go:build darwin

package process

import (
	"errors"

	"golang.org/x/sys/unix"
)

const retainedZombieEPERMPolicy = 1

func retainedZombieSignalErrorMeansAbsent(err error) bool {
	return errors.Is(err, unix.EPERM)
}

func retainedZombieProbeErrorMeansAbsent(
	err error,
	retainedLeader bool,
) bool {
	return retainedLeader && errors.Is(err, unix.EPERM)
}
