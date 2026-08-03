//go:build !windows

package process

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	unixSchedulingWaitBudget = 30 * time.Second
	unixOuterOwnerBudget     = 60 * time.Second
)

func TestUnixExecutableUnavailableMarkerHasLaunchProvenance(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*testing.T) string
		wantCause error
		runtimeID string
	}{
		{
			name: "missing executable",
			prepare: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing-provider")
			},
			wantCause: fs.ErrNotExist,
			runtimeID: "execmiss",
		},
		{
			name: "non-executable file",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "provider")
				if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantCause: fs.ErrPermission,
			runtimeID: "execperm",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			supervisor := newSupervisorForTest(t, supervisorTestLimits())
			requestRuntime := prepareSupervisorRuntime(
				t,
				supervisor,
				test.runtimeID,
			)
			_, err := supervisor.Execute(
				context.Background(),
				requestRuntime,
				CommandSpec{
					Executable: test.prepare(t),
					Dir:        requestRuntime.Dir,
				},
			)
			assertRunErrorKind(t, err, ErrorStart)
			if !errors.Is(err, ErrExecutableUnavailable) {
				t.Fatalf("error=%T %v, want executable-unavailable marker", err, err)
			}
			if !errors.Is(err, test.wantCause) {
				t.Fatalf("error=%T %v, want cause %v", err, err, test.wantCause)
			}
			assertPathGone(t, requestRuntime.Dir)
		})
	}
}

func TestExecutableUnavailableMarkerRequiresExplicitLaunchProvenance(
	t *testing.T,
) {
	for _, cause := range []error{fs.ErrNotExist, fs.ErrPermission} {
		err := &RunError{Kind: ErrorStart, Err: cause}
		if errors.Is(err, ErrExecutableUnavailable) {
			t.Fatalf("unmarked cause %v acquired executable provenance", cause)
		}
	}
}

func TestExpectedUnixProcessGroupRetainsPIDWhenLeaderAlreadyExited(
	t *testing.T,
) {
	const pid = 4321
	pgid, err := expectedUnixProcessGroup(
		pid,
		func(int) (int, error) {
			return -1, unix.ESRCH
		},
	)
	if err != nil {
		t.Fatalf("already-exited leader rejected: %v", err)
	}
	if pgid != pid {
		t.Fatalf("expected PGID=%d, want root PID %d", pgid, pid)
	}
}

func TestExpectedUnixProcessGroupRejectsUnverifiedGroup(t *testing.T) {
	const pid = 4321
	lookupFailure := errors.New("fixed process-group lookup failure")
	tests := []struct {
		name   string
		lookup func(int) (int, error)
		cause  error
	}{
		{
			name: "wrong process group",
			lookup: func(int) (int, error) {
				return pid + 1, nil
			},
		},
		{
			name: "non-ESRCH lookup failure",
			lookup: func(int) (int, error) {
				return -1, lookupFailure
			},
			cause: lookupFailure,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pgid, err := expectedUnixProcessGroup(pid, test.lookup)
			if err == nil {
				t.Fatal("unverified process group was accepted")
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("error=%v, want cause %v", err, test.cause)
			}
			if pgid != pid {
				t.Fatalf("expected PGID=%d, want root PID %d", pgid, pid)
			}
		})
	}
}

func TestNewSupervisorRejectsInvalidConfiguration(t *testing.T) {
	root := openSupervisorTestRoot(t)
	valid := supervisorTestLimits()
	tests := []struct {
		name   string
		root   *Root
		limits Limits
	}{
		{name: "nil root", limits: valid},
		{name: "zero execution", root: root, limits: withExecution(valid, 0)},
		{name: "negative execution", root: root, limits: withExecution(valid, -time.Nanosecond)},
		{name: "execution over maximum", root: root, limits: withExecution(valid, 24*time.Hour+time.Nanosecond)},
		{name: "zero grace", root: root, limits: withGrace(valid, 0)},
		{name: "negative grace", root: root, limits: withGrace(valid, -time.Nanosecond)},
		{name: "grace over maximum", root: root, limits: withGrace(valid, 24*time.Hour+time.Nanosecond)},
		{name: "zero cleanup", root: root, limits: withCleanup(valid, 0)},
		{name: "negative cleanup", root: root, limits: withCleanup(valid, -time.Nanosecond)},
		{name: "cleanup over maximum", root: root, limits: withCleanup(valid, 24*time.Hour+time.Nanosecond)},
		{name: "zero stdout", root: root, limits: withStdout(valid, 0)},
		{name: "stdout over maximum", root: root, limits: withStdout(valid, (64<<20)+1)},
		{name: "zero stderr", root: root, limits: withStderr(valid, 0)},
		{name: "stderr over maximum", root: root, limits: withStderr(valid, (16<<20)+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSupervisor(test.root, test.limits); err == nil {
				t.Fatal("NewSupervisor accepted invalid configuration")
			}
		})
	}
}

