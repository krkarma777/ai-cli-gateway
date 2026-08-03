//go:build integration && windows

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/windows"
)

const (
	windowsSchedulingWaitBudget = 30 * time.Second
	windowsOuterOwnerBudget     = 60 * time.Second
)

func TestWindowsSupervisorPromptStreamsAndExitCodes(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newWindowsSupervisorForTest(t, windowsSupervisorTestLimits())

	echoRuntime := prepareWindowsSupervisorRuntime(t, supervisor, "winecho01")
	echo, err := executeWindowsIntegration(t, supervisor, echoRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=echo-stdin"},
		Dir:        echoRuntime.Dir,
		Stdin:      []byte("fixed prompt only on stdin\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(echo.Stdout) != "fixed prompt only on stdin\n" ||
		len(echo.Stderr) != 0 ||
		echo.ExitCode != 0 ||
		echo.StopReason != StopReasonNormalExit {
		t.Fatalf("echo result=%+v", echo)
	}

	stderrRuntime := prepareWindowsSupervisorRuntime(t, supervisor, "winstderr")
	stderrResult, err := executeWindowsIntegration(
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

	exitRuntime := prepareWindowsSupervisorRuntime(t, supervisor, "winexit07")
	exitResult, err := executeWindowsIntegration(
		t,
		supervisor,
		exitRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=exit-7"},
			Dir:        exitRuntime.Dir,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if exitResult.ExitCode != 7 ||
		exitResult.StopReason != StopReasonNormalExit ||
		exitResult.StopAction != StopActionNone {
		t.Fatalf("exit result=%+v", exitResult)
	}
}

func TestWindowsSupervisorTestBudgetClassesRemainSeparated(t *testing.T) {
	generic := windowsSupervisorTestLimits()
	if generic.Execution != 30*time.Second ||
		generic.TermGrace != 50*time.Millisecond ||
		generic.Cleanup != 30*time.Second {
		t.Fatalf("generic limits=%+v", generic)
	}
	if windowsSchedulingWaitBudget != 30*time.Second {
		t.Fatalf("scheduling wait=%v want=30s", windowsSchedulingWaitBudget)
	}
	if windowsOuterOwnerBudget != 60*time.Second {
		t.Fatalf("outer owner=%v want=60s", windowsOuterOwnerBudget)
	}

	semantic := []struct {
		name   string
		limits Limits
	}{
		{
			name: "timeout",
			limits: Limits{
				Execution: 100 * time.Millisecond,
				TermGrace: 50 * time.Millisecond,
				Cleanup:   500 * time.Millisecond,
			},
		},
		{
			name: "locked cleanup",
			limits: Limits{
				Execution: 30 * time.Second,
				TermGrace: 50 * time.Millisecond,
				Cleanup:   100 * time.Millisecond,
			},
		},
		{
			name: "handle stress timeout",
			limits: Limits{
				Execution: 20 * time.Millisecond,
				TermGrace: 50 * time.Millisecond,
				Cleanup:   500 * time.Millisecond,
			},
		},
	}
	for _, test := range semantic {
		t.Run(test.name, func(t *testing.T) {
			if test.limits.Cleanup >= generic.Cleanup {
				t.Fatalf(
					"semantic limits=%+v inherited generic=%+v",
					test.limits,
					generic,
				)
			}
		})
	}
}

func TestWindowsSupervisorLiveCancellationAtCreateBoundary(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newWindowsSupervisorForTest(t, windowsSupervisorTestLimits())
	baseline := stableWindowsProcessHandleCount(t)
	requestRuntime := prepareWindowsSupervisorRuntime(
		t,
		supervisor,
		"winprec01",
	)
	ctx, cancel := context.WithCancel(context.Background())
	resumed := make(chan struct{}, 1)
	supervisor.hooks.beforeCreateProcess = func(windowsLaunchView) error {
		cancel()
		return nil
	}
	supervisor.hooks.afterResume = func(windowsLaunchView) {
		resumed <- struct{}{}
	}
	result, err := supervisor.Execute(ctx, requestRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=text"},
		Dir:        requestRuntime.Dir,
	})
	assertWindowsRunErrorKind(t, err, ErrorCanceled)
	if result.StopReason != StopReasonCallerCancellation ||
		result.StopAction != StopActionNone {
		t.Fatalf("pre-create cancellation result=%+v", result)
	}
	select {
	case <-resumed:
		t.Fatal("provider resumed after live pre-create cancellation")
	default:
	}
	if got := stableWindowsProcessHandleCount(t); got != baseline {
		t.Fatalf("process handle count=%d want baseline=%d", got, baseline)
	}
}

func TestWindowsSupervisorTimeoutCancellationAndOverflow(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)

	timeoutLimits := windowsSupervisorTestLimits()
	timeoutLimits.Execution = 100 * time.Millisecond
	timeoutLimits.TermGrace = 50 * time.Millisecond
	timeoutLimits.Cleanup = 500 * time.Millisecond
	timeoutSupervisor := newWindowsSupervisorForTest(t, timeoutLimits)
	timeoutRuntime := prepareWindowsSupervisorRuntime(
		t,
		timeoutSupervisor,
		"wintime01",
	)
	timeoutOuter, timeoutCancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	timeoutResult, err := timeoutSupervisor.Execute(
		timeoutOuter,
		timeoutRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=hang"},
			Dir:        timeoutRuntime.Dir,
		},
	)
	timeoutCancel()
	assertWindowsRunErrorKind(t, err, ErrorTimeout)
	if timeoutResult.StopReason != StopReasonSupervisorTimeout ||
		timeoutResult.StopAction != StopActionTerminateJob {
		t.Fatalf("timeout result=%+v", timeoutResult)
	}

	cancellationLimits := windowsSupervisorTestLimits()
	cancellationLimits.Execution = 30 * time.Second
	cancellationLimits.TermGrace = 50 * time.Millisecond
	cancellationLimits.Cleanup = 500 * time.Millisecond
	cancelSupervisor := newWindowsSupervisorForTest(t, cancellationLimits)
	cancelRuntime := prepareWindowsSupervisorRuntime(
		t,
		cancelSupervisor,
		"wincancel",
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelResult, err := cancelSupervisor.Execute(ctx, cancelRuntime, CommandSpec{
		Executable: executable,
		Args:       []string{"--mode=hang"},
		Dir:        cancelRuntime.Dir,
	})
	assertWindowsRunErrorKind(t, err, ErrorCanceled)
	if cancelResult.StopReason != StopReasonCallerCancellation {
		t.Fatalf("pre-cancel result=%+v", cancelResult)
	}

	postStartSupervisor := newWindowsSupervisorForTest(t, cancellationLimits)
	postStartRuntime := prepareWindowsSupervisorRuntime(
		t,
		postStartSupervisor,
		"winpostcan",
	)
	started := make(chan struct{})
	var startedOnce sync.Once
	postStartSupervisor.hooks.afterResume = func(windowsLaunchView) {
		startedOnce.Do(func() {
			close(started)
		})
	}
	postOuter, postOuterCancel := context.WithTimeout(
		context.Background(),
		windowsOuterOwnerBudget,
	)
	defer postOuterCancel()
	postContext, postCancel := context.WithCancel(postOuter)
	type execution struct {
		result Result
		err    error
	}
	executed := make(chan execution, 1)
	go func() {
		result, executeErr := postStartSupervisor.Execute(
			postContext,
			postStartRuntime,
			CommandSpec{
				Executable: executable,
				Args:       []string{"--mode=hang"},
				Dir:        postStartRuntime.Dir,
				Stdin:      make([]byte, 8<<20),
			},
		)
		executed <- execution{result: result, err: executeErr}
	}()
	select {
	case <-started:
		postCancel()
	case <-time.After(windowsSchedulingWaitBudget):
		postCancel()
		t.Fatal("provider was not resumed")
	}
	select {
	case outcome := <-executed:
		assertWindowsRunErrorKind(t, outcome.err, ErrorCanceled)
		if outcome.result.StopReason != StopReasonCallerCancellation ||
			outcome.result.StopAction != StopActionTerminateJob {
			t.Fatalf("post-start cancel result=%+v", outcome.result)
		}
	case <-time.After(windowsSchedulingWaitBudget):
		t.Fatal("post-start cancellation did not return")
	}

	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "stdout", mode: "flood-stdout"},
		{name: "stderr", mode: "flood-stderr"},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := windowsSupervisorTestLimits()
			limits.StdoutBytes = 1024
			limits.StderrBytes = 1024
			supervisor := newWindowsSupervisorForTest(t, limits)
			requestRuntime := prepareWindowsSupervisorRuntime(
				t,
				supervisor,
				"winover"+test.name,
			)
			result, err := executeWindowsIntegration(
				t,
				supervisor,
				requestRuntime,
				CommandSpec{
					Executable: executable,
					Args:       []string{"--mode=" + test.mode},
					Dir:        requestRuntime.Dir,
				},
			)
			assertWindowsRunErrorKind(t, err, ErrorOutputLimit)
			if result.StopReason != StopReasonOutputOverflow ||
				result.StopAction != StopActionTerminateJob {
				t.Fatalf("overflow result=%+v", result)
			}
			if test.name == "stdout" &&
				(len(result.Stdout) != 1024 || result.StdoutTotal <= 1024) {
				t.Fatalf("stdout result=%+v", result)
			}
			if test.name == "stderr" &&
				(len(result.Stderr) != 1024 || result.StderrTotal <= 1024) {
				t.Fatalf("stderr result=%+v", result)
			}
		})
	}

	t.Run("stdout-exit-after-over-limit", func(t *testing.T) {
		limits := windowsSupervisorTestLimits()
		limits.StdoutBytes = 1024
		supervisor := newWindowsSupervisorForTest(t, limits)
		requestRuntime := prepareWindowsSupervisorRuntime(
			t,
			supervisor,
			"winoverexit",
		)
		result, err := executeWindowsIntegration(
			t,
			supervisor,
			requestRuntime,
			CommandSpec{
				Executable: executable,
				Args:       []string{"--mode=flood-once-exit-7"},
				Dir:        requestRuntime.Dir,
			},
		)
		assertWindowsRunErrorKind(t, err, ErrorOutputLimit)
		if result.StopReason != StopReasonOutputOverflow ||
			len(result.Stdout) != 1024 ||
			result.StdoutTotal <= 1024 {
			t.Fatalf("exit-after-overflow result=%+v", result)
		}
	})
}

