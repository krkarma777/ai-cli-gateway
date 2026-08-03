//go:build integration && !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestSupervisorStdinEchoAndSeparateCaptures(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newSupervisorForTest(t, supervisorTestLimits())

	echoRuntime := prepareSupervisorRuntime(t, supervisor, "echostdin")
	echo, err := executeIntegration(t, supervisor, echoRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=echo-stdin"},
		Dir:        echoRuntime.Dir,
		Stdin:      []byte("fixed input\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(echo.Stdout) != "fixed input\n" || len(echo.Stderr) != 0 {
		t.Fatalf("echo stdout=%q stderr=%q", echo.Stdout, echo.Stderr)
	}
	if echo.ExitCode != 0 || echo.StopReason != StopReasonNormalExit {
		t.Fatalf("echo result=%+v", echo)
	}

	stderrRuntime := prepareSupervisorRuntime(t, supervisor, "stderr01")
	stderrResult, err := executeIntegration(
		t,
		supervisor,
		stderrRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--unknown"},
			Dir:        stderrRuntime.Dir,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(stderrResult.Stdout) != 0 ||
		string(stderrResult.Stderr) != "fake-ai-cli: invalid mode\n" ||
		stderrResult.ExitCode != 2 {
		t.Fatalf("stderr result=%+v", stderrResult)
	}
}

func TestSupervisorChildReadsExactMaterializedFile(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "fileexact")
	want := []byte("{\"request\":\"exact bytes\"}\n")
	ctx, cancel := integrationContext(t)
	defer cancel()
	result, err := supervisor.Execute(ctx, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=read-request-file"},
		Dir:        requestRuntime.Dir,
		Files: []FileSpec{{
			Name: "request.json",
			Data: want,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != string(want) || len(result.Stderr) != 0 {
		t.Fatalf("stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestSupervisorMaterializationFilesystemFailuresStayUnmarked(
	t *testing.T,
) {
	t.Run("ENOENT", func(t *testing.T) {
		supervisor := newSupervisorForTest(t, supervisorTestLimits())
		requestRuntime := prepareSupervisorRuntime(t, supervisor, "matmissing")
		if err := os.Remove(requestRuntime.Dir); err != nil {
			t.Fatal(err)
		}

		_, err := supervisor.Execute(
			context.Background(),
			requestRuntime,
			CommandSpec{
				Executable: absoluteEnvExecutable(t),
				Dir:        requestRuntime.Dir,
				Files: []FileSpec{{
					Name: "input.json",
					Data: []byte("not written"),
				}},
			},
		)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("materialization error=%T %v, want ENOENT", err, err)
		}
		if errors.Is(err, ErrExecutableUnavailable) {
			t.Fatalf("materialization ENOENT acquired executable marker: %v", err)
		}
		assertPathGone(t, requestRuntime.Dir)
	})

	t.Run("EACCES", func(t *testing.T) {
		supervisor := newSupervisorForTest(t, supervisorTestLimits())
		requestRuntime := prepareSupervisorRuntime(t, supervisor, "matdenied")
		privateParent := filepath.Dir(supervisor.root.path)
		parentInfo, err := os.Stat(privateParent)
		if err != nil {
			t.Fatal(err)
		}
		originalMode := parentInfo.Mode().Perm()
		if err := os.Chmod(privateParent, 0); err != nil {
			t.Skipf("cannot make private test ancestor inaccessible: %v", err)
		}
		restored := false
		defer func() {
			if !restored {
				if err := os.Chmod(privateParent, originalMode); err != nil {
					t.Errorf("restore private test ancestor mode: %v", err)
				}
			}
		}()

		_, executeErr := supervisor.Execute(
			context.Background(),
			requestRuntime,
			CommandSpec{
				Executable: absoluteEnvExecutable(t),
				Dir:        requestRuntime.Dir,
				Files: []FileSpec{{
					Name: "input.json",
					Data: []byte("not written"),
				}},
			},
		)
		if err := os.Chmod(privateParent, originalMode); err != nil {
			t.Fatalf("restore private test ancestor mode: %v", err)
		}
		restored = true
		if executeErr == nil {
			t.Skip("filesystem permission checks are bypassed by this test user")
		}
		if !errors.Is(executeErr, fs.ErrPermission) {
			t.Fatalf(
				"materialization error=%T %v, want EACCES",
				executeErr,
				executeErr,
			)
		}
		if errors.Is(executeErr, ErrExecutableUnavailable) {
			t.Fatalf(
				"materialization EACCES acquired executable marker: %v",
				executeErr,
			)
		}
		assertPathGone(t, requestRuntime.Dir)
	})
}

func TestSupervisorRealProcessTerminalPrecedence(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)

	t.Run("output over timeout over ordinary exit", func(t *testing.T) {
		limits := supervisorTestLimits()
		limits.Execution = 10 * time.Millisecond
		limits.TermGrace = 50 * time.Millisecond
		limits.Cleanup = 500 * time.Millisecond
		limits.StdoutBytes = 1024
		supervisor := newSupervisorForTest(t, limits)
		supervisor.hooks.beforeCommit = func(events runnerEventView) {
			waitForRunnerEvent(t, events.waitReady)
			waitForRunnerEvent(t, events.timeoutReady)
			waitForRunnerCondition(t, events.overflowed)
		}
		requestRuntime := prepareSupervisorRuntime(t, supervisor, "allready1")
		ctx, cancel := integrationContext(t)
		defer cancel()
		result, err := supervisor.Execute(ctx, requestRuntime, CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=flood-once-exit-7"},
			Dir:        requestRuntime.Dir,
		})
		assertRunErrorKind(t, err, ErrorOutputLimit)
		if result.StopReason != StopReasonOutputOverflow {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("timeout over ordinary exit", func(t *testing.T) {
		limits := supervisorTestLimits()
		limits.Execution = 10 * time.Millisecond
		limits.TermGrace = 50 * time.Millisecond
		limits.Cleanup = 500 * time.Millisecond
		supervisor := newSupervisorForTest(t, limits)
		supervisor.hooks.beforeCommit = func(events runnerEventView) {
			waitForRunnerEvent(t, events.waitReady)
			waitForRunnerEvent(t, events.timeoutReady)
		}
		requestRuntime := prepareSupervisorRuntime(t, supervisor, "timeexit1")
		ctx, cancel := integrationContext(t)
		defer cancel()
		result, err := supervisor.Execute(ctx, requestRuntime, CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=exit-7"},
			Dir:        requestRuntime.Dir,
		})
		assertRunErrorKind(t, err, ErrorTimeout)
		if result.StopReason != StopReasonSupervisorTimeout {
			t.Fatalf("result=%+v", result)
		}
	})
}

func TestTerminalPrecedenceBudgetOrdering(t *testing.T) {
	const semanticExecution = 10 * time.Millisecond
	if unixSchedulingWaitBudget != 30*time.Second {
		t.Fatalf("scheduling wait=%v want=30s", unixSchedulingWaitBudget)
	}
	if unixOuterOwnerBudget != 60*time.Second {
		t.Fatalf("outer owner=%v want=60s", unixOuterOwnerBudget)
	}
	if !(semanticExecution < unixSchedulingWaitBudget &&
		unixSchedulingWaitBudget < unixOuterOwnerBudget) {
		t.Fatalf(
			"budget ordering semantic=%v scheduling=%v outer=%v",
			semanticExecution,
			unixSchedulingWaitBudget,
			unixOuterOwnerBudget,
		)
	}
}

func TestSupervisorCommittedCancellationIsImmutable(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 30 * time.Second
	limits.TermGrace = 50 * time.Millisecond
	limits.Cleanup = 500 * time.Millisecond
	limits.StdoutBytes = 1024
	supervisor := newSupervisorForTest(t, limits)
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "cancelimmut")
	ctx, cancel := integrationContext(t)
	defer cancel()

	committed := make(chan terminalState, 1)
	releaseCommit := make(chan struct{})
	supervisor.hooks.afterCommit = func(
		state terminalState,
		_ runnerEventView,
	) {
		committed <- state
		<-releaseCommit
	}
	type execution struct {
		result Result
		err    error
	}
	executed := make(chan execution, 1)
	go func() {
		result, err := supervisor.Execute(ctx, requestRuntime, CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=release-then-flood"},
			Dir:        requestRuntime.Dir,
		})
		executed <- execution{result: result, err: err}
	}()

	waitForIntegrationPath(t, filepath.Join(requestRuntime.Dir, ".fake-ready"))
	cancel()
	select {
	case state := <-committed:
		if state.kind != ErrorCanceled {
			t.Fatalf("committed state=%+v", state)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("runner did not commit cancellation")
	}
	if err := os.WriteFile(
		filepath.Join(requestRuntime.Dir, ".fake-release"),
		[]byte("release\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	waitForIntegrationPath(
		t,
		filepath.Join(requestRuntime.Dir, ".fake-overflowed"),
	)
	close(releaseCommit)

	select {
	case outcome := <-executed:
		assertRunErrorKind(t, outcome.err, ErrorCanceled)
		if outcome.result.StopReason != StopReasonCallerCancellation {
			t.Fatalf("result=%+v", outcome.result)
		}
		if outcome.result.StdoutTotal <= limits.StdoutBytes {
			t.Fatalf("stdout total=%d", outcome.result.StdoutTotal)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("Execute did not return")
	}
}

func TestSupervisorStartFailureClosesEveryPipe(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	beforeFDs := countOpenFDs(t)
	beforeGoroutines := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		requestRuntime := prepareSupervisorRuntime(
			t,
			supervisor,
			"vanished"+strconv.Itoa(100+i),
		)
		_, err := executeIntegration(t, supervisor, requestRuntime, CommandSpec{
			Executable: executable,
			Dir:        requestRuntime.Dir,
			Stdin:      make([]byte, 64*1024),
		})
		assertRunErrorKind(t, err, ErrorStart)
	}
	waitForResourceBaseline(t, beforeFDs, beforeGoroutines)
}

func TestSupervisorExitCodeSeven(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "exitcode7")
	result, err := executeIntegration(t, supervisor, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=exit-7"},
		Dir:        requestRuntime.Dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || result.StopReason != StopReasonNormalExit {
		t.Fatalf("result=%+v", result)
	}
}

func TestSupervisorTimeoutAndCallerCancellation(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)

	timeoutLimits := supervisorTestLimits()
	timeoutLimits.Execution = 200 * time.Millisecond
	timeoutLimits.TermGrace = 50 * time.Millisecond
	timeoutLimits.Cleanup = 500 * time.Millisecond
	supervisor := newSupervisorForTest(t, timeoutLimits)
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "timeout01")
	started := time.Now()
	timeoutOuter, timeoutCancel := context.WithTimeout(
		context.Background(),
		2*time.Second,
	)
	result, err := supervisor.Execute(timeoutOuter, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=hang"},
		Dir:        requestRuntime.Dir,
	})
	timeoutCancel()
	if err == nil {
		t.Fatalf("timeout unexpectedly succeeded: result=%+v", result)
	}
	assertRunErrorKind(t, err, ErrorTimeout)
	if result.StopReason != StopReasonSupervisorTimeout {
		t.Fatalf("timeout result=%+v", result)
	}
	if elapsed := time.Since(started); elapsed < timeoutLimits.Execution ||
		elapsed > 2*time.Second {
		t.Fatalf("timeout elapsed=%v", elapsed)
	}

	cancelLimits := supervisorTestLimits()
	cancelLimits.Execution = 30 * time.Second
	cancelLimits.TermGrace = 50 * time.Millisecond
	cancelLimits.Cleanup = 500 * time.Millisecond
	supervisor = newSupervisorForTest(t, cancelLimits)
	requestRuntime = prepareSupervisorRuntime(t, supervisor, "cancel001")
	outer, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(outer)
	timer := time.AfterFunc(10*time.Millisecond, cancel)
	defer timer.Stop()
	result, err = supervisor.Execute(ctx, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=hang"},
		Dir:        requestRuntime.Dir,
	})
	assertRunErrorKind(t, err, ErrorCanceled)
	if result.StopReason != StopReasonCallerCancellation {
		t.Fatalf("cancel result=%+v", result)
	}
}

