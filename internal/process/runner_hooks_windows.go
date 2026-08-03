//go:build windows

package process

import "golang.org/x/sys/windows"

// windowsLaunchView is a deterministic integration-test seam. Production
// callers cannot mutate the allowlist; the runner supplies a value copy.
type windowsLaunchView struct {
	directory    string
	childHandles [3]windows.Handle
	// process is borrowed from the runner. It is zero before CreateProcess and
	// valid only for the duration of a post-create hook invocation.
	process windows.Handle
}

type windowsJobCloseView struct {
	job        windows.Handle
	accounting windowsJobAccounting
}

type windowsTerminalEventView struct {
	waitReady    <-chan struct{}
	timeoutReady <-chan struct{}
	overflowed   func() bool
}

type runnerHooks struct {
	beforeCreateProcess func(windowsLaunchView) error
	beforeResume        func(windowsLaunchView) error
	afterResume         func(windowsLaunchView)
	beforeCommit        func(windowsTerminalEventView)
	afterCommit         func(terminalState)
	beforeJobClose      func(windowsJobCloseView)
}
