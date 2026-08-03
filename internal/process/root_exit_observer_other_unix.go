//go:build aix || illumos || solaris

package process

import "errors"

func observeRootExit(_ int) *rootExitObserver {
	return completedRootExitObserver(errors.New(
		"non-reaping provider root exit observation is unavailable",
	))
}