func TestSupervisorCancellationUnblocksStdinWriter(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 30 * time.Second
	limits.TermGrace = 50 * time.Millisecond
	limits.Cleanup = 500 * time.Millisecond
	supervisor := newSupervisorForTest(t, limits)
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "stdinblock")
	outer, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(outer)
	timer := time.AfterFunc(10*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	result, err := supervisor.Execute(ctx, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=hang"},
		Dir:        requestRuntime.Dir,
		Stdin:      make([]byte, 8<<20),
	})
	assertRunErrorKind(t, err, ErrorCanceled)
	if result.StopReason != StopReasonCallerCancellation {
		t.Fatalf("result=%+v", result)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("blocked stdin writer delayed return: %v", elapsed)
	}
}

func TestSupervisorStdoutAndStderrOverflow(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "stdout", mode: "flood-stdout"},
		{name: "stderr", mode: "flood-stderr"},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := supervisorTestLimits()
			limits.StdoutBytes = 1024
			limits.StderrBytes = 1024
			supervisor := newSupervisorForTest(t, limits)
			requestRuntime := prepareSupervisorRuntime(t, supervisor, "overflow"+test.name)
			result, err := executeIntegration(
				t,
				supervisor,
				requestRuntime,
				CommandSpec{
					Executable: executable,
					Args:       []string{"--mode=" + test.mode},
					Dir:        requestRuntime.Dir,
				},
			)
			assertRunErrorKind(t, err, ErrorOutputLimit)
			if result.StopReason != StopReasonOutputOverflow {
				t.Fatalf("result=%+v", result)
			}
			if test.name == "stdout" {
				if len(result.Stdout) != 1024 || result.StdoutTotal <= 1024 {
					t.Fatalf("stdout len=%d total=%d", len(result.Stdout), result.StdoutTotal)
				}
			} else if len(result.Stderr) != 1024 || result.StderrTotal <= 1024 {
				t.Fatalf("stderr len=%d total=%d", len(result.Stderr), result.StderrTotal)
			}
		})
	}
}

