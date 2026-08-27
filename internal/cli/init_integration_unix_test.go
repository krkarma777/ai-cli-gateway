//go:build integration && !windows

package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/configstore"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestCommandInitCancellationBeforeAndAfterCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess cancellation contract")
	}

	t.Run("before commit while waiting for transaction lock", func(t *testing.T) {
		gateway := testutil.BuildGateway(t)
		fixture := newCommandInitFixture(t)
		fake := buildInitCodexFake(t, filepath.Join(fixture.root, "provider-bin"))
		home := privateInitDirectory(t, filepath.Join(fixture.root, "codex-home"))
		configPath := filepath.Join(fixture.root, "cancel-before.toml")
		release := holdInitCommandLock(t, configstore.LockPath(configPath))
		defer release()
		args := append(codexCommandInitArgs(fake, home), "--config", configPath)
		running := startInitCommandProcess(t, gateway, args, fixture.environment)

		awaitInitCommandOutput(t, running)
		if err := running.command.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("signal pre-commit init: %v", err)
		}
		err := awaitInitCommand(t, running)
		if code := commandInitExitCode(err); code != 130 ||
			!strings.HasSuffix(running.stdout.String(), "setup_not_saved\n") ||
			running.stderr.Len() != 0 {
			t.Fatalf("pre-commit cancellation = code %d stdout %q stderr %q", code, running.stdout.String(), running.stderr.String())
		}
		if _, err := os.Lstat(configPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("pre-commit config state = %v, want absent", err)
		}
	})

	t.Run("after commit during Doctor", func(t *testing.T) {
		gateway := testutil.BuildGateway(t)
		fixture := newCommandInitFixture(t)
		fake := buildInitCodexFake(t, filepath.Join(fixture.root, "provider-bin"))
		home := privateInitDirectory(t, filepath.Join(fixture.root, "codex-home"))
		block := filepath.Join(home, ".block-doctor")
		// The path is an exact child of the private test-owned provider home.
		//nolint:gosec
		if err := os.WriteFile(block, []byte("block\n"), 0o600); err != nil {
			t.Fatalf("WriteFile Doctor block marker: %v", err)
		}
		running := startInitCommandProcess(
			t,
			gateway,
			codexCommandInitArgs(fake, home),
			fixture.environment,
		)
		awaitInitDoctorBlock(t, running, filepath.Join(home, ".doctor-blocked"))
		if err := running.command.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("signal post-commit init: %v", err)
		}
		err := awaitInitCommand(t, running)
		if code := commandInitExitCode(err); code != 130 ||
			!strings.HasSuffix(running.stdout.String(), "setup_saved_before_cancellation\n") ||
			running.stderr.Len() != 0 {
			t.Fatalf("post-commit cancellation = code %d stdout %q stderr %q", code, running.stdout.String(), running.stderr.String())
		}
		if _, err := os.Stat(fixture.configPath); err != nil {
			t.Fatalf("post-commit config missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(fixture.configPath), "gateway.key")); err != nil {
			t.Fatalf("post-commit Gateway key missing: %v", err)
		}
	})
}

func awaitInitCommandOutput(t *testing.T, running *runningInitProcess) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-running.stdout.firstWrite:
		return
	case err := <-running.wait:
		_ = running.stdinWriter.Close()
		running.cancel()
		t.Fatalf("init exited before lock cancellation: %v; stdout=%q stderr=%q", err, running.stdout.String(), running.stderr.String())
	case <-timer.C:
		_ = running.stdinWriter.Close()
		running.cancel()
		t.Fatalf("timed out waiting for init output; stdout=%q stderr=%q", running.stdout.String(), running.stderr.String())
	}
}

func holdInitCommandLock(t *testing.T, path string) func() {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- exact private test-owned lock path.
	if err != nil {
		t.Fatalf("OpenFile transaction lock: %v", err)
	}
	if err := unix.Fchmod(int(file.Fd()), 0o600); err != nil {
		_ = file.Close()
		t.Fatalf("Fchmod transaction lock: %v", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		t.Fatalf("Flock transaction lock: %v", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}
}

func awaitInitDoctorBlock(t *testing.T, running *runningInitProcess, marker string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case err := <-running.wait:
			_ = running.stdinWriter.Close()
			running.cancel()
			t.Fatalf("init exited before Doctor block: %v; stdout=%q stderr=%q", err, running.stdout.String(), running.stderr.String())
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				return
			}
		case <-timer.C:
			_ = running.stdinWriter.Close()
			running.cancel()
			t.Fatalf("timed out waiting for Doctor block; stdout=%q stderr=%q", running.stdout.String(), running.stderr.String())
		}
	}
}