func TestNewSupervisorAcceptsExactCeilingsAndIndependentGrace(t *testing.T) {
	root := openSupervisorTestRoot(t)
	limits := Limits{
		Execution:   24 * time.Hour,
		TermGrace:   24 * time.Hour,
		Cleanup:     time.Nanosecond,
		StdoutBytes: 64 << 20,
		StderrBytes: 16 << 20,
	}
	if _, err := NewSupervisor(root, limits); err != nil {
		t.Fatalf("exact independent limits rejected: %v", err)
	}
}

func TestSupervisorTestBudgetClassesRemainSeparated(t *testing.T) {
	generic := supervisorTestLimits()
	if generic.Execution != 30*time.Second ||
		generic.TermGrace != 50*time.Millisecond ||
		generic.Cleanup != 30*time.Second {
		t.Fatalf("generic limits=%+v", generic)
	}

	semantic := []struct {
		name   string
		limits Limits
	}{
		{
			name: "cleanup failure precedence",
			limits: Limits{
				Execution: 2 * time.Second,
				TermGrace: 50 * time.Millisecond,
				Cleanup:   500 * time.Millisecond,
			},
		},
		{
			name: "terminal precedence",
			limits: Limits{
				Execution: 10 * time.Millisecond,
				TermGrace: 50 * time.Millisecond,
				Cleanup:   500 * time.Millisecond,
			},
		},
		{
			name: "grace and cleanup separation",
			limits: Limits{
				Execution: time.Second,
				TermGrace: 50 * time.Millisecond,
				Cleanup:   10 * time.Millisecond,
			},
		},
		{
			name: "deferred wait",
			limits: Limits{
				Execution: 5 * time.Millisecond,
				TermGrace: 5 * time.Millisecond,
				Cleanup:   10 * time.Millisecond,
			},
		},
		{
			name: "minimum cleanup",
			limits: Limits{
				Execution: 10 * time.Millisecond,
				TermGrace: time.Nanosecond,
				Cleanup:   time.Nanosecond,
			},
		},
		{
			name: "repeated timeout and cancellation",
			limits: Limits{
				Execution: 5 * time.Millisecond,
				TermGrace: 5 * time.Millisecond,
				Cleanup:   500 * time.Millisecond,
			},
		},
		{
			name: "quarantine",
			limits: Limits{
				Execution: 2 * time.Second,
				TermGrace: 50 * time.Millisecond,
				Cleanup:   100 * time.Millisecond,
			},
		},
	}
	for _, test := range semantic {
		t.Run(test.name, func(t *testing.T) {
			if test.limits.Execution >= generic.Execution ||
				test.limits.Cleanup >= generic.Cleanup {
				t.Fatalf(
					"semantic limits=%+v inherited generic=%+v",
					test.limits,
					generic,
				)
			}
		})
	}
}

func TestPreparedRuntimeCanBeDiscardedAfterShutdownBegins(t *testing.T) {
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "leasedisc")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := supervisor.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with prepared runtime error=%v", err)
	}
	if _, err := os.Lstat(requestRuntime.Dir); err != nil {
		t.Fatalf("prepared runtime disappeared: %v", err)
	}
	if err := supervisor.Discard(context.Background(), requestRuntime); err != nil {
		t.Fatalf("pre-admitted runtime cannot be discarded: %v", err)
	}
	assertPathGone(t, requestRuntime.Dir)

	retryCtx, retryCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer retryCancel()
	if err := supervisor.Shutdown(retryCtx); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
}

