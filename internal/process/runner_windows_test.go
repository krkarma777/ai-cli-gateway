//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func TestWindowsCommandLineEscapesEveryArgument(t *testing.T) {
	t.Parallel()

	executable := `C:\Program Files\게이트웨이\provider.exe`
	args := []string{
		"plain",
		"",
		"two words",
		`embedded"quote`,
		`C:\trailing space\`,
		`\\server\share\`,
		"한글 모델",
	}
	got, err := windowsCommandLine(executable, args)
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Program Files\게이트웨이\provider.exe"` +
		` plain "" "two words" embedded\"quote` +
		` "C:\trailing space\\" \\server\share\ "한글 모델"`
	if got != want {
		t.Fatalf("command line:\n got: %q\nwant: %q", got, want)
	}

	for _, test := range []struct {
		name       string
		executable string
		args       []string
	}{
		{name: "executable NUL", executable: "C:\\safe\x00.exe"},
		{name: "argument NUL", executable: `C:\safe.exe`, args: []string{"a\x00b"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := windowsCommandLine(test.executable, test.args); err == nil {
				t.Fatal("unsafe command line accepted")
			}
		})
	}
}

func TestWindowsEnvironmentBlockSortsAndTerminatesExactly(t *testing.T) {
	t.Parallel()

	block, err := windowsEnvironmentBlock([]string{
		"z=last",
		"PATH=C:\\safe",
		"alpha=한글",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := appendUTF16Block(
		"alpha=한글",
		"PATH=C:\\safe",
		"z=last",
	)
	if !slices.Equal(block, want) {
		t.Fatalf("block=%v want=%v", block, want)
	}
	if len(block) < 2 ||
		block[len(block)-1] != 0 ||
		block[len(block)-2] != 0 {
		t.Fatalf("environment block lacks double NUL: %v", block)
	}

	empty, err := windowsEnvironmentBlock(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(empty, []uint16{0, 0}) {
		t.Fatalf("empty block=%v", empty)
	}
}

func TestWindowsEnvironmentBlockRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
	}{
		{name: "duplicate exact", env: []string{"A=1", "A=2"}},
		{name: "duplicate case insensitive", env: []string{"Path=1", "PATH=2"}},
		{name: "embedded NUL", env: []string{"A=before\x00after"}},
		{name: "missing equals", env: []string{"A"}},
		{name: "empty name", env: []string{"=value"}},
		{name: "invalid name", env: []string{"A-B=value"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := windowsEnvironmentBlock(test.env); err == nil {
				t.Fatal("unsafe environment accepted")
			}
		})
	}
}

func TestValidateWindowsCommandShapeRejectsShellShims(t *testing.T) {
	t.Parallel()

	runtime := Runtime{Dir: `C:\runtime\request-1`}
	base := CommandSpec{
		Executable: `C:\trusted\provider.exe`,
		Dir:        runtime.Dir,
	}
	if err := validateWindowsCommandShape(runtime, base); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(CommandSpec) CommandSpec
	}{
		{
			name: "cmd lower",
			mutate: func(spec CommandSpec) CommandSpec {
				spec.Executable = `C:\trusted\provider.cmd`
				return spec
			},
		},
		{
			name: "bat upper",
			mutate: func(spec CommandSpec) CommandSpec {
				spec.Executable = `C:\trusted\PROVIDER.BAT`
				return spec
			},
		},
		{
			name: "relative executable",
			mutate: func(spec CommandSpec) CommandSpec {
				spec.Executable = `provider.exe`
				return spec
			},
		},
		{
			name: "wrong directory",
			mutate: func(spec CommandSpec) CommandSpec {
				spec.Dir = filepath.Dir(runtime.Dir)
				return spec
			},
		},
		{
			name: "argument NUL",
			mutate: func(spec CommandSpec) CommandSpec {
				spec.Args = []string{"unsafe\x00argument"}
				return spec
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateWindowsCommandShape(
				runtime,
				test.mutate(base),
			); err == nil {
				t.Fatal("unsafe command accepted")
			}
		})
	}
}

func TestValidateWindowsExecutableMarksOnlyUnavailableInspection(
	t *testing.T,
) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing-provider.exe")
	missingErr := validateWindowsExecutable(missing)
	if !errors.Is(missingErr, ErrExecutableUnavailable) ||
		!errors.Is(missingErr, os.ErrNotExist) {
		t.Fatalf("missing executable error=%T %v", missingErr, missingErr)
	}

	directory := t.TempDir()
	directoryErr := validateWindowsExecutable(directory)
	if directoryErr == nil {
		t.Fatal("directory accepted as provider executable")
	}
	if errors.Is(directoryErr, ErrExecutableUnavailable) {
		t.Fatalf("regular-file rejection acquired unavailable marker: %v", directoryErr)
	}

	executable := filepath.Join(t.TempDir(), "provider.exe")
	if err := os.WriteFile(executable, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsExecutable(executable); err != nil {
		t.Fatalf("regular file rejected: %v", err)
	}
}

