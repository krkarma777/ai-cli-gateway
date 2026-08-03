//go:build darwin

package process

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

var _ [retainedZombieEPERMPolicy - 1]struct{}

func TestDarwinRetainedZombieEPERMPolicy(t *testing.T) {
	wrappedEPERM := errors.Join(errors.New("group"), unix.EPERM)
	if !retainedZombieSignalErrorMeansAbsent(wrappedEPERM) {
		t.Fatal("Darwin retained group signal EPERM was not classified absent")
	}
	if !retainedZombieProbeErrorMeansAbsent(wrappedEPERM, true) {
		t.Fatal("Darwin retained group probe EPERM was not classified absent")
	}
	if retainedZombieProbeErrorMeansAbsent(wrappedEPERM, false) {
		t.Fatal("Darwin post-Wait group probe EPERM was classified absent")
	}
	if retainedZombieSignalErrorMeansAbsent(unix.EACCES) ||
		retainedZombieProbeErrorMeansAbsent(unix.EACCES, true) {
		t.Fatal("Darwin non-EPERM error was classified absent")
	}
}
