//go:build windows

package selftest

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
)

func runParentPlatform(
	executable string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	// Task 9 supplies Windows containment. Keep the hidden mode compilable
	// without claiming the Unix readiness protocol on this platform.
	//nolint:gosec,noctx
	cmd := exec.Command(executable, internalMode, childMode)
	cmd.Env = []string{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return 1
	}
	if err := writeAll(stdout, []byte("ready\n")); err != nil {
		killAndWait(cmd)
		return 1
	}
	if err := cmd.Process.Release(); err != nil {
		killAndWait(cmd)
		return 1
	}
	return 0
}

func runChild() int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	<-signals
	signal.Stop(signals)
	return 0
}