func TestSupervisorEscalatesIgnoredTERMToKILL(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 30 * time.Second
	limits.TermGrace = 50 * time.Millisecond
	limits.Cleanup = 500 * time.Millisecond
	supervisor := newSupervisorForTest(t, limits)
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "killterm1")
	outer, outerCancel := integrationContext(t)
	defer outerCancel()
	ctx, cancel := context.WithCancel(outer)
	defer cancel()
	type execution struct {
		result Result
		err    error
	}
	executed := make(chan execution, 1)
	go func() {
		result, err := supervisor.Execute(ctx, requestRuntime, CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=ignore-term-ready"},
			Dir:        requestRuntime.Dir,
		})
		executed <- execution{result: result, err: err}
	}()

	waitForIntegrationPath(
		t,
		filepath.Join(requestRuntime.Dir, ".fake-child-ready"),
	)
	cancel()
	select {
	case outcome := <-executed:
		assertRunErrorKind(t, outcome.err, ErrorCanceled)
		if outcome.result.StopReason != StopReasonCallerCancellation ||
			outcome.result.StopAction != StopActionKILL {
			t.Fatalf("result=%+v", outcome.result)
		}
		assertPathGone(t, requestRuntime.Dir)
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("Execute did not return after canceling the ready fixture")
	}
}