func TestWindowsHandleLedgerClosesReverseOrderExactlyOnce(t *testing.T) {
	t.Parallel()

	api := newFakeWindowsAPI()
	ledger := newWindowsHandleLedger(api)
	first := ledger.acquire(101, "first")
	second := ledger.acquire(102, "second")
	third := ledger.acquire(103, "third")

	if err := second.close(); err != nil {
		t.Fatal(err)
	}
	if err := second.close(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.closeReverse(); err != nil {
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	if err := third.close(); err != nil {
		t.Fatal(err)
	}

	if got, want := api.closed, []windows.Handle{102, 103, 101}; !slices.Equal(got, want) {
		t.Fatalf("close order=%v want=%v", got, want)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestStartWindowsProcessUsesSuspendedJobOrderAndExactAllowlist(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	request := windowsStartRequestForTest(t)
	var beforeResumeView windowsLaunchView
	request.beforeResume = func(view windowsLaunchView) error {
		api.calls = append(api.calls, "BeforeResume")
		beforeResumeView = view
		return nil
	}
	process, err := startWindowsProcess(
		api,
		request,
		time.Second,
		newCompletionOwner(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.ledger.closeReverse(); err != nil {
			t.Errorf("close launch handles: %v", err)
		}
	})

	wantFlags := uint32(
		windows.CREATE_SUSPENDED |
			windows.CREATE_UNICODE_ENVIRONMENT |
			windows.EXTENDED_STARTUPINFO_PRESENT,
	)
	if api.creationFlags != wantFlags {
		t.Fatalf("creation flags=%#x want=%#x", api.creationFlags, wantFlags)
	}
	if got, want := api.allowlist, []windows.Handle{10, 13, 15}; !slices.Equal(got, want) {
		t.Fatalf("handle allowlist=%v want=%v", got, want)
	}
	if api.inheritHandles != true {
		t.Fatal("CreateProcess did not enable allowlisted inheritance")
	}
	if got, want := filteredLaunchOrder(api.calls), []string{
		"CreateProcess",
		"CreateJobObject",
		"SetInformationJobObject",
		"AssignProcessToJobObject",
		"BeforeResume",
		"ResumeThread",
	}; !slices.Equal(got, want) {
		t.Fatalf("launch order=%v want=%v\nall calls=%v", got, want, api.calls)
	}
	if beforeResumeView.process != 20 {
		t.Fatalf("before-resume process handle=%d want=20", beforeResumeView.process)
	}
	if got, want := beforeResumeView.childHandles, [3]windows.Handle{10, 13, 15}; got != want {
		t.Fatalf("before-resume child handles=%v want=%v", got, want)
	}
	if api.attributeDeletes != 1 {
		t.Fatalf("attribute deletes=%d", api.attributeDeletes)
	}
	for _, child := range []windows.Handle{10, 13, 15} {
		if api.closeCounts[child] != 1 {
			t.Fatalf("child handle %d close count=%d", child, api.closeCounts[child])
		}
	}
	if api.closeCounts[21] != 1 {
		t.Fatalf("thread handle close count=%d", api.closeCounts[21])
	}
}

func TestStartWindowsProcessBeforeResumeFailureAbortsAssignedSuspendedJob(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	hookFailure := errors.New("before-resume inspection failed")
	request := windowsStartRequestForTest(t)
	request.beforeResume = func(view windowsLaunchView) error {
		api.calls = append(api.calls, "BeforeResume")
		if view.process != 20 {
			t.Fatalf("before-resume process handle=%d want=20", view.process)
		}
		return hookFailure
	}

	process, err := startWindowsProcess(
		api,
		request,
		time.Second,
		newCompletionOwner(),
	)
	if process != nil || err == nil {
		t.Fatalf("start result=%v err=%v", process, err)
	}
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v, want cleanup RunError", err, err)
	}
	if !errors.Is(err, hookFailure) {
		t.Fatalf("error=%v, want hook cause", err)
	}
	if got := slices.Contains(api.calls, "ResumeThread"); got {
		t.Fatalf("suspended process was resumed: calls=%v", api.calls)
	}
	if api.terminateJobCalls != 1 || api.terminateProcessCalls != 0 {
		t.Fatalf(
			"TerminateJobObject=%d TerminateProcess=%d",
			api.terminateJobCalls,
			api.terminateProcessCalls,
		)
	}
	if api.waitCalls == 0 {
		t.Fatal("failed suspended process was not waited")
	}
	if got, want := filteredLaunchOrder(api.calls), []string{
		"CreateProcess",
		"CreateJobObject",
		"SetInformationJobObject",
		"AssignProcessToJobObject",
		"BeforeResume",
	}; !slices.Equal(got, want) {
		t.Fatalf("launch order=%v want=%v\nall calls=%v", got, want, api.calls)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestStartWindowsProcessFailureEdgesCloseEveryHandleExactlyOnce(
	t *testing.T,
) {
	t.Parallel()

	steps := []string{
		"CreatePipe#1",
		"SetHandleInformation#1",
		"CreatePipe#2",
		"SetHandleInformation#2",
		"CreatePipe#3",
		"SetHandleInformation#3",
		"NewProcThreadAttributeList",
		"CreateProcess",
		"CreateJobObject",
		"SetInformationJobObject",
		"AssignProcessToJobObject",
		"ResumeThread",
	}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			api := newFakeWindowsAPI()
			api.failAt = step
			process, err := startWindowsProcess(
				api,
				windowsStartRequestForTest(t),
				time.Second,
				newCompletionOwner(),
			)
			if err == nil || process != nil {
				t.Fatalf("start result=%v err=%v", process, err)
			}
			assertEveryAcquiredHandleClosedOnce(t, api)
			if api.attributeDeletes > 1 {
				t.Fatalf("attribute deletes=%d", api.attributeDeletes)
			}
			for _, poison := range []windows.Handle{0xdead, 0xbeef} {
				if api.closeCounts[poison] != 0 {
					t.Fatalf(
						"indeterminate CreatePipe handle %#x was closed",
						poison,
					)
				}
			}
			if api.processCreated {
				if step == "ResumeThread" {
					if api.terminateJobCalls != 1 {
						t.Fatalf("TerminateJobObject calls=%d", api.terminateJobCalls)
					}
				} else if api.terminateProcessCalls != 1 {
					t.Fatalf("TerminateProcess calls=%d", api.terminateProcessCalls)
				}
				if api.waitCalls == 0 {
					t.Fatal("created process was not waited")
				}
			}
		})
	}
}

func TestWindowsExecutableUnavailableMarkerHasLaunchProvenance(
	t *testing.T,
) {
	t.Parallel()

	otherFailure := errors.New("other launch failure")
	tests := []struct {
		name       string
		call       string
		cause      error
		wantMarker bool
	}{
		{
			name:  "pipe permission is not executable provenance",
			call:  "CreatePipe#1",
			cause: os.ErrPermission,
		},
		{
			name:       "CreateProcess missing executable",
			call:       "CreateProcess",
			cause:      os.ErrNotExist,
			wantMarker: true,
		},
		{
			name:       "CreateProcess permission failure",
			call:       "CreateProcess",
			cause:      os.ErrPermission,
			wantMarker: true,
		},
		{
			name:  "other CreateProcess failure",
			call:  "CreateProcess",
			cause: otherFailure,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := newFakeWindowsAPI()
			api.injectFailure(test.call, test.cause)
			process, err := startWindowsProcess(
				api,
				windowsStartRequestForTest(t),
				time.Second,
				newCompletionOwner(),
			)
			if process != nil {
				t.Fatalf("unexpected process: %+v", process)
			}
			var runErr *RunError
			if !errors.As(err, &runErr) || runErr.Kind != ErrorStart {
				t.Fatalf("error=%T %v, want start RunError", err, err)
			}
			if got := errors.Is(err, ErrExecutableUnavailable); got != test.wantMarker {
				t.Fatalf("executable-unavailable marker=%t want=%t: %v", got, test.wantMarker, err)
			}
			if !errors.Is(err, test.cause) {
				t.Fatalf("error=%T %v, want cause %v", err, err, test.cause)
			}
			assertEveryAcquiredHandleClosedOnce(t, api)
		})
	}
}

func TestStartWindowsProcessCleansPartialCreateProcessHandles(t *testing.T) {
	t.Parallel()

	api := newFakeWindowsAPI()
	api.failAt = "CreateProcess"
	api.returnPartialProcessOnFailure = true
	process, err := startWindowsProcess(
		api,
		windowsStartRequestForTest(t),
		time.Second,
		newCompletionOwner(),
	)
	if err == nil || process != nil {
		t.Fatalf("start result=%v err=%v", process, err)
	}
	if api.terminateProcessCalls != 1 || api.waitCalls == 0 {
		t.Fatalf(
			"TerminateProcess=%d Wait=%d",
			api.terminateProcessCalls,
			api.waitCalls,
		)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsLaunchCleanupAPIFailuresHaveStableResultAndFinalOwners(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name       string
		launchFail string
		apiFail    string
	}{
		{
			name:       "TerminateProcess",
			launchFail: "CreateJobObject",
			apiFail:    "TerminateProcess",
		},
		{
			name:       "TerminateJobObject",
			launchFail: "ResumeThread",
			apiFail:    "TerminateJobObject",
		},
		{
			name:       "WaitForSingleObject",
			launchFail: "CreateJobObject",
			apiFail:    "WaitForSingleObject",
		},
		{
			name:       "GetExitCodeProcess",
			launchFail: "CreateJobObject",
			apiFail:    "GetExitCodeProcess",
		},
		{
			name:       "QueryInformationJobObject",
			launchFail: "ResumeThread",
			apiFail:    "QueryInformationJobObject",
		},
		{
			name:    "child CloseHandle",
			apiFail: "CloseHandle(15)",
		},
		{
			name:    "resumed thread CloseHandle",
			apiFail: "CloseHandle(21)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := newFakeWindowsAPI()
			api.failAt = test.launchFail
			injected := errors.New("injected " + test.apiFail)
			api.injectFailure(test.apiFail, injected)

			result, runErr, supervisor := runFakeWindowsRequest(
				t,
				context.Background(),
				api,
				nil,
				runnerHooks{},
			)
			assertWindowsCleanupResult(
				t,
				result,
				runErr,
				injected,
				-1,
			)
			shutdownWindowsFailureSupervisor(t, supervisor, nil)
			assertEveryAcquiredHandleClosedOnce(t, api)
		})
	}
}

func TestWindowsLaunchTerminationOwnerRetriesSuspendedProcess(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	api.failAt = "CreateJobObject"
	terminateFailure := errors.New("injected TerminateProcess")
	terminated := make(chan struct{})
	var terminatedOnce sync.Once
	api.terminateProcessHook = func(call int) error {
		if call == 1 {
			return terminateFailure
		}
		terminatedOnce.Do(func() {
			close(terminated)
		})
		return nil
	}
	api.waitHook = func(int) (uint32, error) {
		select {
		case <-terminated:
			return windows.WAIT_OBJECT_0, nil
		default:
			return uint32(windows.WAIT_TIMEOUT), nil
		}
	}

	result, runErr, supervisor := runFakeWindowsRequest(
		t,
		context.Background(),
		api,
		nil,
		runnerHooks{},
	)
	assertWindowsCleanupResult(t, result, runErr, terminateFailure, -1)
	shutdownWindowsFailureSupervisor(t, supervisor, nil)
	if api.terminateProcessCalls < 2 {
		t.Fatalf(
			"TerminateProcess calls=%d want at least 2",
			api.terminateProcessCalls,
		)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsLaunchTerminationOwnerReapsSignaledProcessAfterAccessDenied(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	api.failAt = "CreateJobObject"
	processSignaled := make(chan struct{})
	var signalOnce sync.Once
	api.terminateProcessHook = func(call int) error {
		if call >= 2 {
			signalOnce.Do(func() {
				close(processSignaled)
			})
		}
		return windows.ERROR_ACCESS_DENIED
	}
	api.waitHook = func(int) (uint32, error) {
		select {
		case <-processSignaled:
			return windows.WAIT_OBJECT_0, nil
		default:
			return uint32(windows.WAIT_TIMEOUT), nil
		}
	}
	exitCodeCollected := make(chan struct{})
	var exitCodeOnce sync.Once
	api.getExitCodeHook = func(int) (uint32, error) {
		exitCodeOnce.Do(func() {
			close(exitCodeCollected)
		})
		return 37, nil
	}

	result, runErr, supervisor := runFakeWindowsRequest(
		t,
		context.Background(),
		api,
		nil,
		runnerHooks{},
	)
	assertWindowsCleanupResult(
		t,
		result,
		runErr,
		windows.ERROR_ACCESS_DENIED,
		-1,
	)
	shutdownWindowsFailureSupervisor(t, supervisor, nil)
	select {
	case <-exitCodeCollected:
	default:
		t.Fatal("signaled process exit code was not collected")
	}
	if api.terminateProcessCalls < 2 {
		t.Fatalf(
			"TerminateProcess calls=%d want at least 2",
			api.terminateProcessCalls,
		)
	}
	if api.waitCalls < 2 {
		t.Fatalf("WaitForSingleObject calls=%d want at least 2", api.waitCalls)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsLaunchTerminationOwnerRetainsPersistentFailure(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	api.failAt = "CreateJobObject"
	terminateFailure := errors.New("persistent TerminateProcess")
	releaseTermination := make(chan struct{})
	terminated := make(chan struct{})
	retryObserved := make(chan struct{})
	var retryOnce sync.Once
	var terminatedOnce sync.Once
	api.terminateProcessHook = func(call int) error {
		if call >= 2 {
			retryOnce.Do(func() {
				close(retryObserved)
			})
		}
		select {
		case <-releaseTermination:
			terminatedOnce.Do(func() {
				close(terminated)
			})
			return nil
		default:
			return terminateFailure
		}
	}
	api.waitHook = func(int) (uint32, error) {
		select {
		case <-terminated:
			return windows.WAIT_OBJECT_0, nil
		default:
			return uint32(windows.WAIT_TIMEOUT), nil
		}
	}

	result, runErr, supervisor := runFakeWindowsRequest(
		t,
		context.Background(),
		api,
		nil,
		runnerHooks{},
	)
	assertWindowsCleanupResult(t, result, runErr, terminateFailure, -1)
	select {
	case <-retryObserved:
	case <-time.After(time.Second):
		t.Fatal("deferred owner did not retry TerminateProcess")
	}
	assertBoundedWindowsShutdownRetainsOwner(t, supervisor, 1)
	close(releaseTermination)
	shutdownWindowsFailureSupervisor(t, supervisor, nil)
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsJobTerminationOwnerRetriesUntilAccountingIsEmpty(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	terminateFailure := errors.New("injected TerminateJobObject")
	terminated := make(chan struct{})
	var terminatedOnce sync.Once
	api.terminateJobHook = func(call int) error {
		if call == 1 {
			return terminateFailure
		}
		terminatedOnce.Do(func() {
			close(terminated)
		})
		return nil
	}
	api.accountingHook = func(int) (windowsJobAccounting, error) {
		select {
		case <-terminated:
			return windowsJobAccounting{
				TotalProcesses: 1,
			}, nil
		default:
			return windowsJobAccounting{
				TotalProcesses:  1,
				ActiveProcesses: 1,
			}, nil
		}
	}
	api.waitHook = func(int) (uint32, error) {
		select {
		case <-terminated:
			return windows.WAIT_OBJECT_0, nil
		default:
			return uint32(windows.WAIT_TIMEOUT), nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	hooks := runnerHooks{
		afterResume: func(windowsLaunchView) {
			cancel()
		},
	}

	result, runErr, supervisor := runFakeWindowsRequest(
		t,
		ctx,
		api,
		nil,
		hooks,
	)
	cancel()
	assertWindowsCleanupResult(t, result, runErr, terminateFailure, -1)
	if result.StopAction != StopActionTerminateJob {
		t.Fatalf("StopAction=%q", result.StopAction)
	}
	shutdownWindowsFailureSupervisor(t, supervisor, nil)
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsJobTerminationOwnerRetainsPersistentFailure(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	terminateFailure := errors.New("persistent TerminateJobObject")
	releaseTermination := make(chan struct{})
	terminated := make(chan struct{})
	retryObserved := make(chan struct{})
	var retryOnce sync.Once
	var terminatedOnce sync.Once
	api.terminateJobHook = func(call int) error {
		if call >= 3 {
			retryOnce.Do(func() {
				close(retryObserved)
			})
		}
		select {
		case <-releaseTermination:
			terminatedOnce.Do(func() {
				close(terminated)
			})
			return nil
		default:
			return terminateFailure
		}
	}
	api.accountingHook = func(int) (windowsJobAccounting, error) {
		select {
		case <-terminated:
			return windowsJobAccounting{
				TotalProcesses: 1,
			}, nil
		default:
			return windowsJobAccounting{
				TotalProcesses:  1,
				ActiveProcesses: 1,
			}, nil
		}
	}
	api.waitHook = func(int) (uint32, error) {
		select {
		case <-terminated:
			return windows.WAIT_OBJECT_0, nil
		default:
			return uint32(windows.WAIT_TIMEOUT), nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	hooks := runnerHooks{
		afterResume: func(windowsLaunchView) {
			cancel()
		},
	}

	result, runErr, supervisor := runFakeWindowsRequest(
		t,
		ctx,
		api,
		nil,
		hooks,
	)
	cancel()
	assertWindowsCleanupResult(t, result, runErr, terminateFailure, -1)
	if result.StopAction != StopActionTerminateJob {
		t.Fatalf("StopAction=%q", result.StopAction)
	}
	select {
	case <-retryObserved:
	case <-time.After(time.Second):
		t.Fatal("deferred owner did not retry TerminateJobObject")
	}
	assertBoundedWindowsShutdownRetainsOwner(t, supervisor, 2)
	close(releaseTermination)
	shutdownWindowsFailureSupervisor(t, supervisor, nil)
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsLateReaderOverflowReclassifiesCommittedTerminalCause(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name           string
		cancelAtResume bool
		timeout        bool
		cleanupFailure bool
		wantCommitted  StopReason
	}{
		{
			name:          "normal exit",
			wantCommitted: StopReasonNormalExit,
		},
		{
			name:           "caller cancellation",
			cancelAtResume: true,
			wantCommitted:  StopReasonCallerCancellation,
		},
		{
			name:          "timeout",
			timeout:       true,
			wantCommitted: StopReasonSupervisorTimeout,
		},
		{
			name:           "cleanup failure stays cleanup",
			cleanupFailure: true,
			wantCommitted:  StopReasonNormalExit,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			api := newFakeWindowsAPI()
			releaseRead := make(chan struct{})
			overflowRecorded := make(chan struct{})
			var overflowOnce sync.Once
			stdoutCalls := 0
			api.readFileHook = func(
				handle windows.Handle,
				_ int,
				buffer []byte,
			) (uint32, error) {
				if handle != 12 {
					return 0, windows.ERROR_BROKEN_PIPE
				}
				stdoutCalls++
				if stdoutCalls == 1 {
					<-releaseRead
					copy(buffer, "xx")
					return 2, nil
				}
				overflowOnce.Do(func() {
					close(overflowRecorded)
				})
				return 0, windows.ERROR_BROKEN_PIPE
			}
			cleanupFailure := errors.New("injected late-overflow cleanup")
			if test.cleanupFailure {
				api.injectFailure("CloseHandle(22)", cleanupFailure)
			}

			ctx := context.Background()
			var cancel context.CancelFunc
			hooks := runnerHooks{}
			if test.cancelAtResume {
				ctx, cancel = context.WithCancel(ctx)
				hooks.afterResume = func(windowsLaunchView) {
					cancel()
				}
			}
			committed := make(chan terminalState, 1)
			releaseCommit := make(chan struct{})
			hooks.afterCommit = func(state terminalState) {
				committed <- state
				<-releaseCommit
			}
			limits := windowsFailureTestLimits()
			limits.StdoutBytes = 1
			if test.timeout {
				limits.Execution = time.Nanosecond
				hooks.beforeCommit = func(events windowsTerminalEventView) {
					<-events.timeoutReady
				}
			}

			runDone := runFakeWindowsRequestAsyncWithLimits(
				t,
				ctx,
				api,
				nil,
				hooks,
				limits,
			)
			select {
			case state := <-committed:
				if state.reason != test.wantCommitted {
					t.Fatalf(
						"committed reason=%q want=%q",
						state.reason,
						test.wantCommitted,
					)
				}
			case <-time.After(time.Second):
				t.Fatal("terminal cause was not committed")
			}
			close(releaseRead)
			select {
			case <-overflowRecorded:
			case <-time.After(time.Second):
				t.Fatal("reader did not record overflow after commit")
			}
			close(releaseCommit)

			var outcome fakeWindowsRunOutcome
			select {
			case outcome = <-runDone:
			case <-time.After(time.Second):
				t.Fatal("runWindowsOwned did not return")
			}
			if cancel != nil {
				cancel()
			}
			if test.cleanupFailure {
				assertWindowsCleanupResult(
					t,
					outcome.result,
					outcome.err,
					cleanupFailure,
					0,
				)
			} else {
				var runErr *RunError
				if !errors.As(outcome.err, &runErr) ||
					runErr.Kind != ErrorOutputLimit ||
					!errors.Is(outcome.err, errOutputExceeded) {
					t.Fatalf(
						"late overflow error=%T %v",
						outcome.err,
						outcome.err,
					)
				}
				if outcome.result.StopReason != StopReasonOutputOverflow ||
					outcome.result.ExitCode != 0 ||
					outcome.result.StdoutTotal != 2 ||
					string(outcome.result.Stdout) != "x" {
					t.Fatalf("late overflow result=%+v", outcome.result)
				}
			}
			shutdownWindowsFailureSupervisor(t, outcome.supervisor, nil)
			assertEveryAcquiredHandleClosedOnce(t, api)
		})
	}
}

func TestWindowsRuntimeAPIFailuresHaveStableResultAndFinalOwners(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name         string
		apiFail      string
		stdin        []byte
		cancelAtRun  bool
		wantExitCode int
	}{
		{
			name:         "WaitForSingleObject",
			apiFail:      "WaitForSingleObject",
			wantExitCode: -1,
		},
		{
			name:         "GetExitCodeProcess",
			apiFail:      "GetExitCodeProcess",
			wantExitCode: -1,
		},
		{
			name:         "QueryInformationJobObject",
			apiFail:      "QueryInformationJobObject",
			wantExitCode: 0,
		},
		{
			name:         "ReadFile",
			apiFail:      "ReadFile",
			wantExitCode: 0,
		},
		{
			name:         "WriteFile",
			apiFail:      "WriteFile",
			stdin:        []byte("prompt"),
			wantExitCode: 0,
		},
		{
			name:         "OpenThread",
			apiFail:      "OpenThread",
			wantExitCode: 0,
		},
		{
			name:         "forced TerminateJobObject",
			apiFail:      "TerminateJobObject",
			cancelAtRun:  true,
			wantExitCode: 0,
		},
		{
			name:         "process CloseHandle",
			apiFail:      "CloseHandle(20)",
			wantExitCode: 0,
		},
		{
			name:         "parent pipe CloseHandle",
			apiFail:      "CloseHandle(11)",
			wantExitCode: 0,
		},
		{
			name:         "I/O thread CloseHandle",
			apiFail:      "CloseHandle(201)",
			wantExitCode: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := newFakeWindowsAPI()
			injected := errors.New("injected " + test.apiFail)
			api.injectFailure(test.apiFail, injected)
			ctx := context.Background()
			hooks := runnerHooks{}
			var cancel context.CancelFunc
			if test.cancelAtRun {
				ctx, cancel = context.WithCancel(ctx)
				hooks.afterResume = func(windowsLaunchView) {
					cancel()
				}
			}

			result, runErr, supervisor := runFakeWindowsRequest(
				t,
				ctx,
				api,
				test.stdin,
				hooks,
			)
			if cancel != nil {
				cancel()
			}
			assertWindowsCleanupResult(
				t,
				result,
				runErr,
				injected,
				test.wantExitCode,
			)
			shutdownWindowsFailureSupervisor(t, supervisor, nil)
			assertEveryAcquiredHandleClosedOnce(t, api)
		})
	}
}

func TestWindowsJobCloseFailureNeverReusesIndeterminateHandle(t *testing.T) {
	t.Parallel()

	api := newFakeWindowsAPI()
	closeFailure := errors.New("injected Job CloseHandle")
	api.injectFailure("CloseHandle(22)", closeFailure)
	result, runErr, supervisor := runFakeWindowsRequest(
		t,
		context.Background(),
		api,
		nil,
		runnerHooks{},
	)
	assertWindowsCleanupResult(t, result, runErr, closeFailure, 0)
	if supervisor.completions.count() != 0 {
		t.Fatalf(
			"indeterminate closed Job received deferred owner: %d",
			supervisor.completions.count(),
		)
	}
	closeIndex := slices.Index(api.calls, "CloseHandle(22)")
	if closeIndex < 0 {
		t.Fatalf("Job close not attempted: %v", api.calls)
	}
	for _, call := range api.calls[closeIndex+1:] {
		if call == "TerminateJobObject" ||
			call == "QueryInformationJobObject" {
			t.Fatalf(
				"indeterminate Job handle reused by %s: %v",
				call,
				api.calls,
			)
		}
	}
	shutdownWindowsFailureSupervisor(t, supervisor, nil)
	if api.closeCounts[22] != 1 {
		t.Fatalf("Job CloseHandle attempts=%d", api.closeCounts[22])
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsDeferredWaitFailureRemainsOwnedUntilShutdownDrains(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	waitFailure := errors.New("injected persistent wait failure")
	releaseRetry := make(chan struct{})
	api.waitHook = func(call int) (uint32, error) {
		if call == 1 {
			return windows.WAIT_FAILED, waitFailure
		}
		<-releaseRetry
		return windows.WAIT_OBJECT_0, nil
	}
	result, runErr, supervisor := runFakeWindowsRequest(
		t,
		context.Background(),
		api,
		nil,
		runnerHooks{},
	)
	assertWindowsCleanupResult(t, result, runErr, waitFailure, -1)

	shortContext, shortCancel := context.WithTimeout(
		context.Background(),
		5*time.Millisecond,
	)
	defer shortCancel()
	shutdownErr := supervisor.Shutdown(shortContext)
	var shutdownRunErr *RunError
	if !errors.As(shutdownErr, &shutdownRunErr) ||
		shutdownRunErr.Kind != ErrorCleanup ||
		!errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("bounded Shutdown error=%T %v", shutdownErr, shutdownErr)
	}
	if supervisor.completions.count() != 1 {
		t.Fatalf(
			"deferred process owner lost after bounded Shutdown: %d",
			supervisor.completions.count(),
		)
	}
	close(releaseRetry)
	shutdownWindowsFailureSupervisor(t, supervisor, nil)
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsCancelSynchronousIOFailureIsDrainedByShutdown(t *testing.T) {
	t.Parallel()

	api := newFakeWindowsAPI()
	cancelFailure := errors.New("injected CancelSynchronousIo")
	api.injectFailure("CancelSynchronousIo", cancelFailure)
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	api.writeFileHook = func(call int, _ []byte) (uint32, error) {
		if call != 1 {
			return 0, io.ErrShortWrite
		}
		close(writeEntered)
		<-releaseWrite
		return 1, nil
	}
	api.cancelIOHook = func(call int) error {
		if call == 2 {
			close(releaseWrite)
		}
		return nil
	}
	ledger := newWindowsHandleLedger(api)
	const pipeHandle = windows.Handle(95)
	api.acquired = append(api.acquired, pipeHandle)
	pipe := ledger.acquire(pipeHandle, "parent stdin")
	root := openTestRoot(t)
	supervisor, err := NewSupervisor(root, windowsFailureTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	process := &windowsProcess{
		api:         api,
		ledger:      ledger,
		completions: supervisor.completions,
		pid:         96,
		stdinParent: pipe,
	}
	started := make(chan windowsIOWorker, 1)
	writerDone := make(chan error, 1)
	go writeWindowsStdin(
		api,
		ledger,
		pipe,
		[]byte("blocked"),
		started,
		writerDone,
	)
	worker := <-started
	<-writeEntered
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
	shutdownWindowsFailureSupervisor(t, supervisor, cancelFailure)
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsCancelSynchronousIOFailureHasStableOuterContract(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	cancelFailure := errors.New("injected CancelSynchronousIo")
	api.injectFailure("CancelSynchronousIo", cancelFailure)
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	api.writeFileHook = func(call int, data []byte) (uint32, error) {
		if call != 1 {
			return 0, fmt.Errorf("unexpected WriteFile call %d", call)
		}
		close(writeEntered)
		<-releaseWrite
		return uint32(len(data)), nil
	}

	runDone := runFakeWindowsRequestAsync(
		t,
		context.Background(),
		api,
		[]byte("blocked"),
		runnerHooks{},
	)
	select {
	case <-writeEntered:
	case <-time.After(time.Second):
		t.Fatal("stdin worker did not enter the blocked WriteFile")
	}

	var outcome fakeWindowsRunOutcome
	select {
	case outcome = <-runDone:
	case <-time.After(time.Second):
		t.Fatal("runWindowsOwned did not hand off the blocked worker")
	}
	assertWindowsCleanupResult(
		t,
		outcome.result,
		outcome.err,
		context.DeadlineExceeded,
		0,
	)
	if outcome.supervisor.completions.count() != 1 {
		t.Fatalf(
			"blocked worker deferred owners=%d, want 1",
			outcome.supervisor.completions.count(),
		)
	}

	shortContext, shortCancel := context.WithTimeout(
		context.Background(),
		5*time.Millisecond,
	)
	shutdownErr := outcome.supervisor.Shutdown(shortContext)
	shortCancel()
	var shutdownRunErr *RunError
	if !errors.As(shutdownErr, &shutdownRunErr) ||
		shutdownRunErr.Kind != ErrorCleanup ||
		!errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("bounded Shutdown error=%T %v", shutdownErr, shutdownErr)
	}
	if outcome.supervisor.completions.count() != 1 {
		t.Fatalf(
			"blocked worker owner lost after bounded Shutdown: %d",
			outcome.supervisor.completions.count(),
		)
	}

	close(releaseWrite)
	shutdownWindowsFailureSupervisor(
		t,
		outcome.supervisor,
		cancelFailure,
	)
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestStartWindowsProcessRechecksCancellationImmediatelyBeforeCreate(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	request := windowsStartRequestForTest(t)
	request.prelaunch = func() error {
		return context.Canceled
	}
	process, err := startWindowsProcess(
		api,
		request,
		time.Second,
		newCompletionOwner(),
	)
	if process != nil {
		t.Fatalf("unexpected process: %+v", process)
	}
	var runErr *RunError
	if !errors.As(err, &runErr) ||
		runErr.Kind != ErrorCanceled ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("error=%T %v, want canceled RunError", err, err)
	}
	if slices.Contains(api.calls, "CreateProcess") {
		t.Fatalf("CreateProcess called after cancellation: %v", api.calls)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsIOCancellationRetriesNotFoundUntilWorkerFinishes(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	observed := make(chan int, 8)
	api.cancelIOHook = func(call int) error {
		if call <= 2 {
			observed <- call
		}
		return windows.ERROR_NOT_FOUND
	}
	ledger := newWindowsHandleLedger(api)
	const threadHandle = windows.Handle(91)
	api.acquired = append(api.acquired, threadHandle)
	thread := ledger.acquire(threadHandle, "I/O thread")
	cancellation := newWindowsIOCancellation()

	cancellation.request(api, thread)
	for want := 1; want <= 2; want++ {
		select {
		case got := <-observed:
			if got != want {
				t.Fatalf("cancellation attempt=%d want=%d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("cancellation attempt %d was not retried", want)
		}
	}
	if !cancellation.requested() {
		t.Fatal("cancellation request was not published to the I/O worker")
	}
	if err := cancellation.finish(); err != nil {
		t.Fatalf("benign ERROR_NOT_FOUND became cleanup error: %v", err)
	}
	if err := thread.close(); err != nil {
		t.Fatal(err)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsStdinWorkerStopsBeforeNextWriteAfterCancellation(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	api.writeFileHook = func(call int, _ []byte) (uint32, error) {
		if call != 1 {
			t.Errorf("unexpected write call %d", call)
			return 0, io.ErrShortWrite
		}
		close(writeEntered)
		<-releaseWrite
		return 1, nil
	}
	api.cancelIOHook = func(call int) error {
		if call == 1 {
			close(releaseWrite)
		}
		return nil
	}
	ledger := newWindowsHandleLedger(api)
	const pipeHandle = windows.Handle(92)
	api.acquired = append(api.acquired, pipeHandle)
	pipe := ledger.acquire(pipeHandle, "parent stdin")
	started := make(chan windowsIOWorker, 1)
	done := make(chan error, 1)
	go writeWindowsStdin(
		api,
		ledger,
		pipe,
		[]byte("more-than-one-byte"),
		started,
		done,
	)
	worker := <-started
	select {
	case <-writeEntered:
	case <-time.After(time.Second):
		t.Fatal("stdin worker did not enter its first write")
	}
	worker.cancellation.request(api, worker.thread)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stdin worker did not finish after cancellation")
	}
	if api.writeCalls != 1 {
		t.Fatalf("write calls=%d, want one", api.writeCalls)
	}
	if api.cancelIOCalls == 0 {
		t.Fatal("CancelSynchronousIo was not attempted")
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestDeferredWindowsIOOwnerKeepsCancellationRetriesAlive(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	api.writeFileHook = func(call int, _ []byte) (uint32, error) {
		if call != 1 {
			t.Errorf("unexpected write call %d", call)
			return 0, io.ErrShortWrite
		}
		close(writeEntered)
		<-releaseWrite
		return 1, nil
	}
	api.cancelIOHook = func(call int) error {
		if call == 1 {
			return windows.ERROR_NOT_FOUND
		}
		if call == 2 {
			close(releaseWrite)
		}
		return nil
	}
	ledger := newWindowsHandleLedger(api)
	const pipeHandle = windows.Handle(93)
	api.acquired = append(api.acquired, pipeHandle)
	pipe := ledger.acquire(pipeHandle, "parent stdin")
	completions := newCompletionOwner()
	process := &windowsProcess{
		api:         api,
		ledger:      ledger,
		completions: completions,
		pid:         94,
		stdinParent: pipe,
	}
	started := make(chan windowsIOWorker, 1)
	writerDone := make(chan error, 1)
	go writeWindowsStdin(
		api,
		ledger,
		pipe,
		[]byte("blocked"),
		started,
		writerDone,
	)
	worker := <-started
	select {
	case <-writeEntered:
	case <-time.After(time.Second):
		t.Fatal("stdin worker did not block")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := joinWindowsCompletions(
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
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("join error=%v", err)
	}
	if completions.count() != 1 {
		t.Fatalf("deferred completions=%d", completions.count())
	}
	drainContext, drainCancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer drainCancel()
	if err := completions.drain(drainContext); err != nil {
		t.Fatal(err)
	}
	if api.cancelIOCalls < 2 {
		t.Fatalf("CancelSynchronousIo calls=%d, want retry", api.cancelIOCalls)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestFinishWindowsJobTerminatesDescendantsBeforeZeroActiveClose(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	api.accounting = []windowsJobAccounting{
		{},
		{TotalProcesses: 2, ActiveProcesses: 1},
		{TotalProcesses: 2, ActiveProcesses: 0},
	}
	ledger := newWindowsHandleLedger(api)
	const jobHandle = windows.Handle(44)
	api.acquired = append(api.acquired, jobHandle)
	process := &windowsProcess{
		api:    api,
		ledger: ledger,
		job:    ledger.acquire(jobHandle, "provider Job"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	terminated, err := finishWindowsJob(ctx, process, true)
	if err != nil {
		t.Fatal(err)
	}
	if !terminated || api.terminateJobCalls != 1 {
		t.Fatalf(
			"terminated=%t TerminateJobObject calls=%d",
			terminated,
			api.terminateJobCalls,
		)
	}
	if api.closeCounts[jobHandle] != 1 {
		t.Fatalf("Job close count=%d", api.closeCounts[jobHandle])
	}
	lastQuery := -1
	closeIndex := -1
	for index, call := range api.calls {
		if call == "QueryInformationJobObject" {
			lastQuery = index
		}
		if call == fmt.Sprintf("CloseHandle(%d)", jobHandle) {
			closeIndex = index
		}
	}
	if lastQuery < 0 || closeIndex <= lastQuery {
		t.Fatalf("Job closed before zero-active query: %v", api.calls)
	}
}

func TestWaitWindowsJobEmptyRequiresObservedProcessHistory(t *testing.T) {
	t.Parallel()

	api := newFakeWindowsAPI()
	api.accounting = []windowsJobAccounting{
		{},
		{TotalProcesses: 1, ActiveProcesses: 0},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := waitWindowsJobEmpty(ctx, api, 55); err != nil {
		t.Fatal(err)
	}
	if api.accountingCalls != 2 {
		t.Fatalf("accounting calls=%d", api.accountingCalls)
	}
}

func TestFinishWindowsJobCleanupDeadlineRetainsActiveJob(t *testing.T) {
	t.Parallel()

	api := newFakeWindowsAPI()
	api.accounting = []windowsJobAccounting{{
		TotalProcesses:  1,
		ActiveProcesses: 1,
	}}
	ledger := newWindowsHandleLedger(api)
	const jobHandle = windows.Handle(66)
	api.acquired = append(api.acquired, jobHandle)
	process := &windowsProcess{
		api:    api,
		ledger: ledger,
		job:    ledger.acquire(jobHandle, "provider Job"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	terminated, err := finishWindowsJob(ctx, process, true)
	if err == nil || !terminated {
		t.Fatalf("terminated=%t err=%v", terminated, err)
	}
	if api.closeCounts[jobHandle] != 0 {
		t.Fatal("active Job was closed without zero-active verification")
	}
	if err := process.job.close(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupDeadlineTransfersUnseenPipeCompletionsToShutdownOwner(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	api.acquired = append(api.acquired, 71, 72, 73)
	ledger := newWindowsHandleLedger(api)
	completions := newCompletionOwner()
	process := &windowsProcess{
		api:          api,
		ledger:       ledger,
		completions:  completions,
		pid:          77,
		stdinParent:  ledger.acquire(71, "parent stdin"),
		stdoutParent: ledger.acquire(72, "parent stdout"),
		stderrParent: ledger.acquire(73, "parent stderr"),
	}
	writerDone := make(chan error, 1)
	stdoutDone := make(chan readerResult, 1)
	stderrDone := make(chan readerResult, 1)
	rootDone := make(chan waitResult, 1)
	rootReady := make(chan struct{})
	force := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	state, err := joinWindowsCompletions(
		ctx,
		force,
		process,
		windowsIOWorkers{},
		windowsRootWait{done: rootDone, ready: rootReady},
		writerDone,
		stdoutDone,
		stderrDone,
		windowsCompletionState{
			wait:     waitResult{exitCode: 0},
			waitSeen: true,
		},
	)
	if err == nil || state.writerSeen || state.stdoutSeen || state.stderrSeen {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if completions.count() != 1 {
		t.Fatalf("deferred completions=%d", completions.count())
	}

	writeFailure := errors.New("injected stdin write failure")
	if err := process.stdinParent.close(); err != nil {
		t.Fatal(err)
	}
	if err := process.stdoutParent.close(); err != nil {
		t.Fatal(err)
	}
	if err := process.stderrParent.close(); err != nil {
		t.Fatal(err)
	}
	writerDone <- writeFailure
	stdoutDone <- readerResult{stream: "stdout"}
	stderrDone <- readerResult{stream: "stderr"}
	drainContext, drainCancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer drainCancel()
	if err := completions.drain(drainContext); !errors.Is(err, writeFailure) {
		t.Fatalf("deferred drain error=%v", err)
	}
	if completions.count() != 0 {
		t.Fatalf("deferred completions after drain=%d", completions.count())
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsRootWaitErrorAvailableAtDeadlineGetsRetryOwner(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	ledger := newWindowsHandleLedger(api)
	completions := newCompletionOwner()
	const processHandle = windows.Handle(74)
	api.acquired = append(api.acquired, processHandle)
	process := &windowsProcess{
		api:         api,
		ledger:      ledger,
		completions: completions,
		pid:         75,
		process:     ledger.acquire(processHandle, "provider process"),
	}
	waitFailure := errors.New("injected root wait failure")
	rootDone := make(chan waitResult, 1)
	rootDone <- waitResult{err: waitFailure, exitCode: -1}

	state := settleWindowsRootWaitAtDeadline(
		process,
		rootDone,
		windowsCompletionState{},
	)
	if !state.waitSeen ||
		!errors.Is(errors.Join(state.errs...), waitFailure) {
		t.Fatalf("state=%+v", state)
	}
	drainContext, drainCancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer drainCancel()
	if err := completions.drain(drainContext); err != nil {
		t.Fatal(err)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsRootWaitErrorAfterDeadlineHandoffGetsRetryOwner(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	ledger := newWindowsHandleLedger(api)
	completions := newCompletionOwner()
	const processHandle = windows.Handle(76)
	api.acquired = append(api.acquired, processHandle)
	process := &windowsProcess{
		api:         api,
		ledger:      ledger,
		completions: completions,
		pid:         77,
		process:     ledger.acquire(processHandle, "provider process"),
	}
	rootDone := make(chan waitResult, 1)
	state := settleWindowsRootWaitAtDeadline(
		process,
		rootDone,
		windowsCompletionState{},
	)
	if state.waitSeen {
		t.Fatalf("pending wait was marked seen: %+v", state)
	}
	if completions.count() != 1 {
		t.Fatalf("deferred completions=%d", completions.count())
	}
	waitFailure := errors.New("injected deferred root wait failure")
	rootDone <- waitResult{err: waitFailure, exitCode: -1}
	drainContext, drainCancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer drainCancel()
	if err := completions.drain(drainContext); !errors.Is(err, waitFailure) {
		t.Fatalf("deferred drain error=%v", err)
	}
	assertEveryAcquiredHandleClosedOnce(t, api)
}

func TestWindowsCompletionJoinReportsUnexpectedStdinWriteFailure(
	t *testing.T,
) {
	t.Parallel()

	api := newFakeWindowsAPI()
	ledger := newWindowsHandleLedger(api)
	process := &windowsProcess{
		api:          api,
		ledger:       ledger,
		completions:  newCompletionOwner(),
		stdinParent:  ledger.acquire(81, "parent stdin"),
		stdoutParent: ledger.acquire(82, "parent stdout"),
		stderrParent: ledger.acquire(83, "parent stderr"),
	}
	writeFailure := errors.New("injected stdin write failure")
	writerDone := make(chan error, 1)
	writerDone <- writeFailure
	stdoutDone := make(chan readerResult, 1)
	stdoutDone <- readerResult{stream: "stdout"}
	stderrDone := make(chan readerResult, 1)
	stderrDone <- readerResult{stream: "stderr"}
	force := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := joinWindowsCompletions(
		ctx,
		force,
		process,
		windowsIOWorkers{},
		windowsRootWait{},
		writerDone,
		stdoutDone,
		stderrDone,
		windowsCompletionState{
			wait:     waitResult{exitCode: 0},
			waitSeen: true,
		},
	)
	if !errors.Is(err, writeFailure) {
		t.Fatalf("join error=%v", err)
	}
	if err := ledger.closeReverse(); err != nil {
		t.Fatal(err)
	}
}

type fakeWindowsRunOutcome struct {
	result     Result
	err        error
	supervisor *Supervisor
}

func runFakeWindowsRequestAsync(
	t *testing.T,
	ctx context.Context,
	api *fakeWindowsAPI,
	stdin []byte,
	hooks runnerHooks,
) <-chan fakeWindowsRunOutcome {
	t.Helper()
	return runFakeWindowsRequestAsyncWithLimits(
		t,
		ctx,
		api,
		stdin,
		hooks,
		windowsFailureTestLimits(),
	)
}

func runFakeWindowsRequestAsyncWithLimits(
	t *testing.T,
	ctx context.Context,
	api *fakeWindowsAPI,
	stdin []byte,
	hooks runnerHooks,
	limits Limits,
) <-chan fakeWindowsRunOutcome {
	t.Helper()
	done := make(chan fakeWindowsRunOutcome, 1)
	go func() {
		result, runErr, supervisor := runFakeWindowsRequestWithLimits(
			t,
			ctx,
			api,
			stdin,
			hooks,
			limits,
		)
		done <- fakeWindowsRunOutcome{
			result:     result,
			err:        runErr,
			supervisor: supervisor,
		}
	}()
	return done
}

func runFakeWindowsRequest(
	t *testing.T,
	ctx context.Context,
	api *fakeWindowsAPI,
	stdin []byte,
	hooks runnerHooks,
) (Result, error, *Supervisor) {
	t.Helper()
	return runFakeWindowsRequestWithLimits(
		t,
		ctx,
		api,
		stdin,
		hooks,
		windowsFailureTestLimits(),
	)
}

func runFakeWindowsRequestWithLimits(
	t *testing.T,
	ctx context.Context,
	api *fakeWindowsAPI,
	stdin []byte,
	hooks runnerHooks,
	limits Limits,
) (Result, error, *Supervisor) {
	t.Helper()
	root := openTestRoot(t)
	supervisor, err := NewSupervisor(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	requestRuntime, err := root.Prepare("fix-round-1")
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(requestRuntime.Dir, "provider.exe")
	if err := os.WriteFile(executable, []byte("fake executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	owned, runErr := runWindowsOwned(
		ctx,
		root,
		requestRuntime,
		CommandSpec{
			Executable: executable,
			Args:       []string{"--fixed"},
			Dir:        requestRuntime.Dir,
			Stdin:      stdin,
		},
		limits,
		supervisor.completions,
		hooks,
		api,
	)
	cleanupContext, cleanupCancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cleanupCancel()
	if err := root.Cleanup(cleanupContext, requestRuntime); err != nil {
		t.Fatal(err)
	}
	return owned.result, runErr, supervisor
}

func windowsFailureTestLimits() Limits {
	return Limits{
		Execution:   time.Second,
		TermGrace:   10 * time.Millisecond,
		Cleanup:     200 * time.Millisecond,
		StdoutBytes: 64 * 1024,
		StderrBytes: 64 * 1024,
	}
}

func assertWindowsCleanupResult(
	t *testing.T,
	result Result,
	err error,
	cause error,
	wantExitCode int,
) {
	t.Helper()
	var runErr *RunError
	if !errors.As(err, &runErr) ||
		runErr.Kind != ErrorCleanup ||
		!errors.Is(err, cause) {
		t.Fatalf("error=%T %v, want cleanup containing %v", err, err, cause)
	}
	if result.StopReason != StopReasonCleanupFailure ||
		result.ExitCode != wantExitCode {
		t.Fatalf(
			"result=%+v, want cleanup stop and exit %d",
			result,
			wantExitCode,
		)
	}
}

func shutdownWindowsFailureSupervisor(
	t *testing.T,
	supervisor *Supervisor,
	wantErr error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := supervisor.Shutdown(ctx)
	if wantErr == nil && err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if wantErr != nil && !errors.Is(err, wantErr) {
		t.Fatalf("Shutdown error=%v want containing %v", err, wantErr)
	}
	if supervisor.completions.count() != 0 {
		t.Fatalf(
			"Shutdown left deferred owners=%d",
			supervisor.completions.count(),
		)
	}
}

func assertBoundedWindowsShutdownRetainsOwner(
	t *testing.T,
	supervisor *Supervisor,
	wantOwners int,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Millisecond,
	)
	defer cancel()
	err := supervisor.Shutdown(ctx)
	var runErr *RunError
	if !errors.As(err, &runErr) ||
		runErr.Kind != ErrorCleanup ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Shutdown error=%T %v", err, err)
	}
	if supervisor.completions.count() != wantOwners {
		t.Fatalf(
			"bounded Shutdown retained owners=%d want=%d",
			supervisor.completions.count(),
			wantOwners,
		)
	}
}

func appendUTF16Block(entries ...string) []uint16 {
	if len(entries) == 0 {
		return []uint16{0, 0}
	}
	var block []uint16
	for _, entry := range entries {
		block = append(block, utf16.Encode([]rune(entry))...)
		block = append(block, 0)
	}
	return append(block, 0)
}

func windowsStartRequestForTest(t *testing.T) windowsStartRequest {
	t.Helper()
	encode := func(value string) []uint16 {
		encoded, err := windows.UTF16FromString(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	return windowsStartRequest{
		applicationName:  encode(`C:\trusted\provider.exe`),
		commandLine:      encode(`C:\trusted\provider.exe --fixed`),
		environment:      []uint16{0, 0},
		currentDirectory: encode(`C:\runtime\request-1`),
	}
}

func filteredLaunchOrder(calls []string) []string {
	var filtered []string
	for _, call := range calls {
		switch call {
		case "CreateProcess",
			"CreateJobObject",
			"SetInformationJobObject",
			"AssignProcessToJobObject",
			"BeforeResume",
			"ResumeThread":
			filtered = append(filtered, call)
		}
	}
	return filtered
}

func assertEveryAcquiredHandleClosedOnce(
	t *testing.T,
	api *fakeWindowsAPI,
) {
	t.Helper()
	for _, handle := range api.acquired {
		if got := api.closeCounts[handle]; got != 1 {
			t.Errorf("handle %d close count=%d calls=%v", handle, got, api.calls)
		}
	}
}

type fakeWindowsHandleList struct {
	api *fakeWindowsAPI
}

func (l *fakeWindowsHandleList) list() *windows.ProcThreadAttributeList {
	return nil
}

func (l *fakeWindowsHandleList) delete() {
	l.api.attributeDeletes++
	l.api.calls = append(l.api.calls, "DeleteProcThreadAttributeList")
}

type fakeWindowsAPI struct {
	runtimeMu                     sync.Mutex
	failureMu                     sync.Mutex
	failures                      map[string][]error
	failAt                        string
	pipeCalls                     int
	clearCalls                    int
	processCreated                bool
	returnPartialProcessOnFailure bool
	creationFlags                 uint32
	inheritHandles                bool
	allowlist                     []windows.Handle
	acquired                      []windows.Handle
	closed                        []windows.Handle
	closeCounts                   map[windows.Handle]int
	calls                         []string
	attributeDeletes              int
	terminateJobCalls             int
	terminateJobHook              func(int) error
	terminateProcessCalls         int
	terminateProcessHook          func(int) error
	waitCalls                     int
	waitHook                      func(int) (uint32, error)
	getExitCodeCalls              int
	getExitCodeHook               func(int) (uint32, error)
	accounting                    []windowsJobAccounting
	accountingCalls               int
	accountingHook                func(int) (windowsJobAccounting, error)
	readCalls                     int
	readFileHook                  func(windows.Handle, int, []byte) (uint32, error)
	threadCalls                   int
	cancelIOCalls                 int
	cancelIOHook                  func(int) error
	writeCalls                    int
	writeFileHook                 func(int, []byte) (uint32, error)
}

func newFakeWindowsAPI() *fakeWindowsAPI {
	return &fakeWindowsAPI{
		closeCounts: make(map[windows.Handle]int),
		failures:    make(map[string][]error),
	}
}

func (a *fakeWindowsAPI) injectFailure(call string, failures ...error) {
	a.failureMu.Lock()
	defer a.failureMu.Unlock()
	a.failures[call] = append(a.failures[call], failures...)
}

func (a *fakeWindowsAPI) takeFailure(call string) error {
	a.failureMu.Lock()
	defer a.failureMu.Unlock()
	failures := a.failures[call]
	if len(failures) == 0 {
		return nil
	}
	failure := failures[0]
	if len(failures) == 1 {
		delete(a.failures, call)
	} else {
		a.failures[call] = failures[1:]
	}
	return failure
}

func (a *fakeWindowsAPI) createPipe() (
	windows.Handle,
	windows.Handle,
	error,
) {
	a.pipeCalls++
	call := fmt.Sprintf("CreatePipe#%d", a.pipeCalls)
	a.calls = append(a.calls, call)
	if err := a.takeFailure(call); err != nil {
		return 0, 0, err
	}
	if a.failAt == call {
		return 0xdead, 0xbeef, errors.New("injected " + call)
	}
	pairs := [][2]windows.Handle{
		{10, 11},
		{12, 13},
		{14, 15},
	}
	pair := pairs[a.pipeCalls-1]
	a.acquired = append(a.acquired, pair[0], pair[1])
	return pair[0], pair[1], nil
}

func (a *fakeWindowsAPI) clearHandleInheritance(
	_ windows.Handle,
) error {
	a.clearCalls++
	call := fmt.Sprintf("SetHandleInformation#%d", a.clearCalls)
	a.calls = append(a.calls, call)
	if a.failAt == call {
		return errors.New("injected " + call)
	}
	return nil
}

func (a *fakeWindowsAPI) newHandleList(
	handles []windows.Handle,
) (windowsHandleList, error) {
	const call = "NewProcThreadAttributeList"
	a.calls = append(a.calls, call)
	if a.failAt == call {
		return nil, errors.New("injected " + call)
	}
	a.allowlist = slices.Clone(handles)
	return &fakeWindowsHandleList{api: a}, nil
}

func (a *fakeWindowsAPI) createProcess(
	request windowsCreateProcessRequest,
) (windows.ProcessInformation, error) {
	const call = "CreateProcess"
	a.calls = append(a.calls, call)
	a.creationFlags = request.creationFlags
	a.inheritHandles = request.inheritHandles
	process := windows.ProcessInformation{
		Process:   20,
		Thread:    21,
		ProcessId: 100,
		ThreadId:  101,
	}
	if err := a.takeFailure(call); err != nil {
		return windows.ProcessInformation{}, err
	}
	if a.failAt == call {
		if a.returnPartialProcessOnFailure {
			a.acquired = append(a.acquired, process.Process, process.Thread)
			return process, errors.New("injected " + call)
		}
		return windows.ProcessInformation{}, errors.New("injected " + call)
	}
	a.processCreated = true
	a.acquired = append(a.acquired, process.Process, process.Thread)
	return process, nil
}

func (a *fakeWindowsAPI) createJobObject() (windows.Handle, error) {
	const call = "CreateJobObject"
	a.calls = append(a.calls, call)
	if a.failAt == call {
		return 0, errors.New("injected " + call)
	}
	const handle = windows.Handle(22)
	a.acquired = append(a.acquired, handle)
	return handle, nil
}

func (a *fakeWindowsAPI) setJobKillOnClose(
	_ windows.Handle,
) error {
	const call = "SetInformationJobObject"
	a.calls = append(a.calls, call)
	if a.failAt == call {
		return errors.New("injected " + call)
	}
	return nil
}

func (a *fakeWindowsAPI) assignProcessToJobObject(
	_, _ windows.Handle,
) error {
	const call = "AssignProcessToJobObject"
	a.calls = append(a.calls, call)
	if a.failAt == call {
		return errors.New("injected " + call)
	}
	return nil
}

func (a *fakeWindowsAPI) resumeThread(
	_ windows.Handle,
) (uint32, error) {
	const call = "ResumeThread"
	a.calls = append(a.calls, call)
	if a.failAt == call {
		return ^uint32(0), errors.New("injected " + call)
	}
	return 1, nil
}

func (a *fakeWindowsAPI) terminateJobObject(
	_ windows.Handle,
	_ uint32,
) error {
	a.runtimeMu.Lock()
	a.terminateJobCalls++
	call := a.terminateJobCalls
	a.calls = append(a.calls, "TerminateJobObject")
	hook := a.terminateJobHook
	a.runtimeMu.Unlock()
	if err := a.takeFailure("TerminateJobObject"); err != nil {
		return err
	}
	if hook != nil {
		return hook(call)
	}
	return nil
}

func (a *fakeWindowsAPI) terminateProcess(
	_ windows.Handle,
	_ uint32,
) error {
	a.runtimeMu.Lock()
	a.terminateProcessCalls++
	call := a.terminateProcessCalls
	a.calls = append(a.calls, "TerminateProcess")
	hook := a.terminateProcessHook
	a.runtimeMu.Unlock()
	if err := a.takeFailure("TerminateProcess"); err != nil {
		return err
	}
	if hook != nil {
		return hook(call)
	}
	return nil
}

func (a *fakeWindowsAPI) waitForSingleObject(
	_ windows.Handle,
	_ uint32,
) (uint32, error) {
	a.runtimeMu.Lock()
	a.waitCalls++
	call := a.waitCalls
	a.calls = append(a.calls, "WaitForSingleObject")
	hook := a.waitHook
	a.runtimeMu.Unlock()
	if err := a.takeFailure("WaitForSingleObject"); err != nil {
		return windows.WAIT_FAILED, err
	}
	if hook != nil {
		return hook(call)
	}
	return windows.WAIT_OBJECT_0, nil
}

func (a *fakeWindowsAPI) getExitCodeProcess(
	_ windows.Handle,
) (uint32, error) {
	a.runtimeMu.Lock()
	a.calls = append(a.calls, "GetExitCodeProcess")
	a.getExitCodeCalls++
	call := a.getExitCodeCalls
	hook := a.getExitCodeHook
	a.runtimeMu.Unlock()
	if err := a.takeFailure("GetExitCodeProcess"); err != nil {
		return 0, err
	}
	if hook != nil {
		return hook(call)
	}
	return 0, nil
}

func (a *fakeWindowsAPI) queryJobAccounting(
	_ windows.Handle,
) (windowsJobAccounting, error) {
	a.runtimeMu.Lock()
	a.calls = append(a.calls, "QueryInformationJobObject")
	a.accountingCalls++
	call := a.accountingCalls
	accounting := slices.Clone(a.accounting)
	hook := a.accountingHook
	a.runtimeMu.Unlock()
	if err := a.takeFailure("QueryInformationJobObject"); err != nil {
		return windowsJobAccounting{}, err
	}
	if hook != nil {
		return hook(call)
	}
	if len(accounting) != 0 {
		index := min(call-1, len(accounting)-1)
		return accounting[index], nil
	}
	return windowsJobAccounting{TotalProcesses: 1}, nil
}

func (a *fakeWindowsAPI) readFile(
	handle windows.Handle,
	buffer []byte,
) (uint32, error) {
	a.runtimeMu.Lock()
	a.readCalls++
	call := a.readCalls
	a.calls = append(a.calls, "ReadFile")
	hook := a.readFileHook
	a.runtimeMu.Unlock()
	if err := a.takeFailure("ReadFile"); err != nil {
		return 0, err
	}
	if hook != nil {
		return hook(handle, call, buffer)
	}
	return 0, windows.ERROR_BROKEN_PIPE
}

func (a *fakeWindowsAPI) writeFile(
	_ windows.Handle,
	data []byte,
) (uint32, error) {
	a.runtimeMu.Lock()
	a.writeCalls++
	call := a.writeCalls
	a.calls = append(a.calls, "WriteFile")
	hook := a.writeFileHook
	a.runtimeMu.Unlock()
	if err := a.takeFailure("WriteFile"); err != nil {
		return 0, err
	}
	if hook != nil {
		return hook(call, data)
	}
	return uint32(len(data)), nil
}

func (a *fakeWindowsAPI) openCurrentThread() (windows.Handle, error) {
	a.runtimeMu.Lock()
	a.threadCalls++
	call := a.threadCalls
	a.calls = append(a.calls, "OpenThread")
	a.runtimeMu.Unlock()
	if err := a.takeFailure("OpenThread"); err != nil {
		return 0, err
	}
	handle := windows.Handle(200 + call)
	a.runtimeMu.Lock()
	a.acquired = append(a.acquired, handle)
	a.runtimeMu.Unlock()
	return handle, nil
}

func (a *fakeWindowsAPI) cancelSynchronousIO(
	_ windows.Handle,
) error {
	a.runtimeMu.Lock()
	a.cancelIOCalls++
	call := a.cancelIOCalls
	a.calls = append(a.calls, "CancelSynchronousIo")
	hook := a.cancelIOHook
	a.runtimeMu.Unlock()
	if err := a.takeFailure("CancelSynchronousIo"); err != nil {
		return err
	}
	if hook != nil {
		return hook(call)
	}
	return nil
}

func (a *fakeWindowsAPI) closeHandle(handle windows.Handle) error {
	a.runtimeMu.Lock()
	a.closed = append(a.closed, handle)
	a.closeCounts[handle]++
	a.calls = append(a.calls, fmt.Sprintf("CloseHandle(%d)", handle))
	a.runtimeMu.Unlock()
	if err := a.takeFailure(fmt.Sprintf("CloseHandle(%d)", handle)); err != nil {
		return err
	}
	return nil
}
