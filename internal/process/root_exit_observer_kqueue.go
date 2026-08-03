//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package process

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func observeRootExit(pid int) *rootExitObserver {
	kqueue, err := unix.Kqueue()
	if err != nil {
		return completedRootExitObserver(fmt.Errorf(
			"open provider root exit observer: %w",
			err,
		))
	}
	alreadyExited, err := registerKqueueRootExit(
		kqueue,
		pid,
		unix.Kevent,
	)
	if err != nil {
		_ = unix.Close(kqueue)
		return completedRootExitObserver(fmt.Errorf(
			"register provider root exit observer: %w",
			err,
		))
	}
	if alreadyExited {
		_ = unix.Close(kqueue)
		return completedRootExitObserver(nil)
	}

	done := make(chan error, 1)
	ready := make(chan struct{})
	stop := make(chan struct{})
	finished := make(chan struct{})
	var publishOnce sync.Once
	publish := func(err error) {
		publishOnce.Do(func() {
			done <- err
			close(ready)
		})
	}
	go func() {
		defer close(finished)
		events := make([]unix.Kevent_t, 1)
		for {
			timeout := unix.NsecToTimespec((2 * time.Millisecond).Nanoseconds())
			count, err := unix.Kevent(kqueue, nil, events, &timeout)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				publish(fmt.Errorf(
					"observe provider root exit without reaping: %w",
					err,
				))
				return
			}
			if count != 0 {
				if events[0].Flags&unix.EV_ERROR != 0 {
					publish(fmt.Errorf(
						"observe provider root exit without reaping: "+
							"kevent error %d",
						events[0].Data,
					))
				} else {
					publish(nil)
				}
				return
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	return &rootExitObserver{
		done:  done,
		ready: ready,
		closeFn: func() error {
			close(stop)
			<-finished
			return unix.Close(kqueue)
		},
	}
}

func registerKqueueRootExit(
	kqueue int,
	pid int,
	kevent func(
		int,
		[]unix.Kevent_t,
		[]unix.Kevent_t,
		*unix.Timespec,
	) (int, error),
) (bool, error) {
	if kevent == nil {
		return false, errors.New("kqueue registration is unavailable")
	}
	change := unix.Kevent_t{}
	unix.SetKevent(
		&change,
		pid,
		unix.EVFILT_PROC,
		unix.EV_ADD|unix.EV_ONESHOT,
	)
	change.Fflags = unix.NOTE_EXIT
	_, err := kevent(
		kqueue,
		[]unix.Kevent_t{change},
		nil,
		nil,
	)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, unix.ESRCH):
		return true, nil
	default:
		return false, err
	}
}
