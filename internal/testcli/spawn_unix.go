//go:build !windows

package testcli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func spawnIgnoreTermChild(stdout, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: executable unavailable\n")
		return 1
	}
	// The fake deliberately starts itself directly, never through a shell.
	//nolint:gosec,noctx
	cmd := exec.Command(executable, "--mode=ignore-term-ready")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: child start failed\n")
		return 1
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(".fake-child-ready"); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			killAndWait(cmd)
			return 1
		}
		select {
		case <-deadline.C:
			killAndWait(cmd)
			return 1
		case <-ticker.C:
		}
	}
	_, _ = io.WriteString(stderr, strconv.Itoa(cmd.Process.Pid)+"\n")
	blockUntilKilled()
	return 0
}

func ignoreTermReady(stderr io.Writer) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGTERM)
	defer signal.Stop(signals)
	if err := os.WriteFile(
		".fake-child-ready",
		[]byte("ready\n"),
		0o600,
	); err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: ready failure\n")
		return 1
	}
	for {
		<-signals
	}
}

func spawnGrandchildHold(_ io.Writer, stderr io.Writer) int {
	_, _ = io.WriteString(
		stderr,
		"fake-ai-cli: grandchild fixture unsupported on unix\n",
	)
	return 2
}

func spawnGrandchildMiddle(_ io.Writer, stderr io.Writer) int {
	_, _ = io.WriteString(
		stderr,
		"fake-ai-cli: grandchild fixture unsupported on unix\n",
	)
	return 2
}

func spawnSessionEscape(stdout, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: executable unavailable\n")
		return 1
	}
	return spawnSessionEscapeExecutable(executable, stdout, stderr)
}

func spawnSessionEscapeExecutable(
	executable string,
	stdout, stderr io.Writer,
) int {
	// The deliberate setsid escape documents the Unix process-group boundary.
	// The integration harness owns, kills, and waits for the printed PID.
	//nolint:gosec,noctx
	cmd := exec.Command(executable, "--mode=session-escape")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: session start failed\n")
		return 1
	}
	handoff, closeHandoff, err := duplicatePIDHandoffWriter(stderr)
	if err != nil {
		killAndWait(cmd)
		return 1
	}
	pidLine := strconv.AppendInt(nil, int64(cmd.Process.Pid), 10)
	pidLine = append(pidLine, '\n')
	writeErr := writeAll(handoff, pidLine)
	closeErr := closeHandoff()
	if writeErr != nil || closeErr != nil {
		killAndWait(cmd)
		return 1
	}
	if err := cmd.Process.Release(); err != nil {
		killAndWait(cmd)
		return 1
	}
	return 0
}

func duplicatePIDHandoffWriter(
	stderr io.Writer,
) (io.Writer, func() error, error) {
	file, ok := stderr.(*os.File)
	if !ok || file.Fd() > uintptr(syscall.Stderr) {
		return stderr, func() error { return nil }, nil
	}
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		return nil, nil, err
	}
	unix.CloseOnExec(fd)
	duplicate := os.NewFile(uintptr(fd), "fake-ai-cli-pid-handoff")
	if duplicate == nil {
		_ = unix.Close(fd)
		return nil, nil, syscall.EBADF
	}
	return duplicate, duplicate.Close, nil
}

func killAndWait(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