func TestPreparedRuntimeCanExecuteAfterShutdownBegins(t *testing.T) {
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	supervisor.runner = func(
		context.Context,
		*Root,
		Runtime,
		CommandSpec,
		Limits,
	) (runnerResult, error) {
		return runnerResult{result: Result{ExitCode: 0}}, nil
	}
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "leaseexec")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := supervisor.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with prepared runtime error=%v", err)
	}
	result, err := supervisor.Execute(
		context.Background(),
		requestRuntime,
		CommandSpec{
			Executable: absoluteEnvExecutable(t),
			Dir:        requestRuntime.Dir,
		},
	)
	if err != nil {
		t.Fatalf("pre-admitted runtime cannot execute: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	assertPathGone(t, requestRuntime.Dir)

	retryCtx, retryCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer retryCancel()
	if err := supervisor.Shutdown(retryCtx); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
}

func TestPreparedRuntimeCopyExecuteAndDiscardConsumeExactlyOnce(
	t *testing.T,
) {
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	started := make(chan struct{}, 2)
	releaseRunner := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseRunner)
		})
	}
	t.Cleanup(release)
	supervisor.runner = func(
		context.Context,
		*Root,
		Runtime,
		CommandSpec,
		Limits,
	) (runnerResult, error) {
		started <- struct{}{}
		<-releaseRunner
		return runnerResult{result: Result{ExitCode: 0}}, nil
	}
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "leasecopy")
	type execution struct {
		result Result
		err    error
	}
	firstDone := make(chan execution, 1)
	go func() {
		result, err := supervisor.Execute(
			context.Background(),
			requestRuntime,
			CommandSpec{
				Executable: absoluteEnvExecutable(t),
				Dir:        requestRuntime.Dir,
			},
		)
		firstDone <- execution{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("first copied runtime did not reach runner")
	}

	discardDone := make(chan error, 1)
	go func() {
		discardDone <- supervisor.Discard(
			context.Background(),
			requestRuntime,
		)
	}()
	select {
	case err := <-discardDone:
		assertRunErrorKind(t, err, ErrorCleanup)
	case <-started:
		t.Fatal("duplicate runtime reached runner")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("duplicate runtime consumption did not fail promptly")
	}
	release()
	select {
	case first := <-firstDone:
		if first.err != nil || first.result.ExitCode != 0 {
			t.Fatalf("first execution result=%+v err=%v", first.result, first.err)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("first runtime consumption did not finish")
	}
	assertPathGone(t, requestRuntime.Dir)
}

func TestPreparedRuntimeLeaseRejectsForeignSupervisorAndGeneration(
	t *testing.T,
) {
	first := newSupervisorForTest(t, supervisorTestLimits())
	second, err := NewSupervisor(first.root, supervisorTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			unixOuterOwnerBudget,
		)
		defer cancel()
		if err := second.Shutdown(ctx); err != nil {
			t.Errorf("shutdown second supervisor: %v", err)
		}
	})
	requestRuntime := prepareSupervisorRuntime(t, first, "leaseident")

	err = second.Discard(context.Background(), requestRuntime)
	assertRunErrorKind(t, err, ErrorCleanup)
	forged := requestRuntime
	forged.leaseGeneration++
	err = first.Discard(context.Background(), forged)
	assertRunErrorKind(t, err, ErrorCleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = first.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("invalid consumers changed lease count: %v", err)
	}
	if err := first.Discard(context.Background(), requestRuntime); err != nil {
		t.Fatal(err)
	}
	assertPathGone(t, requestRuntime.Dir)
	retryCtx, retryCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer retryCancel()
	if err := first.Shutdown(retryCtx); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRuntimeLeaseRejectsForgedRuntimeIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Runtime) Runtime
	}{
		{
			name: "request ID",
			mutate: func(runtime Runtime) Runtime {
				runtime.ID = "forgedid"
				return runtime
			},
		},
		{
			name: "directory",
			mutate: func(runtime Runtime) Runtime {
				runtime.Dir += "-forged"
				return runtime
			},
		},
		{
			name: "record",
			mutate: func(runtime Runtime) Runtime {
				runtime.record = &runtimeRecord{
					generation: runtime.generation,
				}
				return runtime
			},
		},
		{
			name: "root generation",
			mutate: func(runtime Runtime) Runtime {
				runtime.generation++
				return runtime
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			supervisor := newSupervisorForTest(t, supervisorTestLimits())
			requestRuntime := prepareSupervisorRuntime(
				t,
				supervisor,
				"leaseforg"+strconv.Itoa(index),
			)
			forged := test.mutate(requestRuntime)
			err := supervisor.Discard(context.Background(), forged)
			if err == nil {
				t.Fatal("forged runtime identity was accepted")
			}

			ctx, cancel := context.WithTimeout(
				context.Background(),
				10*time.Millisecond,
			)
			err = supervisor.Shutdown(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("forged identity consumed lease: %v", err)
			}
			if err := supervisor.Discard(
				context.Background(),
				requestRuntime,
			); err != nil {
				t.Fatalf("original lease was not consumable: %v", err)
			}
			assertPathGone(t, requestRuntime.Dir)
			retryCtx, retryCancel := context.WithTimeout(
				context.Background(),
				unixOuterOwnerBudget,
			)
			defer retryCancel()
			if err := supervisor.Shutdown(retryCtx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSelfTestLeaseSurvivesShutdownBetweenPrepareAndExecute(t *testing.T) {
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	executable := absoluteEnvExecutable(t)
	supervisor.runner = func(
		context.Context,
		*Root,
		Runtime,
		CommandSpec,
		Limits,
	) (runnerResult, error) {
		return runnerResult{result: Result{
			ExitCode:   0,
			Stdout:     []byte("ready\n"),
			StopAction: StopActionTERM,
		}}, nil
	}
	prepared := make(chan Runtime, 1)
	resume := make(chan struct{})
	var resumeOnce sync.Once
	release := func() {
		resumeOnce.Do(func() {
			close(resume)
		})
	}
	t.Cleanup(release)
	supervisor.selfTestPrepared = func(runtime Runtime) {
		prepared <- runtime
		<-resume
	}
	selfTestDone := make(chan error, 1)
	go func() {
		selfTestDone <- supervisor.SelfTest(
			context.Background(),
			executable,
		)
	}()
	var requestRuntime Runtime
	select {
	case requestRuntime = <-prepared:
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("SelfTest did not prepare its runtime")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := supervisor.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown crossed SelfTest lease gap: %v", err)
	}
	release()
	select {
	case err := <-selfTestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("SelfTest did not consume prepared lease")
	}
	assertPathGone(t, requestRuntime.Dir)
	retryCtx, retryCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer retryCancel()
	if err := supervisor.Shutdown(retryCtx); err != nil {
		t.Fatal(err)
	}
}

func TestSelfTestRejectsSuccessWithoutContainmentAction(t *testing.T) {
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	supervisor.runner = func(
		context.Context,
		*Root,
		Runtime,
		CommandSpec,
		Limits,
	) (runnerResult, error) {
		return runnerResult{result: Result{
			ExitCode: 0,
			Stdout:   []byte("ready\n"),
		}}, nil
	}
	err := supervisor.SelfTest(
		context.Background(),
		absoluteEnvExecutable(t),
	)
	assertRunErrorKind(t, err, ErrorStart)
}

func TestNilSupervisorExecuteFailsClosed(t *testing.T) {
	var supervisor *Supervisor
	_, err := supervisor.Execute(
		context.Background(),
		Runtime{},
		CommandSpec{},
	)
	assertRunErrorKind(t, err, ErrorStart)
}

func TestExecuteRejectsWrongDirectoryAndCleansRuntime(t *testing.T) {
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	runtime := prepareSupervisorRuntime(t, supervisor, "wrongdir")
	spec := CommandSpec{
		Executable: absoluteEnvExecutable(t),
		Dir:        filepath.Dir(runtime.Dir),
	}
	_, err := supervisor.Execute(context.Background(), runtime, spec)
	assertRunErrorKind(t, err, ErrorStart)
	assertPathGone(t, runtime.Dir)
}

func TestExecuteRevalidatesRuntimeImmediatelyBeforeLaunch(t *testing.T) {
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	runtime := prepareSupervisorRuntime(t, supervisor, "replace1")
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(runtime.Dir), "moved-original")
	originalRunner := supervisor.runner
	supervisor.runner = func(
		ctx context.Context,
		root *Root,
		gotRuntime Runtime,
		spec CommandSpec,
		limits Limits,
	) (runnerResult, error) {
		materialized, err := os.ReadFile(
			filepath.Join(gotRuntime.Dir, "request.json"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if string(materialized) != "materialized" {
			t.Fatalf("materialized file=%q", materialized)
		}
		if err := os.Rename(gotRuntime.Dir, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, gotRuntime.Dir); err != nil {
			t.Fatal(err)
		}
		return originalRunner(ctx, root, gotRuntime, spec, limits)
	}

	spec := CommandSpec{
		Executable: absoluteEnvExecutable(t),
		Dir:        runtime.Dir,
		Files: []FileSpec{{
			Name: "request.json",
			Data: []byte("materialized"),
		}},
	}
	_, err := supervisor.Execute(context.Background(), runtime, spec)
	assertRunErrorKind(t, err, ErrorStart)
	if _, err := os.Lstat(moved); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("anchored original runtime remains: %v", err)
	}
	// #nosec G304 -- sentinel is created and owned by this test.
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "safe" {
		t.Fatalf("outside sentinel changed: data=%q err=%v", got, err)
	}
	if info, err := os.Lstat(runtime.Dir); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement was followed or removed: info=%v err=%v", info, err)
	}
	if err := os.Remove(runtime.Dir); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteNilAndEmptyEnvInheritNothing(t *testing.T) {
	t.Setenv("SPAWNGATE_PROVIDER_SECRET", "must-not-leak")
	for _, env := range [][]string{nil, {}} {
		name := "nil"
		if env != nil {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			supervisor := newSupervisorForTest(t, supervisorTestLimits())
			runtime := prepareSupervisorRuntime(t, supervisor, "env"+name+"01")
			result, err := supervisor.Execute(context.Background(), runtime, CommandSpec{
				Executable: absoluteEnvExecutable(t),
				Env:        env,
				Dir:        runtime.Dir,
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(result.Stdout), "SPAWNGATE_PROVIDER_SECRET") ||
				strings.Contains(string(result.Stdout), "must-not-leak") {
				t.Fatalf("gateway environment leaked: %q", result.Stdout)
			}
		})
	}
}

func TestExecuteRejectsMalformedEnvironment(t *testing.T) {
	for i, env := range [][]string{
		{""},
		{"NO_SEPARATOR"},
		{"=missing-name"},
		{"BAD\x00NAME=value"},
		{"NAME=bad\x00value"},
		{"DUP=first", "DUP=second"},
	} {
		supervisor := newSupervisorForTest(t, supervisorTestLimits())
		runtime := prepareSupervisorRuntime(t, supervisor, "badenv0"+string(rune('a'+i)))
		_, err := supervisor.Execute(context.Background(), runtime, CommandSpec{
			Executable: absoluteEnvExecutable(t),
			Env:        env,
			Dir:        runtime.Dir,
		})
		assertRunErrorKind(t, err, ErrorStart)
		assertPathGone(t, runtime.Dir)
	}
}

func TestDiscardRejectsForeignRuntimeWithoutDeletingIt(t *testing.T) {
	first := newSupervisorForTest(t, supervisorTestLimits())
	second := newSupervisorForTest(t, supervisorTestLimits())
	runtime := prepareSupervisorRuntime(t, first, "foreign1")
	err := second.Discard(context.Background(), runtime)
	assertRunErrorKind(t, err, ErrorCleanup)
	if _, err := os.Lstat(runtime.Dir); err != nil {
		t.Fatalf("foreign runtime was changed: %v", err)
	}
	if err := first.Discard(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	assertPathGone(t, runtime.Dir)
}

func TestCleanupFailureWinsAndJoinsExecutionCause(t *testing.T) {
	limits := Limits{
		Execution:   2 * time.Second,
		TermGrace:   50 * time.Millisecond,
		Cleanup:     500 * time.Millisecond,
		StdoutBytes: 64 * 1024,
		StderrBytes: 64 * 1024,
	}
	supervisor := newSupervisorForTest(t, limits)
	runtime := prepareSupervisorRuntime(t, supervisor, "cleanup1")
	moved := filepath.Join(t.TempDir(), "moved-runtime")
	originalRunner := supervisor.runner
	supervisor.runner = func(
		ctx context.Context,
		root *Root,
		gotRuntime Runtime,
		spec CommandSpec,
		limits Limits,
	) (runnerResult, error) {
		if err := os.Rename(gotRuntime.Dir, moved); err != nil {
			t.Fatal(err)
		}
		return originalRunner(ctx, root, gotRuntime, spec, limits)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := supervisor.Execute(ctx, runtime, CommandSpec{
		Executable: absoluteEnvExecutable(t),
		Dir:        runtime.Dir,
	})
	if ctx.Err() != nil {
		t.Fatalf("outer context won cleanup precedence: %v", ctx.Err())
	}
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v", err, err)
	}
	var joinedStart *RunError
	if !errors.As(runErr.Err, &joinedStart) || joinedStart.Kind != ErrorStart {
		t.Fatalf("cleanup error did not retain execution cause: %v", runErr.Err)
	}
	if err := os.RemoveAll(moved); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCausePrecedence(t *testing.T) {
	overflow := make(chan struct{}, 1)
	stdout := newCapture(1, overflow)
	if _, err := stdout.Write([]byte("xx")); err != nil {
		t.Fatal(err)
	}
	state := terminalState{}
	state.commit(terminalObservation{
		overflowed: stdout.Overflowed(),
		timedOut:   true,
		waited:     true,
	})
	if state.kind != ErrorOutputLimit {
		t.Fatalf("output/timeout/exit kind=%q", state.kind)
	}

	state = terminalState{}
	state.commit(terminalObservation{timedOut: true, waited: true})
	if state.kind != ErrorTimeout {
		t.Fatalf("timeout/exit kind=%q", state.kind)
	}

	state = terminalState{}
	state.commit(terminalObservation{canceled: true})
	state.commit(terminalObservation{overflowed: true, timedOut: true})
	if state.kind != ErrorCanceled {
		t.Fatalf("committed cancellation was replaced by %q", state.kind)
	}
}

func TestProcessCompletionDefersOnlyWithheldWait(t *testing.T) {
	beforeFDs := countUnitOpenFDs(t)
	beforeGoroutines := runtime.NumGoroutine()
	pipes, err := openUnixPipes()
	if err != nil {
		t.Fatal(err)
	}
	if err := pipes.closeChildEnds(); err != nil {
		t.Fatal(err)
	}

	waitDone := make(chan waitResult, 1)
	writerDone := make(chan error, 1)
	stdoutDone := make(chan readerResult, 1)
	stderrDone := make(chan readerResult, 1)
	writerDone <- nil
	stdoutDone <- readerResult{stream: "stdout"}
	stderrDone <- readerResult{stream: "stderr"}

	owner := newCompletionOwner()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Millisecond,
	)
	started := time.Now()
	state, err := joinProcessCompletions(
		ctx,
		pipes,
		owner,
		1234,
		processCompletionChannels{
			wait:   waitDone,
			writer: writerDone,
			stdout: stdoutDone,
			stderr: stderrDone,
		},
		processCompletionState{
			wait: waitResult{exitCode: -1},
		},
	)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("join error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("join exceeded bound: %v", elapsed)
	}
	if state.waitSeen || state.wait.exitCode != -1 {
		t.Fatalf("withheld Wait state=%+v", state)
	}
	if !state.writerSeen || !state.stdoutSeen || !state.stderrSeen {
		t.Fatalf("pipe completion state=%+v", state)
	}
	if got := owner.count(); got != 1 {
		t.Fatalf("pending Waits=%d, want 1", got)
	}

	waitDone <- waitResult{exitCode: 0}
	drainCtx, drainCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer drainCancel()
	if err := owner.drain(drainCtx); err != nil {
		t.Fatal(err)
	}
	if err := pipes.closeAll(); err != nil {
		t.Fatal(err)
	}
	waitForUnitResourceBaseline(t, beforeFDs, beforeGoroutines)
}

func TestCompletionOwnerRetainsConcreteResultUntilDrain(t *testing.T) {
	owner := newCompletionOwner()
	waitDone := make(chan waitResult, 1)
	owner.deferWait(4321, waitDone)
	waitDone <- waitResult{exitCode: 7}
	deadline := time.NewTimer(unixSchedulingWaitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for owner.completedCount() != 1 {
		select {
		case <-deadline.C:
			t.Fatal("deferred Wait result was not retained")
		case <-ticker.C:
		}
	}
	if got := owner.count(); got != 1 {
		t.Fatalf("retained Waits=%d, want 1", got)
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer cancel()
	if err := owner.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := owner.count(); got != 0 {
		t.Fatalf("drain left Waits=%d", got)
	}
}

func TestCompletionOwnerTimedDrainReturnsCompletedResultAndKeepsPending(
	t *testing.T,
) {
	owner := newCompletionOwner()
	completed := make(chan waitResult, 1)
	withheld := make(chan waitResult, 1)
	sentinel := errors.New("fixed Wait failure")
	owner.deferWait(4321, completed)
	owner.deferWait(4322, withheld)
	completed <- waitResult{err: sentinel, exitCode: -1}
	deadline := time.NewTimer(unixSchedulingWaitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for owner.completedCount() != 1 {
		select {
		case <-deadline.C:
			t.Fatal("completed Wait result was not retained")
		case <-ticker.C:
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := owner.drain(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) ||
		!errors.Is(err, sentinel) {
		t.Fatalf("timed drain error=%v", err)
	}
	if got := owner.count(); got != 1 {
		t.Fatalf("timed drain retained Waits=%d, want 1", got)
	}

	withheld <- waitResult{exitCode: 0}
	drainCtx, drainCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer drainCancel()
	if err := owner.drain(drainCtx); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteReturnsDeferredCompletionFailureAsCleanup(t *testing.T) {
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "ownerfail")
	waitFailure := errors.New("fixed retained Wait failure")
	supervisor.runner = func(
		context.Context,
		*Root,
		Runtime,
		CommandSpec,
		Limits,
	) (runnerResult, error) {
		completed := make(chan waitResult, 1)
		supervisor.completions.deferWait(4321, completed)
		completed <- waitResult{err: waitFailure, exitCode: -1}
		return runnerResult{result: Result{ExitCode: -1}}, nil
	}

	result, err := supervisor.Execute(
		context.Background(),
		requestRuntime,
		CommandSpec{
			Executable: absoluteEnvExecutable(t),
			Dir:        requestRuntime.Dir,
		},
	)
	if err == nil {
		drainCtx, cancel := context.WithTimeout(
			context.Background(),
			unixOuterOwnerBudget,
		)
		deferredErr := supervisor.completions.drain(drainCtx)
		cancel()
		t.Fatalf(
			"Execute succeeded before retained failure drain: deferred=%v",
			deferredErr,
		)
	}
	assertRunErrorKind(t, err, ErrorCleanup)
	if !errors.Is(err, waitFailure) {
		t.Fatalf("Execute error=%v, want retained Wait failure", err)
	}
	if result.StopReason != StopReasonCleanupFailure {
		t.Fatalf("result=%+v", result)
	}
	if got := supervisor.completions.count(); got != 0 {
		t.Fatalf("Execute left completion owners=%d", got)
	}
	assertPathGone(t, requestRuntime.Dir)
}

func TestProcessCompletionDeadlineSynchronouslyJoinsRealPipeOwners(
	t *testing.T,
) {
	beforeFDs := countUnitOpenFDs(t)
	beforeGoroutines := runtime.NumGoroutine()
	pipes, err := openUnixPipes()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pipes.closeAll()
	})

	waitDone := make(chan waitResult, 1)
	writerDone := make(chan error, 1)
	stdoutDone := make(chan readerResult, 1)
	stderrDone := make(chan readerResult, 1)
	stdinParent := pipes.stdinParent
	stdoutParent := pipes.stdoutParent
	stderrParent := pipes.stderrParent
	go func() {
		writerDone <- writeStdinAndClose(stdinParent, make([]byte, 8<<20))
	}()
	go copyPipe("stdout", stdoutParent, io.Discard, stdoutDone)
	go copyPipe("stderr", stderrParent, io.Discard, stderrDone)

	owner := newCompletionOwner()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	state, err := joinProcessCompletions(
		ctx,
		pipes,
		owner,
		1234,
		processCompletionChannels{
			wait:   waitDone,
			writer: writerDone,
			stdout: stdoutDone,
			stderr: stderrDone,
		},
		processCompletionState{
			wait: waitResult{exitCode: -1},
		},
	)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("join error=%v", err)
	}
	if state.waitSeen {
		t.Fatal("withheld Wait was reported complete")
	}
	if !state.writerSeen || !state.stdoutSeen || !state.stderrSeen {
		t.Fatalf(
			"pipe owners were not synchronously joined: writer=%v stdout=%v stderr=%v",
			state.writerSeen,
			state.stdoutSeen,
			state.stderrSeen,
		)
	}

	waitDone <- waitResult{exitCode: 0}
	if err := pipes.closeAll(); err != nil {
		t.Fatal(err)
	}
	waitForUnitResourceBaseline(t, beforeFDs, beforeGoroutines)
}

func TestUnverifiedStartCleanupDefersWithheldWait(t *testing.T) {
	beforeFDs := countUnitOpenFDs(t)
	beforeGoroutines := runtime.NumGoroutine()
	pipes, err := openUnixPipes()
	if err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan waitResult, 1)
	owner := newCompletionOwner()
	killed := false
	started := time.Now()
	err = boundedUnverifiedStartCleanup(
		10*time.Millisecond,
		pipes,
		1234,
		waitDone,
		func() error {
			killed = true
			return nil
		},
		owner,
	)
	if !killed {
		t.Fatal("root process was not killed")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("cleanup exceeded bound: %v", elapsed)
	}
	waitDone <- waitResult{exitCode: -1}
	drainCtx, drainCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer drainCancel()
	if err := owner.drain(drainCtx); err != nil {
		t.Fatal(err)
	}
	waitForUnitResourceBaseline(t, beforeFDs, beforeGoroutines)
}

func countUnitOpenFDs(t *testing.T) int {
	t.Helper()
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	cur := checkedRlimitCur(t, limit.Cur)
	maxFD := min(cur, uint64(4_096))
	count := 0
	for fd := range maxFD {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			count++
		}
	}
	return count
}

func checkedRlimitCur[T ~int64 | ~uint64](
	t *testing.T,
	cur T,
) uint64 {
	t.Helper()
	if cur < 0 {
		t.Fatalf("RLIMIT_NOFILE Cur is negative: %d", cur)
	}
	return uint64(cur)
}

func waitForUnitResourceBaseline(
	t *testing.T,
	beforeFDs int,
	beforeGoroutines int,
) {
	t.Helper()
	deadline := time.NewTimer(unixSchedulingWaitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if countUnitOpenFDs(t) <= beforeFDs &&
			runtime.NumGoroutine() <= beforeGoroutines {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf(
				"resources remain: fds %d->%d goroutines %d->%d",
				beforeFDs,
				countUnitOpenFDs(t),
				beforeGoroutines,
				runtime.NumGoroutine(),
			)
		case <-ticker.C:
		}
	}
}

func openSupervisorTestRoot(t *testing.T) *Root {
	t.Helper()
	root, err := OpenRoot(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close root: %v", err)
		}
	})
	return root
}

func newSupervisorForTest(t *testing.T, limits Limits) *Supervisor {
	t.Helper()
	supervisor, err := NewSupervisor(openSupervisorTestRoot(t), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			unixOuterOwnerBudget,
		)
		defer cancel()
		if err := supervisor.Shutdown(ctx); err != nil {
			t.Errorf("shutdown supervisor: %v", err)
		}
	})
	return supervisor
}

func supervisorTestLimits() Limits {
	return Limits{
		Execution:   30 * time.Second,
		TermGrace:   50 * time.Millisecond,
		Cleanup:     30 * time.Second,
		StdoutBytes: 64 * 1024,
		StderrBytes: 64 * 1024,
	}
}

func prepareSupervisorRuntime(
	t *testing.T,
	supervisor *Supervisor,
	id string,
) Runtime {
	t.Helper()
	runtime, err := supervisor.Prepare(id)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func assertRunErrorKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != kind {
		t.Fatalf("error=%T %v, want RunError kind %q", err, err, kind)
	}
}

func assertPathGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path still exists: %s: %v", path, err)
	}
}

func absoluteEnvExecutable(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env executable unavailable")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func withExecution(limits Limits, value time.Duration) Limits {
	limits.Execution = value
	return limits
}

func withGrace(limits Limits, value time.Duration) Limits {
	limits.TermGrace = value
	return limits
}

func withCleanup(limits Limits, value time.Duration) Limits {
	limits.Cleanup = value
	return limits
}

func withStdout(limits Limits, value int64) Limits {
	limits.StdoutBytes = value
	return limits
}

func withStderr(limits Limits, value int64) Limits {
	limits.StderrBytes = value
	return limits
}
