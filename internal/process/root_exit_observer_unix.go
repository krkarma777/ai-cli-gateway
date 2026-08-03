//go:build !windows

package process

import "sync"

// rootExitObserver reports root exit without reaping it. Keeping the verified
// group leader waitable prevents its PID/PGID from being reused until every
// possible process-group signal decision is complete.
type rootExitObserver struct {
	done      <-chan error
	ready     <-chan struct{}
	closeOnce sync.Once
	closeFn   func() error
	closeErr  error
}

func (o *rootExitObserver) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		if o.closeFn != nil {
			o.closeErr = o.closeFn()
		}
	})
	return o.closeErr
}