type observedWindowsIOAPI struct {
	windowsAPI
	writeEntered    chan struct{}
	writeOnce       sync.Once
	cancelSucceeded chan struct{}
	cancelOnce      sync.Once
}

func (a *observedWindowsIOAPI) writeFile(
	handle windows.Handle,
	data []byte,
) (uint32, error) {
	a.writeOnce.Do(func() {
		close(a.writeEntered)
	})
	return a.windowsAPI.writeFile(handle, data)
}

func (a *observedWindowsIOAPI) cancelSynchronousIO(
	thread windows.Handle,
) error {
	err := a.windowsAPI.cancelSynchronousIO(thread)
	if err == nil {
		a.cancelOnce.Do(func() {
			close(a.cancelSucceeded)
		})
	}
	return err
}

func TestWindowsNativeCancellationDrainsDeferredBlockedWriter(t *testing.T) {
	baseline := stableWindowsProcessHandleCount(t)
	api := &observedWindowsIOAPI{
		windowsAPI:      nativeWindowsAPI{},
		writeEntered:    make(chan struct{}),
		cancelSucceeded: make(chan struct{}),
	}
	readHandle, writeHandle, err := api.createPipe()
	if err != nil {
		t.Fatal(err)
	}
	ledger := newWindowsHandleLedger(api)
	readOwner := ledger.acquire(readHandle, "test pipe read")
	writeOwner := ledger.acquire(writeHandle, "test pipe write")
	completions := newCompletionOwner()
	started := make(chan windowsIOWorker, 1)
	writerDone := make(chan error, 1)
	var (
		worker     windowsIOWorker
		workerSeen bool
		handedOff  bool
	)
	t.Cleanup(func() {
		if workerSeen {
			worker.cancellation.request(api, worker.thread)
		}
		_ = readOwner.close()
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			windowsOuterOwnerBudget,
		)
		defer cleanupCancel()
		if handedOff {
			_ = completions.drain(cleanupContext)
		} else if workerSeen {
			select {
			case <-writerDone:
			case <-cleanupContext.Done():
			}
		}
		_ = ledger.closeReverse()
	})

	go writeWindowsStdin(
		api,
		ledger,
		writeOwner,
		make([]byte, 8<<20),
		started,
		writerDone,
	)
	worker = <-started
	workerSeen = true
	select {
	case <-api.writeEntered:
	case <-time.After(windowsSchedulingWaitBudget):
		t.Fatal("native stdin worker did not enter WriteFile")
	}

	process := &windowsProcess{
		api:         api,
		ledger:      ledger,
		completions: completions,
		pid:         uint32(os.Getpid()),
		stdinParent: writeOwner,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, joinErr := joinWindowsCompletions(
		ctx,
		make(chan struct{}),
		process,
		windowsIOWorkers{stdin: worker},
		windowsRootWait{},
		writerDone,
		make(chan readerResult),
		make(chan readerResult),
		windowsCompletionState{
			wait:       waitResult{exitCode: 0},
			waitSeen:   true,
			stdoutSeen: true,
			stderrSeen: true,
		},
	)
	if !errors.Is(joinErr, context.Canceled) {
		t.Fatalf("join error=%v", joinErr)
	}
	handedOff = true
	if completions.count() != 1 {
		t.Fatalf("deferred completions=%d", completions.count())
	}
	select {
	case <-api.cancelSucceeded:
	case <-time.After(windowsSchedulingWaitBudget):
		_ = readOwner.close()
		t.Fatal("real CancelSynchronousIo never found the pending write")
	}
	drainContext, drainCancel := context.WithTimeout(
		context.Background(),
		windowsOuterOwnerBudget,
	)
	defer drainCancel()
	if err := completions.drain(drainContext); err != nil {
		t.Fatal(err)
	}
	if err := readOwner.close(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.closeReverse(); err != nil {
		t.Fatal(err)
	}
	if got := stableWindowsProcessHandleCount(t); got != baseline {
		t.Fatalf("process handle count=%d want baseline=%d", got, baseline)
	}
}

func TestWindowsSupervisorDrainsBothStreamsUnderConcurrentPressure(
	t *testing.T,
) {
	const streamBytes = 512 * 1024

	executable := testutil.BuildFakeCLI(t)
	limits := windowsSupervisorTestLimits()
	limits.StdoutBytes = streamBytes
	limits.StderrBytes = streamBytes
	supervisor := newWindowsSupervisorForTest(t, limits)
	requestRuntime := prepareWindowsSupervisorRuntime(
		t,
		supervisor,
		"winboth01",
	)
	result, err := executeWindowsIntegration(
		t,
		supervisor,
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=pressure-both"},
			Dir:        requestRuntime.Dir,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 ||
		result.StdoutTotal != streamBytes ||
		result.StderrTotal != streamBytes ||
		len(result.Stdout) != streamBytes ||
		len(result.Stderr) != streamBytes ||
		!bytes.Equal(result.Stdout, bytes.Repeat([]byte{'o'}, streamBytes)) ||
		!bytes.Equal(result.Stderr, bytes.Repeat([]byte{'e'}, streamBytes)) {
		t.Fatalf(
			"dual-stream result exit=%d stdout=%d/%d stderr=%d/%d",
			result.ExitCode,
			len(result.Stdout),
			result.StdoutTotal,
			len(result.Stderr),
			result.StderrTotal,
		)
	}
}

func TestWindowsSupervisorObservesRealJobEmptyBeforeClose(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newWindowsSupervisorForTest(t, windowsSupervisorTestLimits())
	requestRuntime := prepareWindowsSupervisorRuntime(
		t,
		supervisor,
		"winjob001",
	)
	type observation struct {
		accounting windowsJobAccounting
		err        error
	}
	observed := make(chan observation, 1)
	supervisor.hooks.beforeJobClose = func(view windowsJobCloseView) {
		accounting, err := (nativeWindowsAPI{}).queryJobAccounting(view.job)
		observed <- observation{accounting: accounting, err: err}
	}
	result, err := executeWindowsIntegration(
		t,
		supervisor,
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=text"},
			Dir:        requestRuntime.Dir,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	select {
	case item := <-observed:
		if item.err != nil {
			t.Fatalf("query open Job from pre-close hook: %v", item.err)
		}
		if item.accounting.TotalProcesses == 0 ||
			item.accounting.ActiveProcesses != 0 {
			t.Fatalf("pre-close Job accounting=%+v", item.accounting)
		}
	case <-time.After(windowsSchedulingWaitBudget):
		t.Fatal("Job pre-close observation hook was not called")
	}
}

func TestWindowsSupervisorConcurrentLaunchesInheritOnlyOwnStdio(
	t *testing.T,
) {
	executable := testutil.BuildFakeCLI(t)
	supervisors := [2]*Supervisor{
		newWindowsSupervisorForTest(t, windowsSupervisorTestLimits()),
		newWindowsSupervisorForTest(t, windowsSupervisorTestLimits()),
	}
	runtimes := [2]Runtime{
		prepareWindowsSupervisorRuntime(t, supervisors[0], "winiso001"),
		prepareWindowsSupervisorRuntime(t, supervisors[1], "winiso002"),
	}

	attributes := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	planted, err := windows.CreateEvent(attributes, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = windows.CloseHandle(planted)
	}()

	type indexedView struct {
		index int
		view  windowsLaunchView
	}
	type indexedProbe struct {
		index int
		err   error
	}
	views := make(chan indexedView, 2)
	probes := make(chan indexedProbe, 2)
	release := make(chan struct{})
	releaseResumes := make(chan struct{})
	var releaseOnce sync.Once
	var releaseResumesOnce sync.Once
	releaseLaunches := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	t.Cleanup(releaseLaunches)

	var captured [2]windowsLaunchView
	var retained [2][3]windows.Handle
	var retainedPlanted windows.Handle
	var retainedCloseOnce sync.Once
	var retainedCloseErr error
	closeRetained := func() error {
		retainedCloseOnce.Do(func() {
			var closeErrors []error
			for launchIndex := range retained {
				for streamIndex, handle := range retained[launchIndex] {
					if handle == 0 {
						continue
					}
					closeErrors = append(
						closeErrors,
						windows.CloseHandle(handle),
					)
					retained[launchIndex][streamIndex] = 0
				}
			}
			if retainedPlanted != 0 {
				closeErrors = append(
					closeErrors,
					windows.CloseHandle(retainedPlanted),
				)
				retainedPlanted = 0
			}
			retainedCloseErr = errors.Join(closeErrors...)
		})
		return retainedCloseErr
	}
	t.Cleanup(func() {
		if err := closeRetained(); err != nil {
			t.Errorf("close retained identity handles: %v", err)
		}
	})
	releaseProbedLaunches := func() {
		releaseResumesOnce.Do(func() {
			close(releaseResumes)
		})
	}
	t.Cleanup(releaseProbedLaunches)
	for index := range supervisors {
		index := index
		supervisors[index].hooks.beforeCreateProcess = func(
			view windowsLaunchView,
		) error {
			views <- indexedView{index: index, view: view}
			<-release
			return nil
		}
		supervisors[index].hooks.beforeResume = func(
			view windowsLaunchView,
		) (probeErr error) {
			defer func() {
				probes <- indexedProbe{index: index, err: probeErr}
				<-releaseResumes
			}()
			peer := 1 - index
			candidates := [4]windows.Handle{
				captured[peer].childHandles[0],
				captured[peer].childHandles[1],
				captured[peer].childHandles[2],
				planted,
			}
			expected := [4]windows.Handle{
				retained[peer][0],
				retained[peer][1],
				retained[peer][2],
				retainedPlanted,
			}
			for candidateIndex, candidate := range candidates {
				inherited, probeErr := probeWindowsHandleIdentity(
					liveWindowsHandleIdentityOps,
					view.process,
					candidate,
					expected[candidateIndex],
				)
				if probeErr != nil {
					return fmt.Errorf(
						"inspect candidate %d before resume: %w",
						candidateIndex,
						probeErr,
					)
				}
				if inherited {
					return fmt.Errorf(
						"candidate %d inherited a forbidden object",
						candidateIndex,
					)
				}
			}
			return nil
		}
	}

	type launchResult struct {
		index  int
		result Result
		err    error
	}
	results := make(chan launchResult, 2)
	for index := range supervisors {
		index := index
		go func() {
			result, executeErr := executeWindowsIntegration(
				t,
				supervisors[index],
				runtimes[index],
				CommandSpec{
					Executable: executable,
					Args:       []string{"--mode=text"},
					Dir:        runtimes[index].Dir,
				},
			)
			results <- launchResult{
				index:  index,
				result: result,
				err:    executeErr,
			}
		}()
	}

	for range captured {
		select {
		case item := <-views:
			captured[item.index] = item.view
		case <-time.After(windowsSchedulingWaitBudget):
			releaseLaunches()
			t.Fatal("concurrent launch did not reach handle barrier")
		}
	}
	for launchIndex := range captured {
		for streamIndex, handle := range captured[launchIndex].childHandles {
			retained[launchIndex][streamIndex] =
				duplicateWindowsHandleForIdentity(t, handle)
		}
	}
	retainedPlanted = duplicateWindowsHandleForIdentity(t, planted)
	releaseLaunches()
	var probeErrors []error
	for range supervisors {
		select {
		case item := <-probes:
			if item.err != nil {
				probeErrors = append(probeErrors, fmt.Errorf(
					"launch %d handle probe: %w",
					item.index,
					item.err,
				))
			}
		case <-time.After(windowsSchedulingWaitBudget):
			t.Fatal("concurrent launch did not finish handle probes")
		}
	}
	closeErr := closeRetained()
	releaseProbedLaunches()
	if closeErr != nil {
		t.Fatalf("close retained identity handles: %v", closeErr)
	}
	if err := errors.Join(probeErrors...); err != nil {
		t.Fatal(err)
	}
	for range supervisors {
		select {
		case item := <-results:
			if item.err != nil {
				t.Fatalf("launch %d: %v", item.index, item.err)
			}
			if item.result.ExitCode != 0 ||
				item.result.StopReason != StopReasonNormalExit ||
				string(item.result.Stdout) != "hello\n" ||
				len(item.result.Stderr) != 0 {
				t.Fatalf(
					"launch %d result=%+v",
					item.index,
					item.result,
				)
			}
		case <-time.After(windowsSchedulingWaitBudget):
			t.Fatal("concurrent launch did not finish")
		}
	}
}

func duplicateWindowsHandleForIdentity(
	t *testing.T,
	source windows.Handle,
) windows.Handle {
	t.Helper()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		source,
		windows.CurrentProcess(),
		&duplicate,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		t.Fatal(err)
	}
	return duplicate
}

func TestWindowsSupervisorRootExitTerminatesPipeHoldingDescendant(
	t *testing.T,
) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newWindowsSupervisorForTest(t, windowsSupervisorTestLimits())
	requestRuntime := prepareWindowsSupervisorRuntime(
		t,
		supervisor,
		"winchild1",
	)
	started := time.Now()
	result, err := executeWindowsIntegration(
		t,
		supervisor,
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=spawn-child-hold"},
			Dir:        requestRuntime.Dir,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 ||
		result.StopReason != StopReasonNormalExit ||
		result.StopAction != StopActionTerminateJob {
		t.Fatalf("descendant result=%+v", result)
	}
	if time.Since(started) >= time.Second {
		t.Fatalf("descendant pipe delayed EOF: %v", time.Since(started))
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(result.Stderr)))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("descendant PID %q: %v", result.Stderr, parseErr)
	}
	assertWindowsProcessGone(t, uint32(pid))
}