func TestSupervisorStartsWaitOnlyAfterTERMSignalDecision(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 30 * time.Second
	limits.TermGrace = 50 * time.Millisecond
	limits.Cleanup = 500 * time.Millisecond
	supervisor := newSupervisorForTest(t, limits)
	waitStarted := make(chan struct{}, 1)
	termReached := make(chan struct{})
	releaseTERM := make(chan struct{})
	supervisor.hooks.beforeWait = func() {
		waitStarted <- struct{}{}
	}
	supervisor.hooks.beforeGroupSignal = func(signal syscall.Signal) {
		if signal != syscall.SIGTERM {
			return
		}
		close(termReached)
		<-releaseTERM
	}
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "waitafterterm")
	type execution struct {
		result Result
		err    error
	}
	executed := make(chan execution, 1)
	outer, outerCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer outerCancel()
	go func() {
		result, err := supervisor.Execute(
			outer,
			requestRuntime,
			CommandSpec{
				Executable: executable,
				Args:       []string{"--mode=spawn-child-hold"},
				Dir:        requestRuntime.Dir,
			},
		)
		executed <- execution{result: result, err: err}
	}()
	waitForRunnerEvent(t, termReached)
	select {
	case <-waitStarted:
		t.Fatal("Wait started before the TERM group signal decision")
	default:
	}
	close(releaseTERM)
	select {
	case outcome := <-executed:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.result.StopReason != StopReasonNormalExit ||
			(outcome.result.StopAction != StopActionTERM &&
				outcome.result.StopAction != StopActionKILL) {
			t.Fatalf("result=%+v", outcome.result)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("Execute did not return")
	}
}

