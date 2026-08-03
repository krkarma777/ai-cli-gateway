//go:build windows

package testcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

func spawnSessionEscape(_ io.Writer, stderr io.Writer) int {
	_, _ = io.WriteString(
		stderr,
		"fake-ai-cli: session escape unsupported on windows\n",
	)
	return 2
}

func spawnIgnoreTermChild(_ io.Writer, stderr io.Writer) int {
	_, _ = io.WriteString(
		stderr,
		"fake-ai-cli: signal fixture unsupported on windows\n",
	)
	return 2
}

func ignoreTermReady(stderr io.Writer) int {
	_, _ = io.WriteString(
		stderr,
		"fake-ai-cli: signal fixture unsupported on windows\n",
	)
	return 2
}

func spawnGrandchildHold(stdout, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		return 1
	}
	// The fixture starts the same test executable directly, without a shell.
	//nolint:gosec,noctx
	command := exec.Command(executable, "--mode=spawn-grandchild-middle")
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return 1
	}
	if err := command.Process.Release(); err != nil {
		killAndWait(command)
		return 1
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(".fake-grandchild-ready"); err == nil {
			return 0
		} else if !errors.Is(err, os.ErrNotExist) {
			return 1
		}
		select {
		case <-deadline.C:
			return 1
		case <-ticker.C:
		}
	}
}

func spawnGrandchildMiddle(stdout, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		return 1
	}
	// The middle fixture starts the grandchild directly, without a shell.
	//nolint:gosec,noctx
	command := exec.Command(executable, "--mode=grandchild-hold")
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return 1
	}
	if _, err := fmt.Fprintf(
		stderr,
		"%d %d\n",
		os.Getpid(),
		command.Process.Pid,
	); err != nil {
		killAndWait(command)
		return 1
	}
	if err := os.WriteFile(
		".fake-grandchild-ready",
		[]byte("ready\n"),
		0o600,
	); err != nil {
		killAndWait(command)
		return 1
	}
	if err := command.Process.Release(); err != nil {
		killAndWait(command)
		return 1
	}
	blockUntilKilled()
	return 0
}

func killAndWait(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
	_ = command.Wait()
}