func TestWindowsSupervisorTerminatesChildAndGrandchild(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newWindowsSupervisorForTest(t, windowsSupervisorTestLimits())
	requestRuntime := prepareWindowsSupervisorRuntime(
		t,
		supervisor,
		"wingrand1",
	)
	result, err := executeWindowsIntegration(
		t,
		supervisor,
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=spawn-grandchild-hold"},
			Dir:        requestRuntime.Dir,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != StopReasonNormalExit ||
		result.StopAction != StopActionTerminateJob {
		t.Fatalf("grandchild result=%+v", result)
	}
	fields := strings.Fields(string(result.Stderr))
	if len(fields) != 2 {
		t.Fatalf("grandchild PIDs=%q", result.Stderr)
	}
	for _, field := range fields {
		pid, parseErr := strconv.ParseUint(field, 10, 32)
		if parseErr != nil || pid == 0 {
			t.Fatalf("grandchild PID %q: %v", field, parseErr)
		}
		assertWindowsProcessGone(t, uint32(pid))
	}
}

func TestWindowsSupervisorSelfTestUsesJobContainment(t *testing.T) {
	gateway := testutil.BuildGateway(t)
	supervisor := newWindowsSupervisorForTest(t, windowsSupervisorTestLimits())
	ctx, cancel := context.WithTimeout(
		context.Background(),
		windowsOuterOwnerBudget,
	)
	defer cancel()
	if err := supervisor.SelfTest(ctx, gateway); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsSupervisorLockedRuntimeCleanupIsBounded(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	limits := windowsSupervisorTestLimits()
	limits.Execution = 30 * time.Second
	limits.TermGrace = 50 * time.Millisecond
	limits.Cleanup = 100 * time.Millisecond
	supervisor := newWindowsSupervisorForTest(t, limits)
	requestRuntime := prepareWindowsSupervisorRuntime(
		t,
		supervisor,
		"winlocked",
	)

	var (
		lockMu sync.Mutex
		lock   windows.Handle
	)
	supervisor.hooks.beforeCreateProcess = func(view windowsLaunchView) error {
		path, err := windows.UTF16PtrFromString(
			filepath.Join(view.directory, "locked.txt"),
		)
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(
			path,
			windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if err != nil {
			return err
		}
		lockMu.Lock()
		lock = handle
		lockMu.Unlock()
		return nil
	}
	started := time.Now()
	outer, outerCancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	result, err := supervisor.Execute(
		outer,
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--mode=text"},
			Dir:        requestRuntime.Dir,
			Files: []FileSpec{{
				Name: "locked.txt",
				Data: []byte("locked\n"),
			}},
		},
	)
	outerCancel()
	assertWindowsRunErrorKind(t, err, ErrorCleanup)
	if result.StopReason != StopReasonCleanupFailure {
		t.Fatalf("locked cleanup result=%+v", result)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("locked cleanup was not bounded: %v", elapsed)
	}
	lockMu.Lock()
	closeErr := windows.CloseHandle(lock)
	lock = 0
	lockMu.Unlock()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	janitorCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := supervisor.root.Janitor(janitorCtx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		requestPrefix + requestRuntime.ID,
		quarantinePrefix + requestRuntime.ID,
	} {
		if _, statErr := os.Lstat(filepath.Join(supervisor.root.path, name)); !errors.Is(
			statErr,
			os.ErrNotExist,
		) {
			t.Fatalf("locked runtime remains as %s: %v", name, statErr)
		}
	}
}

func TestWindowsSupervisorHandleCountReturnsToQuiescentBaseline(
	t *testing.T,
) {
	executable := testutil.BuildFakeCLI(t)
	supervisor := newWindowsSupervisorForTest(
		t,
		windowsSupervisorTestLimits(),
	)
	timeoutLimits := windowsSupervisorTestLimits()
	timeoutLimits.Execution = 20 * time.Millisecond
	timeoutLimits.TermGrace = 50 * time.Millisecond
	timeoutLimits.Cleanup = 500 * time.Millisecond
	timeoutSupervisor := newWindowsSupervisorForTest(t, timeoutLimits)
	cancellationLimits := windowsSupervisorTestLimits()
	cancellationLimits.Execution = 30 * time.Second
	cancellationLimits.TermGrace = 50 * time.Millisecond
	cancellationLimits.Cleanup = 500 * time.Millisecond
	cancellationSupervisor := newWindowsSupervisorForTest(t, cancellationLimits)
	overflowLimits := windowsSupervisorTestLimits()
	overflowLimits.StdoutBytes = 1024
	overflowSupervisor := newWindowsSupervisorForTest(t, overflowLimits)

	const iterations = 100
	const batchSize = 8
	var cancellationStarts sync.Map
	cancellationSupervisor.hooks.afterResume = func(view windowsLaunchView) {
		value, exists := cancellationStarts.LoadAndDelete(view.directory)
		if exists {
			close(value.(chan struct{}))
		}
	}

	scenarios := []struct {
		name     string
		run      func(int) (Result, error)
		validate func(*testing.T, Result, error)
	}{
		{
			name: "success",
			run: func(index int) (Result, error) {
				requestRuntime, err := supervisor.Prepare(
					fmt.Sprintf("winok%04d", index),
				)
				if err != nil {
					return Result{}, err
				}
				return executeWindowsStressRequest(
					supervisor,
					requestRuntime,
					CommandSpec{
						Executable: executable,
						Args:       []string{"--mode=text"},
						Dir:        requestRuntime.Dir,
					},
				)
			},
			validate: func(t *testing.T, result Result, err error) {
				t.Helper()
				if err != nil ||
					result.ExitCode != 0 ||
					result.StopReason != StopReasonNormalExit {
					t.Fatalf("success result=%+v err=%v", result, err)
				}
			},
		},
		{
			name: "start-failure",
			run: func(index int) (Result, error) {
				requestRuntime, err := supervisor.Prepare(
					fmt.Sprintf("winfail%03d", index),
				)
				if err != nil {
					return Result{}, err
				}
				return executeWindowsStressRequest(
					supervisor,
					requestRuntime,
					CommandSpec{
						Executable: filepath.Join(
							requestRuntime.Dir,
							"bad-provider.exe",
						),
						Dir: requestRuntime.Dir,
						Files: []FileSpec{{
							Name: "bad-provider.exe",
							Data: []byte("not a Windows executable"),
						}},
					},
				)
			},
			validate: func(t *testing.T, _ Result, err error) {
				t.Helper()
				assertWindowsRunErrorKind(t, err, ErrorStart)
			},
		},
		{
			name: "timeout",
			run: func(index int) (Result, error) {
				requestRuntime, err := timeoutSupervisor.Prepare(
					fmt.Sprintf("wintmo%04d", index),
				)
				if err != nil {
					return Result{}, err
				}
				return executeWindowsStressRequestWithin(
					timeoutSupervisor,
					requestRuntime,
					CommandSpec{
						Executable: executable,
						Args:       []string{"--mode=hang"},
						Dir:        requestRuntime.Dir,
					},
					5*time.Second,
				)
			},
			validate: func(t *testing.T, result Result, err error) {
				t.Helper()
				assertWindowsRunErrorKind(t, err, ErrorTimeout)
				if result.StopAction != StopActionTerminateJob {
					t.Fatalf("timeout result=%+v", result)
				}
			},
		},
		{
			name: "exit-after-overflow",
			run: func(index int) (Result, error) {
				requestRuntime, err := overflowSupervisor.Prepare(
					fmt.Sprintf("winovr%04d", index),
				)
				if err != nil {
					return Result{}, err
				}
				return executeWindowsStressRequest(
					overflowSupervisor,
					requestRuntime,
					CommandSpec{
						Executable: executable,
						Args: []string{
							"--mode=flood-once-exit-7",
						},
						Dir: requestRuntime.Dir,
					},
				)
			},
			validate: func(t *testing.T, result Result, err error) {
				t.Helper()
				assertWindowsRunErrorKind(t, err, ErrorOutputLimit)
				if result.StopReason != StopReasonOutputOverflow ||
					len(result.Stdout) != 1024 ||
					result.StdoutTotal <= 1024 {
					t.Fatalf("overflow-race result=%+v", result)
				}
			},
		},
		{
			name: "post-resume-cancel",
			run: func(index int) (Result, error) {
				requestRuntime, err := cancellationSupervisor.Prepare(
					fmt.Sprintf("wincan%04d", index),
				)
				if err != nil {
					return Result{}, err
				}
				started := make(chan struct{})
				cancellationStarts.Store(requestRuntime.Dir, started)
				defer cancellationStarts.Delete(requestRuntime.Dir)
				outer, outerCancel := context.WithTimeout(
					context.Background(),
					windowsOuterOwnerBudget,
				)
				defer outerCancel()
				ctx, cancel := context.WithCancel(outer)
				resumeResult := make(chan error, 1)
				go func() {
					timer := time.NewTimer(windowsSchedulingWaitBudget)
					defer timer.Stop()
					select {
					case <-started:
						cancel()
						resumeResult <- nil
					case <-timer.C:
						cancel()
						resumeResult <- errors.New(
							"cancellation launch was not resumed",
						)
					case <-ctx.Done():
						resumeResult <- ctx.Err()
					}
				}()
				result, executeErr := cancellationSupervisor.Execute(
					ctx,
					requestRuntime,
					CommandSpec{
						Executable: executable,
						Args:       []string{"--mode=hang"},
						Dir:        requestRuntime.Dir,
						Stdin:      make([]byte, 2<<20),
					},
				)
				cancel()
				return result, errors.Join(executeErr, <-resumeResult)
			},
			validate: func(t *testing.T, result Result, err error) {
				t.Helper()
				assertWindowsRunErrorKind(t, err, ErrorCanceled)
				if result.StopAction != StopActionTerminateJob {
					t.Fatalf("cancel result=%+v", result)
				}
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			baseline := warmWindowsHandleStressBaseline(
				t,
				iterations,
				batchSize,
				scenario.run,
				scenario.validate,
			)
			runWindowsHandleStressBatches(
				t,
				iterations,
				batchSize,
				baseline,
				scenario.run,
				scenario.validate,
			)
		})
	}
}

type windowsStressResult struct {
	index  int
	result Result
	err    error
}

func warmWindowsHandleStressBaseline(
	t *testing.T,
	start int,
	batchSize int,
	run func(int) (Result, error),
	validate func(*testing.T, Result, error),
) uint32 {
	t.Helper()
	const maxWarmupBatches = 4
	var previous uint32
	for batch := range maxWarmupBatches {
		batchStart := start + batch*batchSize
		runWindowsHandleStressBatch(
			t,
			batchStart,
			batchStart+batchSize,
			run,
			validate,
		)
		current := stableWindowsProcessHandleCount(t)
		if batch != 0 && current == previous {
			return current
		}
		previous = current
	}
	t.Fatalf(
		"process handle count did not reach a fixed point after %d warmup batches",
		maxWarmupBatches,
	)
	return 0
}

func runWindowsHandleStressBatches(
	t *testing.T,
	iterations int,
	batchSize int,
	baseline uint32,
	run func(int) (Result, error),
	validate func(*testing.T, Result, error),
) {
	t.Helper()
	for start := 0; start < iterations; start += batchSize {
		end := min(start+batchSize, iterations)
		runWindowsHandleStressBatch(t, start, end, run, validate)
		if got := stableWindowsProcessHandleCount(t); got != baseline {
			t.Fatalf(
				"batch [%d,%d) process handle count=%d want baseline=%d",
				start,
				end,
				got,
				baseline,
			)
		}
	}
}

func runWindowsHandleStressBatch(
	t *testing.T,
	start int,
	end int,
	run func(int) (Result, error),
	validate func(*testing.T, Result, error),
) {
	t.Helper()
	outcomes := make(chan windowsStressResult, end-start)
	for index := start; index < end; index++ {
		index := index
		go func() {
			result, err := run(index)
			outcomes <- windowsStressResult{
				index:  index,
				result: result,
				err:    err,
			}
		}()
	}
	for range end - start {
		outcome := <-outcomes
		if outcome.err != nil {
			var runErr *RunError
			if !errors.As(outcome.err, &runErr) {
				t.Errorf(
					"iteration %d unexpected error=%v",
					outcome.index,
					outcome.err,
				)
				continue
			}
		}
		validate(t, outcome.result, outcome.err)
	}
}

func executeWindowsStressRequest(
	supervisor *Supervisor,
	requestRuntime Runtime,
	spec CommandSpec,
) (Result, error) {
	return executeWindowsStressRequestWithin(
		supervisor,
		requestRuntime,
		spec,
		windowsOuterOwnerBudget,
	)
}

func executeWindowsStressRequestWithin(
	supervisor *Supervisor,
	requestRuntime Runtime,
	spec CommandSpec,
	timeout time.Duration,
) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return supervisor.Execute(ctx, requestRuntime, spec)
}

func TestWindowsNestedJobWorkerEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("UNRELATED_TEST_SECRET", "must-not-propagate")
	entries := windowsNestedJobWorkerEnvironment()
	allowed := map[string]bool{
		"SPAWNGATE_WINDOWS_NESTED_JOB": false,
		"SYSTEMROOT":                   false,
		"TEMP":                         false,
		"TMP":                          false,
	}
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 {
			t.Fatalf("malformed nested worker environment entry %q", entry)
		}
		name := strings.ToUpper(entry[:separator])
		if _, ok := allowed[name]; !ok {
			t.Fatalf("unexpected nested worker environment name %q", name)
		}
		allowed[name] = true
		values[name] = entry[separator+1:]
	}
	if got := values["SPAWNGATE_WINDOWS_NESTED_JOB"]; got != "worker" {
		t.Fatalf("nested worker marker = %q, want worker", got)
	}
	for _, name := range []string{"TEMP", "TMP"} {
		if got := values[name]; got != os.TempDir() {
			t.Fatalf("nested worker %s = %q, want %q", name, got, os.TempDir())
		}
	}
	if _, err := windowsEnvironmentBlock(entries); err != nil {
		t.Fatalf("encode nested worker environment: %v", err)
	}
}