func TestSupervisorStartsWaitOnlyAfterKILLSignalDecision(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 30 * time.Second
	limits.TermGrace = 20 * time.Millisecond
	limits.Cleanup = 500 * time.Millisecond
	supervisor := newSupervisorForTest(t, limits)
	waitStarted := make(chan struct{}, 1)
	killReached := make(chan struct{})
	releaseKILL := make(chan struct{})
	var releaseKILLOnce sync.Once
	releaseKILLDecision := func() {
		releaseKILLOnce.Do(func() {
			close(releaseKILL)
		})
	}
	t.Cleanup(releaseKILLDecision)
	supervisor.hooks.beforeWait = func() {
		waitStarted <- struct{}{}
	}
	supervisor.hooks.beforeGroupSignal = func(signal syscall.Signal) {
		if signal != syscall.SIGKILL {
			return
		}
		close(killReached)
		<-releaseKILL
	}
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "waitafterkill")
	outer, outerCancel := context.WithTimeout(
		context.Background(),
		unixOuterOwnerBudget,
	)
	defer outerCancel()
	ctx, cancel := context.WithCancel(outer)
	defer cancel()
	type execution struct {
		result Result
		err    error
	}
	executed := make(chan execution, 1)
	go func() {
		result, err := supervisor.Execute(
			ctx,
			requestRuntime,
			CommandSpec{
				Executable: executable,
				Args:       []string{"--mode=spawn-ignore-term-child"},
				Dir:        requestRuntime.Dir,
			},
		)
		executed <- execution{result: result, err: err}
	}()
	childReady := filepath.Join(requestRuntime.Dir, ".fake-child-ready")
	readinessDeadline := time.NewTimer(unixSchedulingWaitBudget)
	defer readinessDeadline.Stop()
	readinessPoll := time.NewTicker(time.Millisecond)
	defer readinessPoll.Stop()
	for {
		select {
		case outcome := <-executed:
			t.Fatalf(
				"Execute returned before child readiness: result=%+v error=%v",
				outcome.result,
				outcome.err,
			)
		default:
		}
		if _, err := os.Lstat(childReady); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("check child readiness path: %v", err)
		}
		select {
		case outcome := <-executed:
			t.Fatalf(
				"Execute returned before child readiness: result=%+v error=%v",
				outcome.result,
				outcome.err,
			)
		case <-readinessDeadline.C:
			t.Fatal("child readiness path did not become ready")
		case <-readinessPoll.C:
		}
	}
	cancel()
	waitForRunnerEvent(t, killReached)
	select {
	case <-waitStarted:
		t.Fatal("Wait started before the KILL group signal decision")
	default:
	}
	releaseKILLDecision()
	select {
	case outcome := <-executed:
		assertRunErrorKind(t, outcome.err, ErrorCanceled)
		if outcome.result.StopReason != StopReasonCallerCancellation ||
			outcome.result.StopAction != StopActionKILL {
			t.Fatalf("result=%+v", outcome.result)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("Execute did not return")
	}
}

func TestSupervisorGraceDoesNotConsumeCleanupBudget(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = time.Second
	limits.TermGrace = 50 * time.Millisecond
	limits.Cleanup = 10 * time.Millisecond
	supervisor := newSupervisorForTest(t, limits)
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "gracebudget")
	outer, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	result, err := supervisor.Execute(outer, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=ignore-term"},
		Dir:        requestRuntime.Dir,
	})
	assertRunErrorKind(t, err, ErrorTimeout)
	if result.StopAction != StopActionKILL {
		t.Fatalf("stop action=%q", result.StopAction)
	}
	if elapsed := time.Since(started); elapsed < limits.Execution+limits.TermGrace {
		t.Fatalf("grace was not accounted separately: %v", elapsed)
	}
}

