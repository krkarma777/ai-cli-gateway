//go:build !windows

package sdkcontract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestExpectedUnixChildProcessGroupAcceptsAlreadyExitedLeader(t *testing.T) {
	const pid = 4321
	pgid, err := expectedUnixChildProcessGroup(pid, func(observedPID int) (int, error) {
		if observedPID != pid {
			t.Fatalf("lookup PID = %d", observedPID)
		}
		return -1, unix.ESRCH
	})
	if err != nil {
		t.Fatalf("already-exited leader rejected: %v", err)
	}
	if pgid != pid {
		t.Fatalf("PGID = %d, want %d", pgid, pid)
	}
}

func TestExpectedUnixChildProcessGroupRejectsUnverifiedGroup(t *testing.T) {
	const pid = 4321
	lookupFailure := errors.New("fixed lookup failure")
	for _, test := range []struct {
		name   string
		pid    int
		lookup func(int) (int, error)
		cause  error
	}{
		{
			name: "invalid PID",
			pid:  0,
			lookup: func(int) (int, error) {
				return pid, nil
			},
		},
		{name: "missing lookup", pid: pid},
		{
			name: "wrong process group",
			pid:  pid,
			lookup: func(int) (int, error) {
				return pid + 1, nil
			},
		},
		{
			name: "non-ESRCH lookup failure",
			pid:  pid,
			lookup: func(int) (int, error) {
				return -1, lookupFailure
			},
			cause: lookupFailure,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pgid, err := expectedUnixChildProcessGroup(test.pid, test.lookup)
			if err == nil {
				t.Fatal("unverified process group was accepted")
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("error = %v, want cause %v", err, test.cause)
			}
			if pgid != test.pid {
				t.Fatalf("PGID = %d, want %d", pgid, test.pid)
			}
		})
	}
}

func TestStartUnixCommandJoinsAlreadyExitedLeader(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	started, err := startUnixCommandWithProcessGroupLookup(
		executable,
		"",
		[]string{"-test.run=^TestSDKContractEmptyEnvironmentHelperProcess$"},
		[]string{},
		io.Discard,
		io.Discard,
		func(int) (int, error) { return -1, unix.ESRCH },
	)
	if err != nil {
		t.Fatalf("start already-exited child: %v", err)
	}
	result := started.StopAndWait(time.Second)
	if result.Err != nil || !result.SafeToRemove {
		t.Fatalf("StopAndWait = %#v", result)
	}
	select {
	case <-started.Exited():
	default:
		t.Fatal("child Wait was not joined")
	}
}

func TestStartUnixCommandDoesNotCertifyWrongLiveGroup(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	started, err := startUnixCommandWithProcessGroupLookup(
		executable,
		"",
		[]string{"-test.run=^TestSDKContractChildHelperProcess$"},
		[]string{"SDK_CONTRACT_CHILD_HELPER=ignore-term"},
		io.Discard,
		io.Discard,
		func(pid int) (int, error) { return pid + 1, nil },
	)
	if started == nil || !isCleanupSafety(err) || cleanupErrorSafe(err) {
		t.Fatalf("start result child=%#v error=%v safe=%t", started, err, cleanupErrorSafe(err))
	}
	cleanup := started.StopAndWait(50 * time.Millisecond)
	if cleanup.Err != nil || !cleanup.SafeToRemove {
		t.Fatalf("explicit cleanup = %#v", cleanup)
	}
}

func TestUnixChildDoesNotSignalJoinedAbsentGroup(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	child := &unixChild{pid: 4321, pgid: 4321, exited: exited}
	signals := 0
	result := child.stopAndWaitWith(
		time.Second,
		func(int, unix.Signal) error {
			signals++
			return errors.New("unexpected signal")
		},
		func(pgid int) bool {
			if pgid != child.pgid {
				t.Fatalf("group probe PGID = %d", pgid)
			}
			return true
		},
	)
	if result.Err != nil || !result.SafeToRemove || signals != 0 {
		t.Fatalf("stop result=%#v signals=%d", result, signals)
	}
}

func TestRunGroupCommandAcceptsCachedGoBuild(t *testing.T) {
	repository := moduleRootForUnitTest(t)
	goExecutable, err := resolveBuildTool(exec.LookPath)
	if err != nil {
		t.Fatalf("resolve Go executable: %v", err)
	}
	for _, test := range []struct {
		name        string
		packagePath string
	}{
		{name: "gateway", packagePath: "./cmd/ai-cli-gateway"},
		{name: "slow codex", packagePath: "./internal/sdkcontract/testdata/slow-codex"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := trustedSiblingFixture(t)
			cache := filepath.Join(root, "gocache")
			if err := os.Mkdir(cache, 0o700); err != nil {
				t.Fatalf("create isolated Go cache: %v", err)
			}
			t.Setenv("GOCACHE", cache)

			for attempt := 1; attempt <= 3; attempt++ {
				output := filepath.Join(root, fmt.Sprintf("build-output-%d", attempt))
				var result groupCommandResult
				err := func() error {
					previousUmask := setProcessUmask(0o002)
					defer setProcessUmask(previousUmask)
					var runErr error
					result, runErr = runGroupCommand(
						context.Background(),
						goExecutable,
						repository,
						[]string{"build", "-trimpath", "-o", output, test.packagePath},
						minimalBuildEnvironment(),
						30*time.Second,
						0,
						8<<10,
					)
					return runErr
				}()
				if err != nil {
					t.Fatalf("cached Go build attempt %d: %v", attempt, err)
				}
				if len(result.stderr) != 0 {
					t.Fatalf("cached Go build attempt %d stderr was not empty", attempt)
				}
			}
		})
	}
}