func TestWindowsSupervisorExecutesInsideNestedJob(t *testing.T) {
	switch os.Getenv("SPAWNGATE_WINDOWS_NESTED_JOB") {
	case "worker":
		runWindowsNestedJobWorker(t)
		return
	case "provider":
		_, _ = os.Stdout.WriteString("nested-ready\n")
		return
	}
	runWindowsNestedJobController(t)
}

func runWindowsNestedJobWorker(t *testing.T) {
	t.Helper()
	supervisor := newWindowsSupervisorForTest(t, windowsSupervisorTestLimits())
	requestRuntime := prepareWindowsSupervisorRuntime(t, supervisor, "winnested")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeWindowsIntegration(
		t,
		supervisor,
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args: []string{
				"-test.run=^TestWindowsSupervisorExecutesInsideNestedJob$",
			},
			Env: []string{"SPAWNGATE_WINDOWS_NESTED_JOB=provider"},
			Dir: requestRuntime.Dir,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 ||
		!strings.Contains(string(result.Stdout), "nested-ready\n") {
		t.Fatalf("nested result=%+v", result)
	}
}

func runWindowsNestedJobController(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commandLine, err := windowsCommandLine(executable, []string{
		"-test.run=^TestWindowsSupervisorExecutesInsideNestedJob$",
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := windows.UTF16FromString(executable)
	if err != nil {
		t.Fatal(err)
	}
	command, err := windows.UTF16FromString(commandLine)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := windows.UTF16FromString(filepath.Dir(executable))
	if err != nil {
		t.Fatal(err)
	}
	block, err := windowsEnvironmentBlock(windowsNestedJobWorkerEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ProcessInformation
	startup := windows.StartupInfo{
		Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})),
	}
	if err := windows.CreateProcess(
		&application[0],
		&command[0],
		nil,
		nil,
		false,
		windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT,
		&block[0],
		&directory[0],
		&startup,
		&info,
	); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = windows.TerminateProcess(info.Process, 1)
		_, _ = windows.WaitForSingleObject(info.Process, 30000)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
	}()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	if err := windows.AssignProcessToJobObject(job, info.Process); err != nil {
		t.Fatal(err)
	}
	if _, err := windows.ResumeThread(info.Thread); err != nil {
		t.Fatal(err)
	}
	event, err := windows.WaitForSingleObject(info.Process, 30000)
	if err != nil || event != windows.WAIT_OBJECT_0 {
		t.Fatalf("nested worker wait event=%#x err=%v", event, err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(info.Process, &exitCode); err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("nested worker exit=%d", exitCode)
	}
}

func windowsNestedJobWorkerEnvironment() []string {
	temp := os.TempDir()
	entries := []string{
		"SPAWNGATE_WINDOWS_NESTED_JOB=worker",
		"TEMP=" + temp,
		"TMP=" + temp,
	}
	if value, ok := os.LookupEnv("SystemRoot"); ok {
		entries = append(entries, "SystemRoot="+value)
	}
	return entries
}

func newWindowsSupervisorForTest(
	t *testing.T,
	limits Limits,
) *Supervisor {
	t.Helper()
	root := openTestRoot(t)
	supervisor, err := NewSupervisor(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			windowsOuterOwnerBudget,
		)
		defer cancel()
		if err := supervisor.Shutdown(ctx); err != nil {
			t.Errorf("shutdown supervisor: %v", err)
		}
	})
	return supervisor
}

