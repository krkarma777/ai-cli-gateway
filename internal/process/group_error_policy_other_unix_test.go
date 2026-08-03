//go:build !darwin && !windows

package process

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

var _ [0 - retainedZombieEPERMPolicy]struct{}

func TestNonDarwinRetainedZombieEPERMPolicy(t *testing.T) {
	wrappedEPERM := errors.Join(errors.New("group"), unix.EPERM)
	if retainedZombieSignalErrorMeansAbsent(wrappedEPERM) {
		t.Fatal("non-Darwin group signal EPERM was suppressed")
	}
	if retainedZombieProbeErrorMeansAbsent(wrappedEPERM, true) {
		t.Fatal("non-Darwin retained group probe EPERM was classified absent")
	}
	if retainedZombieProbeErrorMeansAbsent(wrappedEPERM, false) {
		t.Fatal("non-Darwin post-Wait group probe EPERM was classified absent")
	}
}
