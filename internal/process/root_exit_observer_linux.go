//go:build linux

package process

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func observeRootExit(pid int) *rootExitObserver {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
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
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			var info unix.Siginfo
			err := unix.Waitid(
				unix.P_PID,
				pid,
				&info,
				unix.WEXITED|unix.WNOHANG|unix.WNOWAIT,
				nil,
			)
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
			if info.Signo != 0 {
				publish(nil)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return &rootExitObserver{
		done:  done,
		ready: ready,
		closeFn: func() error {
			cancel()
			<-finished
			return nil
		},
	}
}