func TestSupervisorExecuteRetainsAndDrainsDeferredRootWait(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 5 * time.Millisecond
	limits.TermGrace = 5 * time.Millisecond
	limits.Cleanup = 10 * time.Millisecond
	supervisor := newSupervisorForTest(t, limits)
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "waitdrain")
	beforeFDs := countOpenFDs(t)
	beforeGoroutines := runtime.NumGoroutine()
	waitRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseWait := func() {
		releaseOnce.Do(func() {
			close(waitRelease)
		})
	}
	t.Cleanup(releaseWait)
	supervisor.hooks.waitRelease = waitRelease

	runOuter, runCancel := context.WithTimeout(
		context.Background(),
		2*time.Second,
	)
	t.Cleanup(runCancel)
	type execution struct {
		result Result
		err    error
	}
	executed := make(chan execution, 1)
	go func() {
		result, err := supervisor.Execute(
			runOuter,
			requestRuntime,
			CommandSpec{
				Executable: executable,
				Args:       []string{"--mode=exit-7"},
				Dir:        requestRuntime.Dir,
			},
		)
		executed <- execution{result: result, err: err}
	}()

	waitForCompletionOwners(t, supervisor.completions, 1)
	select {
	case outcome := <-executed:
		t.Fatalf(
			"Execute released ownership before root Wait: result=%+v error=%v",
			outcome.result,
			outcome.err,
		)
	default:
	}

	firstCtx, firstCancel := context.WithTimeout(
		context.Background(),
		10*time.Millisecond,
	)
	err := supervisor.Shutdown(firstCtx)
	firstCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Shutdown error=%v", err)
	}
	if got := supervisor.completions.count(); got != 1 {
		t.Fatalf("timed-out Shutdown lost Wait ownership: %d", got)
	}
	if _, err := supervisor.Prepare("afterdown"); err == nil {
		t.Fatal("Prepare accepted work after Shutdown began")
	}
	rejected, err := supervisor.Execute(
		context.Background(),
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=text"},
			Dir:        requestRuntime.Dir,
		},
	)
	assertRunErrorKind(t, err, ErrorStart)
	if rejected.ExitCode != -1 {
		t.Fatalf("rejected Execute result=%+v", rejected)
	}
	if _, err := os.Lstat(requestRuntime.Dir); err != nil {
		t.Fatalf("runtime cleaned before retained Wait completed: %v", err)
	}

	releaseWait()
	select {
	case outcome := <-executed:
		runCancel()
		assertRunErrorKind(t, outcome.err, ErrorCleanup)
		if outcome.result.StopReason != StopReasonCleanupFailure ||
			outcome.result.ExitCode != -1 {
			t.Fatalf("deferred Wait result=%+v", outcome.result)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("Execute did not finish after the retained Wait completed")
	}
	if got := supervisor.completions.count(); got != 0 {
		t.Fatalf("Execute left pending Waits=%d", got)
	}
	assertPathGone(t, requestRuntime.Dir)
	secondCtx, secondCancel := integrationContext(t)
	err = supervisor.Shutdown(secondCtx)
	secondCancel()
	if err != nil {
		t.Fatal(err)
	}
	if got := supervisor.completions.count(); got != 0 {
		t.Fatalf("Shutdown left pending Waits=%d", got)
	}
	waitForResourceBaseline(t, beforeFDs, beforeGoroutines)
	if err := supervisor.root.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitForCompletionOwners(
	t *testing.T,
	owner *completionOwner,
	want int,
) {
	t.Helper()
	deadline := time.NewTimer(unixSchedulingWaitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for owner.count() != want {
		select {
		case <-deadline.C:
			t.Fatalf(
				"completion owners=%d, want %d",
				owner.count(),
				want,
			)
		case <-ticker.C:
		}
	}
}

func TestSupervisorMinimumGraceCleanupKeepsDeferredExitUnknown(
	t *testing.T,
) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 10 * time.Millisecond
	limits.TermGrace = time.Nanosecond
	limits.Cleanup = time.Nanosecond
	supervisor := newSupervisorForTest(t, limits)
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "minwait01")
	waitRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseWait := func() {
		releaseOnce.Do(func() {
			close(waitRelease)
		})
	}
	t.Cleanup(releaseWait)
	supervisor.hooks.waitRelease = waitRelease

	runOuter, runCancel := context.WithTimeout(
		context.Background(),
		2*time.Second,
	)
	t.Cleanup(runCancel)
	type execution struct {
		result Result
		err    error
	}
	executed := make(chan execution, 1)
	go func() {
		result, err := supervisor.Execute(
			runOuter,
			requestRuntime,
			CommandSpec{
				Executable: executable,
				Args:       []string{"--mode=ignore-term"},
				Dir:        requestRuntime.Dir,
			},
		)
		executed <- execution{result: result, err: err}
	}()
	waitForCompletionOwners(t, supervisor.completions, 1)
	select {
	case outcome := <-executed:
		t.Fatalf(
			"Execute released minimum-budget Wait ownership: result=%+v error=%v",
			outcome.result,
			outcome.err,
		)
	default:
	}

	releaseWait()
	select {
	case outcome := <-executed:
		runCancel()
		assertRunErrorKind(t, outcome.err, ErrorCleanup)
		if outcome.result.StopReason != StopReasonCleanupFailure ||
			outcome.result.ExitCode != -1 {
			t.Fatalf("minimum cleanup result=%+v", outcome.result)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("Execute did not finish after minimum-budget Wait completed")
	}
	if got := supervisor.completions.count(); got != 0 {
		t.Fatalf("Execute left pending Waits=%d", got)
	}
	ctx, cancel := integrationContext(t)
	defer cancel()
	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := supervisor.completions.count(); got != 0 {
		t.Fatalf("Shutdown left pending Waits=%d", got)
	}
}

func TestSupervisorReapsGroupAfterRootExitAndReturnsAfterCleanup(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "childhold")
	result, err := executeIntegration(t, supervisor, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=spawn-child-hold"},
		Dir:        requestRuntime.Dir,
		Files: []FileSpec{{
			Name: "request.json",
			Data: []byte(`{"fixed":true}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	childPID := parsePID(t, result.Stderr)
	assertProcessGone(t, childPID)
	assertPathGone(t, requestRuntime.Dir)
	if result.StopAction == StopActionNone {
		t.Fatalf("descendant was not terminated: %+v", result)
	}
}

func TestSupervisorRetainsExpectedGroupWhenLeaderLookupReportsESRCH(
	t *testing.T,
) {
	executable := testutil.BuildFakeCLI(t)
	tests := []struct {
		name            string
		mode            string
		wantDescendant  bool
		wantStopAction  bool
		requestIdentity string
	}{
		{
			name:            "already-exited root",
			mode:            "empty-success",
			requestIdentity: "pgidesrchroot",
		},
		{
			name:            "fast root with live descendant",
			mode:            "spawn-child-hold",
			wantDescendant:  true,
			wantStopAction:  true,
			requestIdentity: "pgidesrchchild",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			supervisor := newSupervisorForTest(t, supervisorTestLimits())
			supervisor.hooks.processGroupLookup = func(int) (int, error) {
				return -1, unix.ESRCH
			}
			requestRuntime := prepareSupervisorRuntime(
				t,
				supervisor,
				test.requestIdentity,
			)
			result, err := executeIntegration(
				t,
				supervisor,
				requestRuntime,
				CommandSpec{
					Executable: executable,
					Args:       []string{"--mode=" + test.mode},
					Dir:        requestRuntime.Dir,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != 0 ||
				result.StopReason != StopReasonNormalExit {
				t.Fatalf("result=%+v", result)
			}
			if test.wantDescendant {
				assertProcessGone(t, parsePID(t, result.Stderr))
			}
			if test.wantStopAction && result.StopAction == StopActionNone {
				t.Fatalf("expected group was not terminated: %+v", result)
			}
			assertPathGone(t, requestRuntime.Dir)
		})
	}
}

func TestRunUnixLeavesNoRecordedProcessGroup(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	root := openSupervisorTestRoot(t)
	requestRuntime, err := root.Prepare("pgidgone")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := integrationContext(t)
	defer cancel()
	outcome, err := runUnixOwned(
		ctx,
		root,
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=spawn-child-hold"},
			Dir:        requestRuntime.Dir,
		},
		supervisorTestLimits(),
		newCompletionOwner(),
		runnerHooks{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.pgid <= 0 {
		t.Fatalf("pgid=%d", outcome.pgid)
	}
	if err := unix.Kill(-outcome.pgid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("process group %d remains: %v", outcome.pgid, err)
	}
	cleanupCtx, cleanupCancel := integrationContext(t)
	defer cleanupCancel()
	if err := root.Cleanup(cleanupCtx, requestRuntime); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRepeatedCancelAndTimeoutRunsLeakNothing(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 5 * time.Millisecond
	limits.TermGrace = 5 * time.Millisecond
	limits.Cleanup = 500 * time.Millisecond
	supervisor := newSupervisorForTest(t, limits)
	beforeFDs := countOpenFDs(t)
	beforeGoroutines := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		requestRuntime := prepareSupervisorRuntime(
			t,
			supervisor,
			"repeat"+strconv.Itoa(1000+i),
		)
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		var cancel context.CancelFunc
		var timer *time.Timer
		if i%2 == 0 {
			ctx, cancel = context.WithCancel(ctx)
			timer = time.AfterFunc(time.Millisecond, cancel)
		}
		_, err := supervisor.Execute(ctx, requestRuntime, CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=hang"},
			Dir:        requestRuntime.Dir,
		})
		stop()
		if timer != nil {
			timer.Stop()
			cancel()
			assertRunErrorKind(t, err, ErrorCanceled)
		} else {
			assertRunErrorKind(t, err, ErrorTimeout)
		}
	}
	waitForResourceBaseline(t, beforeFDs, beforeGoroutines)
}

func TestSupervisorDocumentsSetsidEscapeAndHarnessKillsIt(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := supervisorTestLimits()
	limits.Execution = 2 * time.Second
	limits.TermGrace = 50 * time.Millisecond
	limits.Cleanup = 100 * time.Millisecond
	supervisor := newSupervisorForTest(t, limits)
	requestRuntime := prepareSupervisorRuntime(t, supervisor, "setsidesc")
	outer, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := supervisor.Execute(outer, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=spawn-session-escape"},
		Dir:        requestRuntime.Dir,
	})
	escapedPID := parsePID(t, result.Stderr)
	cleanupEscaped := func() {
		killErr := unix.Kill(escapedPID, unix.SIGKILL)
		if killErr != nil && !errors.Is(killErr, unix.ESRCH) {
			t.Errorf("kill escaped process: %v", killErr)
		}
		waitProcessGone(t, escapedPID)
	}
	t.Cleanup(cleanupEscaped)
	if err == nil {
		t.Fatalf("setsid escape unexpectedly succeeded: result=%+v", result)
	}
	assertRunErrorKind(t, err, ErrorCleanup)
	if err := unix.Kill(escapedPID, 0); err != nil {
		t.Fatalf("setsid escape was unexpectedly contained: %v", err)
	}
	cleanupEscaped()
}

func TestSupervisorSelfTestUsesFixedHiddenMode(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-be-used")
	gateway := testutil.BuildGateway(t)
	supervisor := newSupervisorForTest(t, supervisorTestLimits())
	ctx, cancel := integrationContext(t)
	defer cancel()
	if err := supervisor.SelfTest(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(supervisor.root.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), requestPrefix+"selftest") {
			t.Fatalf("self-test runtime remains: %s", entry.Name())
		}
	}
}

func TestProcessSelfTestChildAcknowledgesReadinessAndIgnoresUnrelatedSignals(
	t *testing.T,
) {
	gateway := testutil.BuildGateway(t)
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = readyReader.Close()
		_ = readyWriter.Close()
	}()
	// The fixed hidden child is invoked directly without a shell. Descriptor 3
	// is its inherited readiness control pipe.
	//nolint:gosec,noctx
	cmd := exec.Command(gateway, "__process-selftest", "child")
	cmd.Env = []string{}
	cmd.ExtraFiles = []*os.File{readyWriter}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyWriter.Close()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitDone:
		case <-time.After(unixSchedulingWaitBudget):
			t.Errorf("self-test child Wait did not finish")
		}
	})

	acknowledged := make(chan error, 1)
	go func() {
		var ready [1]byte
		_, err := io.ReadFull(readyReader, ready[:])
		if err == nil && ready[0] != 1 {
			err = fmt.Errorf("readiness byte=%d", ready[0])
		}
		acknowledged <- err
	}()
	select {
	case err := <-acknowledged:
		if err != nil {
			t.Fatalf("child readiness acknowledgment: %v", err)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("child did not acknowledge signal-handler readiness")
	}

	for _, signal := range []syscall.Signal{
		syscall.SIGURG,
		syscall.SIGWINCH,
	} {
		if err := cmd.Process.Signal(signal); err != nil {
			t.Fatalf("send %s: %v", signal, err)
		}
		select {
		case err := <-waitDone:
			t.Fatalf("child exited on %s: %v", signal, err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("child exit after SIGTERM: %v", err)
		}
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("child did not exit after SIGTERM")
	}
}

func TestProcessSelfTestModeIsAbsentFromPublicHelp(t *testing.T) {
	gateway := testutil.BuildGateway(t)
	// The test-owned gateway executable is invoked directly without a shell.
	ctx, cancel := integrationContext(t)
	defer cancel()
	//nolint:gosec
	cmd := exec.CommandContext(ctx, gateway, "--help")
	cmd.Env = []string{}
	output, _ := cmd.CombinedOutput()
	if strings.Contains(string(output), "__process-selftest") {
		t.Fatalf("hidden self-test appeared in public help: %q", output)
	}
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	return countUnitOpenFDs(t)
}

func waitForResourceBaseline(
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
		currentFDs := countOpenFDs(t)
		currentGoroutines := runtime.NumGoroutine()
		if currentFDs <= beforeFDs+2 && currentGoroutines <= beforeGoroutines+2 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf(
				"resources did not return: fds %d->%d goroutines %d->%d",
				beforeFDs,
				currentFDs,
				beforeGoroutines,
				currentGoroutines,
			)
		case <-ticker.C:
		}
	}
}

func parsePID(t *testing.T, data []byte) int {
	t.Helper()
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		t.Fatalf("missing PID in %q", data)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		t.Fatalf("invalid PID in %q: %v", data, err)
	}
	return pid
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("process %d remains: %v", pid, err)
	}
}

func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.NewTimer(unixSchedulingWaitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("process %d did not exit", pid)
		case <-ticker.C:
		}
	}
}

func integrationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), unixOuterOwnerBudget)
}

func executeIntegration(
	t *testing.T,
	supervisor *Supervisor,
	runtime Runtime,
	spec CommandSpec,
) (Result, error) {
	t.Helper()
	ctx, cancel := integrationContext(t)
	defer cancel()
	return supervisor.Execute(ctx, runtime, spec)
}

func waitForRunnerEvent(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(unixSchedulingWaitBudget):
		t.Fatal("runner event did not become ready")
	}
}

func waitForRunnerCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(unixSchedulingWaitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("runner condition did not become true")
		case <-ticker.C:
		}
	}
}

func waitForIntegrationPath(t *testing.T, path string) {
	t.Helper()
	waitForRunnerCondition(t, func() bool {
		_, err := os.Lstat(path)
		return err == nil
	})
}
