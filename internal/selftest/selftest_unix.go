//go:build !windows

package selftest

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

const readinessFD = 3

func runParentPlatform(
	executable string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return 1
	}
	defer func() {
		_ = readyReader.Close()
		_ = readyWriter.Close()
	}()
	// The self-test starts only the same resolved gateway executable with fixed
	// internal argv, no inherited environment, and one fixed control pipe.
	//nolint:gosec,noctx
	cmd := exec.Command(executable, internalMode, childMode)
	cmd.Env = []string{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{readyWriter}
	if err := cmd.Start(); err != nil {
		return 1
	}
	_ = readyWriter.Close()
	var ready [1]byte
	if _, err := io.ReadFull(readyReader, ready[:]); err != nil ||
		ready[0] != 1 {
		killAndWait(cmd)
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
	ready := os.NewFile(readinessFD, "self-test-readiness")
	if ready == nil {
		return 1
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	if err := writeAll(ready, []byte{1}); err != nil {
		signal.Stop(signals)
		_ = ready.Close()
		return 1
	}
	if err := ready.Close(); err != nil {
		signal.Stop(signals)
		return 1
	}
	<-signals
	signal.Stop(signals)
	return 0
}
