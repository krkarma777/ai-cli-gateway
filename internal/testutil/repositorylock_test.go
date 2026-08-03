package testutil

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	repositoryLockHelperModeEnv   = "AI_CLI_GATEWAY_TEST_REPOSITORY_LOCK_MODE"
	repositoryLockHelperSharedEnv = "AI_CLI_GATEWAY_TEST_REPOSITORY_LOCK_SHARED"
)

func TestRepositoryScanLockSerializesMutationCleanupBeforeScan(t *testing.T) {
	shared := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	owner := startRepositoryLockHelper(ctx, t, "owner", shared)
	owner.expectLine(t, "owner-ready")

	scanner := startRepositoryLockHelper(ctx, t, "scanner", shared)
	scanner.expectLine(t, "scanner-attempting")

	if _, err := io.WriteString(owner.stdin, "release\n"); err != nil {
		t.Fatalf("release owner helper: %v", err)
	}
	if err := owner.stdin.Close(); err != nil {
		t.Fatalf("close owner helper stdin: %v", err)
	}
	owner.wait(t)

	scanner.expectLine(t, "scanner-clean")
	scanner.wait(t)
}

func TestRepositoryScanLockHelperProcess(t *testing.T) {
	mode := os.Getenv(repositoryLockHelperModeEnv)
	if mode == "" {
		t.Skip("repository lock helper process")
	}
	shared := os.Getenv(repositoryLockHelperSharedEnv)
	if shared == "" {
		t.Fatal("missing repository lock helper directory")
	}
	marker := filepath.Join(shared, "mutation")

	switch mode {
	case "owner":
		AcquireRepositoryScanLock(t)
		//nolint:gosec // The parent test passes its exact test-owned temporary directory.
		if err := os.WriteFile(marker, []byte("present"), 0o600); err != nil {
			t.Fatalf("create mutation marker: %v", err)
		}
		t.Cleanup(func() {
			//nolint:gosec // The parent test passes its exact test-owned temporary directory.
			if err := os.Remove(marker); err != nil {
				t.Errorf("remove mutation marker: %v", err)
			}
		})
		if _, err := fmt.Fprintln(os.Stdout, "owner-ready"); err != nil {
			t.Fatalf("signal owner readiness: %v", err)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatalf("wait for owner release: %v", err)
		}
	case "scanner":
		if _, err := fmt.Fprintln(os.Stdout, "scanner-attempting"); err != nil {
			t.Fatalf("signal scanner attempt: %v", err)
		}
		AcquireRepositoryScanLock(t)
		//nolint:gosec // The parent test passes its exact test-owned temporary directory.
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatalf("mutation marker remained visible after lock acquisition: %v", err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "scanner-clean"); err != nil {
			t.Fatalf("signal clean scan: %v", err)
		}
	default:
		t.Fatalf("unknown repository lock helper mode %q", mode)
	}
}

type repositoryLockHelper struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  bytes.Buffer
}

func startRepositoryLockHelper(
	ctx context.Context,
	t *testing.T,
	mode string,
	shared string,
) *repositoryLockHelper {
	t.Helper()
	command := exec.CommandContext( //nolint:gosec // The test re-executes its exact test binary with fixed arguments.
		ctx,
		os.Args[0],
		"-test.run=^TestRepositoryScanLockHelperProcess$",
	)
	command.Env = append(
		os.Environ(),
		repositoryLockHelperModeEnv+"="+mode,
		repositoryLockHelperSharedEnv+"="+shared,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("create %s helper stdin: %v", mode, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("create %s helper stdout: %v", mode, err)
	}
	helper := &repositoryLockHelper{
		command: command,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
	}
	command.Stderr = &helper.stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start %s helper: %v", mode, err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	return helper
}

func (helper *repositoryLockHelper) expectLine(t *testing.T, want string) {
	t.Helper()
	line, err := helper.stdout.ReadString('\n')
	if err != nil {
		t.Fatalf(
			"read helper output: %v; stderr=%q",
			err,
			helper.stderr.String(),
		)
	}
	if got := strings.TrimSpace(line); got != want {
		t.Fatalf("helper output = %q, want %q", got, want)
	}
}

func (helper *repositoryLockHelper) wait(t *testing.T) {
	t.Helper()
	if err := helper.command.Wait(); err != nil {
		t.Fatalf("helper exit: %v; stderr=%q", err, helper.stderr.String())
	}
}
