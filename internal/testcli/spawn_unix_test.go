//go:build !windows

package testcli

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestSpawnSessionEscapeFailedPIDHandoffKillsAndWaits(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	writer := &capturingFailureWriter{}
	code := spawnSessionEscapeExecutable(executable, io.Discard, writer)
	pid, err := strconv.Atoi(strings.TrimSpace(writer.String()))
	if err != nil {
		t.Fatalf("captured PID %q: %v", writer.String(), err)
	}
	t.Cleanup(func() {
		killAndWaitForPID(t, pid)
	})
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("escaped child %d remains: %v", pid, err)
	}
}

func TestSpawnSessionEscapeClosedStderrExitsWithoutSIGPIPE(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	// Package path and arguments are test-owned; no shell is involved.
	//nolint:gosec,noctx
	cmd := exec.Command(executable, "--mode=spawn-session-escape")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := stderr.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	if waitErr == nil || cmd.ProcessState.ExitCode() != 1 {
		t.Fatalf(
			"wait=%v exit=%d",
			waitErr,
			cmd.ProcessState.ExitCode(),
		)
	}
}

type capturingFailureWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *capturingFailureWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.buffer.Write(data)
	return 0, syscall.EPIPE
}

func (w *capturingFailureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func killAndWaitForPID(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("process %d remains", pid)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
