//go:build darwin

package process

import (
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRegisterKqueueRootExitTreatsESRCHAsAlreadyExited(
	t *testing.T,
) {
	alreadyExited, err := registerKqueueRootExit(
		123,
		4321,
		func(
			int,
			[]unix.Kevent_t,
			[]unix.Kevent_t,
			*unix.Timespec,
		) (int, error) {
			return 0, fmt.Errorf("register fixture: %w", unix.ESRCH)
		},
	)
	if err != nil {
		t.Fatalf("already-exited registration rejected: %v", err)
	}
	if !alreadyExited {
		t.Fatal("ESRCH registration was not classified as already exited")
	}

	registrationFailure := errors.New("fixed kqueue registration failure")
	alreadyExited, err = registerKqueueRootExit(
		123,
		4321,
		func(
			int,
			[]unix.Kevent_t,
			[]unix.Kevent_t,
			*unix.Timespec,
		) (int, error) {
			return 0, registrationFailure
		},
	)
	if alreadyExited || !errors.Is(err, registrationFailure) {
		t.Fatalf(
			"registration failure classified exited=%v error=%v",
			alreadyExited,
			err,
		)
	}
}
