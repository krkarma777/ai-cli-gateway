//go:build darwin || dragonfly || freebsd || netbsd || openbsd || aix || illumos || solaris

package process

func completedRootExitObserver(err error) *rootExitObserver {
	done := make(chan error, 1)
	ready := make(chan struct{})
	done <- err
	close(ready)
	return &rootExitObserver{
		done:  done,
		ready: ready,
	}
}