func windowsSupervisorTestLimits() Limits {
	return Limits{
		Execution:   30 * time.Second,
		TermGrace:   50 * time.Millisecond,
		Cleanup:     30 * time.Second,
		StdoutBytes: 64 * 1024,
		StderrBytes: 64 * 1024,
	}
}

func prepareWindowsSupervisorRuntime(
	t *testing.T,
	supervisor *Supervisor,
	id string,
) Runtime {
	t.Helper()
	requestRuntime, err := supervisor.Prepare(id)
	if err != nil {
		t.Fatal(err)
	}
	return requestRuntime
}

func executeWindowsIntegration(
	t *testing.T,
	supervisor *Supervisor,
	requestRuntime Runtime,
	spec CommandSpec,
) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		windowsOuterOwnerBudget,
	)
	defer cancel()
	return supervisor.Execute(ctx, requestRuntime, spec)
}

func assertWindowsRunErrorKind(
	t *testing.T,
	err error,
	kind ErrorKind,
) {
	t.Helper()
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != kind {
		t.Fatalf("error=%T %v, want RunError kind %q", err, err, kind)
	}
}

func assertWindowsProcessGone(t *testing.T, pid uint32) {
	t.Helper()
	handle, err := windows.OpenProcess(
		windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		pid,
	)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || event != windows.WAIT_OBJECT_0 {
		t.Fatalf("process %d remains event=%#x err=%v", pid, event, err)
	}
}

func stableWindowsProcessHandleCount(t *testing.T) uint32 {
	t.Helper()
	var previous uint32
	deadline := time.Now().Add(windowsSchedulingWaitBudget)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		runtime.GC()
		current, err := windowsProcessHandleCount()
		if err != nil {
			t.Fatal(err)
		}
		if attempt != 0 && current == previous {
			return current
		}
		previous = current
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("process handle count did not become stable")
	return 0
}

func windowsProcessHandleCount() (uint32, error) {
	procedure := windows.NewLazySystemDLL("kernel32.dll").NewProc(
		"GetProcessHandleCount",
	)
	var count uint32
	result, _, callErr := procedure.Call(
		uintptr(windows.CurrentProcess()),
		uintptr(unsafe.Pointer(&count)),
	)
	if result == 0 {
		return 0, callErr
	}
	return count, nil
}
