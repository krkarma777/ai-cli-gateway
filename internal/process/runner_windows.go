//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	errExecutionTimedOut         = errors.New("provider execution timed out")
	errOutputExceeded            = errors.New("provider output limit exceeded")
	cancelSynchronousIOProcedure = windows.NewLazySystemDLL(
		"kernel32.dll",
	).NewProc("CancelSynchronousIo")
)

const windowsTerminateExitCode = uint32(1)

type terminalObservation struct {
	overflowed bool
	canceled   bool
	timedOut   bool
	waited     bool
	cancelErr  error
}

type terminalState struct {
	committed bool
	kind      ErrorKind
	reason    StopReason
	cause     error
}

func (s *terminalState) commit(observation terminalObservation) {
	if s.committed {
		return
	}
	s.committed = true
	switch {
	case observation.overflowed:
		s.kind = ErrorOutputLimit
		s.reason = StopReasonOutputOverflow
		s.cause = errOutputExceeded
	case observation.canceled:
		s.kind = ErrorCanceled
		s.reason = StopReasonCallerCancellation
		s.cause = observation.cancelErr
		if s.cause == nil {
			s.cause = context.Canceled
		}
	case observation.timedOut:
		s.kind = ErrorTimeout
		s.reason = StopReasonSupervisorTimeout
		s.cause = errExecutionTimedOut
	case observation.waited:
		s.reason = StopReasonNormalExit
	default:
		s.kind = ErrorCleanup
		s.reason = StopReasonCleanupFailure
		s.cause = errors.New("missing terminal process event")
	}
}

func (s terminalState) runError() error {
	if s.kind == "" {
		return nil
	}
	return &RunError{Kind: s.kind, Err: s.cause}
}

type readerResult struct {
	stream string
	err    error
}

type windowsJobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

type windowsHandleList interface {
	list() *windows.ProcThreadAttributeList
	delete()
}

type windowsCreateProcessRequest struct {
	applicationName  []uint16
	commandLine      []uint16
	environment      []uint16
	currentDirectory []uint16
	stdin            windows.Handle
	stdout           windows.Handle
	stderr           windows.Handle
	handleList       windowsHandleList
	inheritHandles   bool
	creationFlags    uint32
}

type windowsAPI interface {
	createPipe() (windows.Handle, windows.Handle, error)
	clearHandleInheritance(windows.Handle) error
	newHandleList([]windows.Handle) (windowsHandleList, error)
	createProcess(windowsCreateProcessRequest) (
		windows.ProcessInformation,
		error,
	)
	createJobObject() (windows.Handle, error)
	setJobKillOnClose(windows.Handle) error
	assignProcessToJobObject(windows.Handle, windows.Handle) error
	resumeThread(windows.Handle) (uint32, error)
	terminateJobObject(windows.Handle, uint32) error
	terminateProcess(windows.Handle, uint32) error
	waitForSingleObject(windows.Handle, uint32) (uint32, error)
	getExitCodeProcess(windows.Handle) (uint32, error)
	queryJobAccounting(windows.Handle) (windowsJobAccounting, error)
	readFile(windows.Handle, []byte) (uint32, error)
	writeFile(windows.Handle, []byte) (uint32, error)
	openCurrentThread() (windows.Handle, error)
	cancelSynchronousIO(windows.Handle) error
	closeHandle(windows.Handle) error
}

type nativeWindowsAPI struct{}

type nativeWindowsHandleList struct {
	container *windows.ProcThreadAttributeListContainer
}

func (l *nativeWindowsHandleList) list() *windows.ProcThreadAttributeList {
	return l.container.List()
}

func (l *nativeWindowsHandleList) delete() {
	l.container.Delete()
}

func (nativeWindowsAPI) createPipe() (
	windows.Handle,
	windows.Handle,
	error,
) {
	attributes := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	var readHandle, writeHandle windows.Handle
	err := windows.CreatePipe(
		&readHandle,
		&writeHandle,
		attributes,
		0,
	)
	return readHandle, writeHandle, err
}

func (nativeWindowsAPI) clearHandleInheritance(
	handle windows.Handle,
) error {
	return windows.SetHandleInformation(
		handle,
		windows.HANDLE_FLAG_INHERIT,
		0,
	)
}

func (nativeWindowsAPI) newHandleList(
	handles []windows.Handle,
) (windowsHandleList, error) {
	if len(handles) != 3 {
		return nil, errors.New("windows stdio allowlist must contain three handles")
	}
	container, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	// Update retains the slice pointer until Delete, so this wrapper owns the
	// attribute container through the synchronous CreateProcess call.
	if err := container.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&handles[0]),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
	); err != nil {
		container.Delete()
		return nil, err
	}
	return &nativeWindowsHandleList{container: container}, nil
}

func (nativeWindowsAPI) createProcess(
	request windowsCreateProcessRequest,
) (windows.ProcessInformation, error) {
	var information windows.ProcessInformation
	if len(request.applicationName) == 0 ||
		len(request.commandLine) == 0 ||
		len(request.environment) < 2 ||
		len(request.currentDirectory) == 0 ||
		request.handleList == nil {
		return information, errors.New("incomplete Windows process request")
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  request.stdin,
			StdOutput: request.stdout,
			StdErr:    request.stderr,
		},
		ProcThreadAttributeList: request.handleList.list(),
	}
	err := windows.CreateProcess(
		&request.applicationName[0],
		&request.commandLine[0],
		nil,
		nil,
		request.inheritHandles,
		request.creationFlags,
		&request.environment[0],
		&request.currentDirectory[0],
		&startup.StartupInfo,
		&information,
	)
	return information, err
}

func (nativeWindowsAPI) createJobObject() (windows.Handle, error) {
	return windows.CreateJobObject(nil, nil)
}

func (nativeWindowsAPI) setJobKillOnClose(job windows.Handle) error {
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags =
		windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	)
	return err
}

func (nativeWindowsAPI) assignProcessToJobObject(
	job windows.Handle,
	process windows.Handle,
) error {
	return windows.AssignProcessToJobObject(job, process)
}

func (nativeWindowsAPI) resumeThread(
	thread windows.Handle,
) (uint32, error) {
	return windows.ResumeThread(thread)
}

func (nativeWindowsAPI) terminateJobObject(
	job windows.Handle,
	exitCode uint32,
) error {
	return windows.TerminateJobObject(job, exitCode)
}

func (nativeWindowsAPI) terminateProcess(
	process windows.Handle,
	exitCode uint32,
) error {
	return windows.TerminateProcess(process, exitCode)
}

func (nativeWindowsAPI) waitForSingleObject(
	handle windows.Handle,
	milliseconds uint32,
) (uint32, error) {
	return windows.WaitForSingleObject(handle, milliseconds)
}

func (nativeWindowsAPI) getExitCodeProcess(
	handle windows.Handle,
) (uint32, error) {
	var exitCode uint32
	err := windows.GetExitCodeProcess(handle, &exitCode)
	return exitCode, err
}

func (nativeWindowsAPI) queryJobAccounting(
	job windows.Handle,
) (windowsJobAccounting, error) {
	var accounting windowsJobAccounting
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)),
		uint32(unsafe.Sizeof(accounting)),
		nil,
	)
	return accounting, err
}

func (nativeWindowsAPI) readFile(
	handle windows.Handle,
	buffer []byte,
) (uint32, error) {
	var read uint32
	err := windows.ReadFile(handle, buffer, &read, nil)
	return read, err
}

func (nativeWindowsAPI) writeFile(
	handle windows.Handle,
	buffer []byte,
) (uint32, error) {
	var written uint32
	err := windows.WriteFile(handle, buffer, &written, nil)
	return written, err
}

func (nativeWindowsAPI) openCurrentThread() (windows.Handle, error) {
	return windows.OpenThread(
		windows.THREAD_TERMINATE,
		false,
		windows.GetCurrentThreadId(),
	)
}

func (nativeWindowsAPI) cancelSynchronousIO(
	thread windows.Handle,
) error {
	result, _, callErr := cancelSynchronousIOProcedure.Call(uintptr(thread))
	if result != 0 {
		return nil
	}
	if callErr != nil && callErr != syscall.Errno(0) {
		return callErr
	}
	return errors.New("CancelSynchronousIo failed without a Windows error")
}

func (nativeWindowsAPI) closeHandle(handle windows.Handle) error {
	return windows.CloseHandle(handle)
}

type windowsHandleLedger struct {
	api     windowsAPI
	mu      sync.Mutex
	entries []*windowsHandleOwner
}

type windowsHandleOwner struct {
	ledger *windowsHandleLedger
	handle windows.Handle
	name   string
	closed bool
}

func newWindowsHandleLedger(api windowsAPI) *windowsHandleLedger {
	return &windowsHandleLedger{api: api}
}

func (l *windowsHandleLedger) acquire(
	handle windows.Handle,
	name string,
) *windowsHandleOwner {
	if l == nil || handle == 0 || handle == windows.InvalidHandle {
		return nil
	}
	owner := &windowsHandleOwner{
		ledger: l,
		handle: handle,
		name:   name,
	}
	l.mu.Lock()
	l.entries = append(l.entries, owner)
	l.mu.Unlock()
	return owner
}

func (o *windowsHandleOwner) close() error {
	if o == nil || o.ledger == nil {
		return nil
	}
	o.ledger.mu.Lock()
	if o.closed {
		o.ledger.mu.Unlock()
		return nil
	}
	o.closed = true
	handle := o.handle
	o.ledger.mu.Unlock()
	if err := o.ledger.api.closeHandle(handle); err != nil {
		return fmt.Errorf("close Windows %s handle: %w", o.name, err)
	}
	return nil
}

func (o *windowsHandleOwner) isClosed() bool {
	if o == nil || o.ledger == nil {
		return true
	}
	o.ledger.mu.Lock()
	defer o.ledger.mu.Unlock()
	return o.closed
}

func (o *windowsHandleOwner) use(
	operation func(windows.Handle) error,
) error {
	if o == nil || o.ledger == nil {
		return nil
	}
	o.ledger.mu.Lock()
	defer o.ledger.mu.Unlock()
	if o.closed {
		return nil
	}
	return operation(o.handle)
}

func (l *windowsHandleLedger) closeReverse() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	entries := append([]*windowsHandleOwner(nil), l.entries...)
	l.mu.Unlock()
	var closeErrors []error
	for index := len(entries) - 1; index >= 0; index-- {
		if err := entries[index].close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

type windowsStartRequest struct {
	applicationName  []uint16
	commandLine      []uint16
	environment      []uint16
	currentDirectory []uint16
	directory        string
	prelaunch        func() error
	beforeCreate     func(windowsLaunchView) error
	beforeResume     func(windowsLaunchView) error
	afterResume      func(windowsLaunchView)
	beforeJobClose   func(windowsJobCloseView)
}

type windowsProcess struct {
	api            windowsAPI
	ledger         *windowsHandleLedger
	completions    *completionOwner
	pid            uint32
	assigned       bool
	stdinParent    *windowsHandleOwner
	stdoutParent   *windowsHandleOwner
	stderrParent   *windowsHandleOwner
	process        *windowsHandleOwner
	thread         *windowsHandleOwner
	job            *windowsHandleOwner
	deferProcess   sync.Once
	deferJob       sync.Once
	deferIO        sync.Once
	beforeJobClose func(windowsJobCloseView)
}

type windowsIOWorker struct {
	thread       *windowsHandleOwner
	cancellation *windowsIOCancellation
}

type windowsIOWorkers struct {
	stdin  windowsIOWorker
	stdout windowsIOWorker
	stderr windowsIOWorker
}

func (workers windowsIOWorkers) cancel(api windowsAPI) {
	for _, worker := range []windowsIOWorker{
		workers.stdin,
		workers.stdout,
		workers.stderr,
	} {
		if worker.cancellation != nil {
			worker.cancellation.request(api, worker.thread)
		}
	}
}

type windowsIOCancellation struct {
	mu           sync.Mutex
	requestedIO  bool
	finished     bool
	done         chan struct{}
	retryDone    chan struct{}
	firstError   error
	failureCount int
}

func newWindowsIOCancellation() *windowsIOCancellation {
	return &windowsIOCancellation{
		done:      make(chan struct{}),
		retryDone: make(chan struct{}),
	}
}

func (c *windowsIOCancellation) request(
	api windowsAPI,
	thread *windowsHandleOwner,
) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.requestedIO || c.finished {
		c.mu.Unlock()
		return
	}
	c.requestedIO = true
	c.mu.Unlock()
	go c.retry(api, thread)
}

func (c *windowsIOCancellation) retry(
	api windowsAPI,
	thread *windowsHandleOwner,
) {
	defer close(c.retryDone)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		default:
		}
		err := thread.use(api.cancelSynchronousIO)
		if err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
			c.mu.Lock()
			if c.firstError == nil {
				c.firstError = err
			}
			c.failureCount++
			c.mu.Unlock()
		}
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}
	}
}

func (c *windowsIOCancellation) requested() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestedIO
}

func (c *windowsIOCancellation) finish() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.finished {
		firstError := c.firstError
		failureCount := c.failureCount
		c.mu.Unlock()
		return windowsIOCancellationError(firstError, failureCount)
	}
	c.finished = true
	close(c.done)
	requested := c.requestedIO
	c.mu.Unlock()
	if requested {
		<-c.retryDone
	}
	c.mu.Lock()
	firstError := c.firstError
	failureCount := c.failureCount
	c.mu.Unlock()
	return windowsIOCancellationError(firstError, failureCount)
}

func windowsIOCancellationError(first error, count int) error {
	if first == nil {
		return nil
	}
	return fmt.Errorf(
		"cancel synchronous Windows pipe I/O failed %d time(s): %w",
		count,
		first,
	)
}

func runPlatform(
	ctx context.Context,
	root *Root,
	runtime Runtime,
	spec CommandSpec,
	limits Limits,
	completions *completionOwner,
	hooks runnerHooks,
) (runnerResult, error) {
	return runWindowsOwned(
		ctx,
		root,
		runtime,
		spec,
		limits,
		completions,
		hooks,
		nativeWindowsAPI{},
	)
}

func runWindowsOwned(
	ctx context.Context,
	root *Root,
	requestRuntime Runtime,
	spec CommandSpec,
	limits Limits,
	completions *completionOwner,
	hooks runnerHooks,
	api windowsAPI,
) (runnerResult, error) {
	result := Result{
		ExitCode:   -1,
		StopReason: StopReasonNormalExit,
		StopAction: StopActionNone,
	}
	if err := validateWindowsCommandShape(requestRuntime, spec); err != nil {
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  err,
		}
	}
	if err := contextError(ctx); err != nil {
		result.StopReason = StopReasonCallerCancellation
		return runnerResult{result: result}, &RunError{
			Kind: ErrorCanceled,
			Err:  err,
		}
	}
	commandLine, err := windowsCommandLine(spec.Executable, spec.Args)
	if err != nil {
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  err,
		}
	}
	environment, err := windowsEnvironmentBlock(spec.Env)
	if err != nil {
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  err,
		}
	}
	applicationName, err := windows.UTF16FromString(spec.Executable)
	if err != nil {
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  fmt.Errorf("encode provider executable: %w", err),
		}
	}
	commandLineUTF16, err := windows.UTF16FromString(commandLine)
	if err != nil {
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  fmt.Errorf("encode provider command line: %w", err),
		}
	}
	currentDirectory, err := windows.UTF16FromString(spec.Dir)
	if err != nil {
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  fmt.Errorf("encode provider directory: %w", err),
		}
	}

	process, startErr := startWindowsProcess(
		api,
		windowsStartRequest{
			applicationName:  applicationName,
			commandLine:      commandLineUTF16,
			environment:      environment,
			currentDirectory: currentDirectory,
			directory:        spec.Dir,
			beforeCreate:     hooks.beforeCreateProcess,
			beforeResume:     hooks.beforeResume,
			afterResume:      hooks.afterResume,
			beforeJobClose:   hooks.beforeJobClose,
			prelaunch: func() error {
				if err := contextError(ctx); err != nil {
					return err
				}
				if err := validateWindowsExecutable(spec.Executable); err != nil {
					return err
				}
				if err := root.validateRuntimePath(requestRuntime); err != nil {
					return fmt.Errorf(
						"validate runtime immediately before launch: %w",
						err,
					)
				}
				return nil
			},
		},
		limits.Cleanup,
		completions,
	)
	if startErr != nil {
		var runErr *RunError
		if errors.As(startErr, &runErr) {
			switch runErr.Kind {
			case ErrorCanceled:
				result.StopReason = StopReasonCallerCancellation
			case ErrorCleanup:
				result.StopReason = StopReasonCleanupFailure
			}
			return runnerResult{result: result}, startErr
		}
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  startErr,
		}
	}

	overflow := make(chan struct{}, 1)
	stdoutCapture := newCapture(limits.StdoutBytes, overflow)
	stderrCapture := newCapture(limits.StderrBytes, overflow)
	writerDone := make(chan error, 1)
	stdoutDone := make(chan readerResult, 1)
	stderrDone := make(chan readerResult, 1)
	writerStarted := make(chan windowsIOWorker, 1)
	stdoutStarted := make(chan windowsIOWorker, 1)
	stderrStarted := make(chan windowsIOWorker, 1)
	rootWait := process.startRootWait()

	go writeWindowsStdin(
		process.api,
		process.ledger,
		process.stdinParent,
		spec.Stdin,
		writerStarted,
		writerDone,
	)
	go readWindowsPipe(
		process.api,
		process.ledger,
		"stdout",
		process.stdoutParent,
		stdoutCapture,
		stdoutStarted,
		stdoutDone,
	)
	go readWindowsPipe(
		process.api,
		process.ledger,
		"stderr",
		process.stderrParent,
		stderrCapture,
		stderrStarted,
		stderrDone,
	)
	ioWorkers := windowsIOWorkers{
		stdin:  <-writerStarted,
		stdout: <-stdoutStarted,
		stderr: <-stderrStarted,
	}

	if ctx == nil {
		ctx = context.Background()
	}
	executionExpired := make(chan struct{})
	executionTimer := time.AfterFunc(limits.Execution, func() {
		close(executionExpired)
	})
	defer executionTimer.Stop()
	events := windowsTerminalEventView{
		waitReady:    rootWait.ready,
		timeoutReady: executionExpired,
		overflowed: func() bool {
			return stdoutCapture.Overflowed() ||
				stderrCapture.Overflowed()
		},
	}

	state := terminalState{}
	for !state.committed {
		if hooks.beforeCommit != nil {
			hooks.beforeCommit(events)
		}
		select {
		case <-overflow:
		case <-ctx.Done():
		case <-executionExpired:
		case <-rootWait.ready:
		}
		observation := terminalObservation{
			overflowed: stdoutCapture.Overflowed() ||
				stderrCapture.Overflowed(),
		}
		if !observation.overflowed && ctx.Err() != nil {
			observation.canceled = true
			observation.cancelErr = ctx.Err()
		}
		if !observation.overflowed && !observation.canceled {
			select {
			case <-executionExpired:
				observation.timedOut = true
			default:
			}
		}
		if !observation.overflowed &&
			!observation.canceled &&
			!observation.timedOut {
			select {
			case <-rootWait.ready:
				observation.waited = true
			default:
			}
		}
		state.commit(observation)
	}
	if hooks.afterCommit != nil {
		hooks.afterCommit(state)
	}

	waited, action, cleanupErr := terminateWindows(
		process,
		ioWorkers,
		state,
		limits.Cleanup,
		rootWait,
		writerDone,
		stdoutDone,
		stderrDone,
	)
	result.Stdout = stdoutCapture.Bytes()
	result.Stderr = stderrCapture.Bytes()
	result.StdoutTotal = stdoutCapture.Total()
	result.StderrTotal = stderrCapture.Total()
	result.ExitCode = waited.exitCode
	result.StopReason = state.reason
	result.StopAction = action

	terminalErr := state.runError()
	if cleanupErr != nil {
		result.StopReason = StopReasonCleanupFailure
		return runnerResult{result: result}, &RunError{
			Kind: ErrorCleanup,
			Err:  errors.Join(terminalErr, cleanupErr),
		}
	}
	if stdoutCapture.Overflowed() || stderrCapture.Overflowed() {
		result.StopReason = StopReasonOutputOverflow
		return runnerResult{result: result}, &RunError{
			Kind: ErrorOutputLimit,
			Err:  errOutputExceeded,
		}
	}
	return runnerResult{result: result}, terminalErr
}

func windowsCommandLine(
	executable string,
	args []string,
) (string, error) {
	values := make([]string, 0, len(args)+1)
	values = append(values, executable)
	values = append(values, args...)
	escaped := make([]string, len(values))
	for index, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return "", errors.New("provider command argument contains NUL")
		}
		escaped[index] = windows.EscapeArg(value)
	}
	return strings.Join(escaped, " "), nil
}

func windowsEnvironmentBlock(entries []string) ([]uint16, error) {
	type environmentEntry struct {
		name  string
		value string
	}
	validated := make([]environmentEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if strings.IndexByte(entry, 0) >= 0 {
			return nil, errors.New("provider environment contains NUL")
		}
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 {
			return nil, errors.New("provider environment entry is malformed")
		}
		name := entry[:separator]
		if !validEnvironmentName(name) {
			return nil, errors.New("provider environment name is malformed")
		}
		folded := strings.ToUpper(name)
		if _, exists := seen[folded]; exists {
			return nil, errors.New(
				"provider environment contains a duplicate name",
			)
		}
		seen[folded] = struct{}{}
		validated = append(validated, environmentEntry{
			name:  folded,
			value: entry,
		})
	}
	sort.Slice(validated, func(left, right int) bool {
		return validated[left].name < validated[right].name
	})
	if len(validated) == 0 {
		return []uint16{0, 0}, nil
	}
	var block []uint16
	for _, entry := range validated {
		encoded, err := windows.UTF16FromString(entry.value)
		if err != nil {
			return nil, fmt.Errorf("encode provider environment: %w", err)
		}
		block = append(block, encoded...)
	}
	block = append(block, 0)
	return block, nil
}

func validEnvironmentName(name string) bool {
	for index, value := range []byte(name) {
		if (value >= 'a' && value <= 'z') ||
			(value >= 'A' && value <= 'Z') ||
			value == '_' ||
			(index > 0 && value >= '0' && value <= '9') {
			continue
		}
		return false
	}
	return name != ""
}

func validateWindowsCommandShape(
	requestRuntime Runtime,
	spec CommandSpec,
) error {
	if !filepath.IsAbs(spec.Executable) ||
		strings.IndexByte(spec.Executable, 0) >= 0 {
		return errors.New("provider executable must be an absolute safe path")
	}
	switch strings.ToLower(filepath.Ext(spec.Executable)) {
	case ".cmd", ".bat":
		return errors.New("windows command shell shims are not executable")
	}
	if spec.Dir != requestRuntime.Dir || !filepath.IsAbs(spec.Dir) {
		return errors.New("provider directory must exactly match runtime")
	}
	for _, argument := range spec.Args {
		if strings.IndexByte(argument, 0) >= 0 {
			return errors.New("provider argument contains NUL")
		}
	}
	return nil
}

func validateWindowsExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf(
			"inspect provider executable: %w",
			markExecutableUnavailable(err),
		)
	}
	if !info.Mode().IsRegular() {
		return errors.New("provider executable is not a regular file")
	}
	return nil
}

func startWindowsProcess(
	api windowsAPI,
	request windowsStartRequest,
	cleanup time.Duration,
	completions *completionOwner,
) (*windowsProcess, error) {
	process := &windowsProcess{
		api:            api,
		ledger:         newWindowsHandleLedger(api),
		completions:    completions,
		beforeJobClose: request.beforeJobClose,
	}
	failBeforeProcess := func(stage error) (*windowsProcess, error) {
		closeErr := process.ledger.closeReverse()
		kind := ErrorStart
		if errors.Is(stage, context.Canceled) ||
			errors.Is(stage, context.DeadlineExceeded) {
			kind = ErrorCanceled
		}
		if closeErr != nil {
			kind = ErrorCleanup
		}
		return nil, &RunError{
			Kind: kind,
			Err:  errors.Join(stage, closeErr),
		}
	}

	stdinRead, stdinWrite, err := api.createPipe()
	if err != nil {
		return failBeforeProcess(fmt.Errorf("create provider stdin pipe: %w", err))
	}
	stdinChild := process.ledger.acquire(stdinRead, "child stdin")
	process.stdinParent = process.ledger.acquire(stdinWrite, "parent stdin")
	if err := api.clearHandleInheritance(process.stdinParent.handle); err != nil {
		return failBeforeProcess(fmt.Errorf(
			"clear parent stdin inheritance: %w",
			err,
		))
	}

	stdoutRead, stdoutWrite, err := api.createPipe()
	if err != nil {
		return failBeforeProcess(fmt.Errorf("create provider stdout pipe: %w", err))
	}
	process.stdoutParent = process.ledger.acquire(stdoutRead, "parent stdout")
	stdoutChild := process.ledger.acquire(stdoutWrite, "child stdout")
	if err := api.clearHandleInheritance(process.stdoutParent.handle); err != nil {
		return failBeforeProcess(fmt.Errorf(
			"clear parent stdout inheritance: %w",
			err,
		))
	}

	stderrRead, stderrWrite, err := api.createPipe()
	if err != nil {
		return failBeforeProcess(fmt.Errorf("create provider stderr pipe: %w", err))
	}
	process.stderrParent = process.ledger.acquire(stderrRead, "parent stderr")
	stderrChild := process.ledger.acquire(stderrWrite, "child stderr")
	if err := api.clearHandleInheritance(process.stderrParent.handle); err != nil {
		return failBeforeProcess(fmt.Errorf(
			"clear parent stderr inheritance: %w",
			err,
		))
	}

	childHandles := []windows.Handle{
		stdinChild.handle,
		stdoutChild.handle,
		stderrChild.handle,
	}
	handleList, err := api.newHandleList(childHandles)
	if err != nil {
		return failBeforeProcess(fmt.Errorf(
			"create provider stdio handle allowlist: %w",
			err,
		))
	}
	deleteHandleList := true
	defer func() {
		if deleteHandleList {
			handleList.delete()
		}
	}()
	if request.beforeCreate != nil {
		if err := request.beforeCreate(windowsLaunchView{
			directory: request.directory,
			childHandles: [3]windows.Handle{
				childHandles[0],
				childHandles[1],
				childHandles[2],
			},
		}); err != nil {
			return failBeforeProcess(fmt.Errorf(
				"before Windows process creation: %w",
				err,
			))
		}
	}
	if request.prelaunch != nil {
		if err := request.prelaunch(); err != nil {
			return failBeforeProcess(err)
		}
	}

	information, createErr := api.createProcess(windowsCreateProcessRequest{
		applicationName:  request.applicationName,
		commandLine:      request.commandLine,
		environment:      request.environment,
		currentDirectory: request.currentDirectory,
		stdin:            childHandles[0],
		stdout:           childHandles[1],
		stderr:           childHandles[2],
		handleList:       handleList,
		inheritHandles:   true,
		creationFlags: uint32(
			windows.CREATE_SUSPENDED |
				windows.CREATE_UNICODE_ENVIRONMENT |
				windows.EXTENDED_STARTUPINFO_PRESENT,
		),
	})
	createErr = markExecutableUnavailable(createErr)
	process.process = process.ledger.acquire(
		information.Process,
		"provider process",
	)
	process.thread = process.ledger.acquire(
		information.Thread,
		"provider thread",
	)
	process.pid = information.ProcessId
	childCloseErr := errors.Join(
		stderrChild.close(),
		stdoutChild.close(),
		stdinChild.close(),
	)
	handleList.delete()
	deleteHandleList = false

	if createErr != nil {
		if process.process == nil {
			closeErr := process.ledger.closeReverse()
			kind := ErrorStart
			if childCloseErr != nil || closeErr != nil {
				kind = ErrorCleanup
			}
			return nil, &RunError{
				Kind: kind,
				Err: errors.Join(
					fmt.Errorf("create suspended provider process: %w", createErr),
					childCloseErr,
					closeErr,
				),
			}
		}
		cleanupErr := process.abortStart(cleanup)
		return nil, &RunError{
			Kind: ErrorCleanup,
			Err: errors.Join(
				fmt.Errorf("create suspended provider process: %w", createErr),
				childCloseErr,
				cleanupErr,
			),
		}
	}
	if process.process == nil || process.thread == nil {
		cleanupErr := process.abortStart(cleanup)
		return nil, &RunError{
			Kind: ErrorCleanup,
			Err: errors.Join(
				errors.New("CreateProcess returned incomplete handles"),
				childCloseErr,
				cleanupErr,
			),
		}
	}
	if childCloseErr != nil {
		cleanupErr := process.abortStart(cleanup)
		return nil, &RunError{
			Kind: ErrorCleanup,
			Err:  errors.Join(childCloseErr, cleanupErr),
		}
	}

	jobHandle, err := api.createJobObject()
	process.job = process.ledger.acquire(jobHandle, "provider Job")
	if err != nil {
		return nil, process.launchFailure(
			cleanup,
			fmt.Errorf("create provider Job Object: %w", err),
		)
	}
	if err := api.setJobKillOnClose(process.job.handle); err != nil {
		return nil, process.launchFailure(
			cleanup,
			fmt.Errorf("configure provider Job Object: %w", err),
		)
	}
	if err := api.assignProcessToJobObject(
		process.job.handle,
		process.process.handle,
	); err != nil {
		return nil, process.launchFailure(
			cleanup,
			fmt.Errorf("assign provider process to Job Object: %w", err),
		)
	}
	process.assigned = true
	launchView := windowsLaunchView{
		directory: request.directory,
		childHandles: [3]windows.Handle{
			childHandles[0],
			childHandles[1],
			childHandles[2],
		},
		process: process.process.handle,
	}
	if request.beforeResume != nil {
		if err := request.beforeResume(launchView); err != nil {
			return nil, process.launchFailure(
				cleanup,
				fmt.Errorf("before Windows process resume: %w", err),
			)
		}
	}
	if _, err := api.resumeThread(process.thread.handle); err != nil {
		return nil, process.launchFailure(
			cleanup,
			fmt.Errorf("resume provider process: %w", err),
		)
	}
	if request.afterResume != nil {
		request.afterResume(launchView)
	}
	if err := process.thread.close(); err != nil {
		return nil, process.launchFailure(cleanup, err)
	}
	return process, nil
}

func (p *windowsProcess) launchFailure(
	cleanup time.Duration,
	stage error,
) error {
	return &RunError{
		Kind: ErrorCleanup,
		Err:  errors.Join(stage, p.abortStart(cleanup)),
	}
}

func (p *windowsProcess) abortStart(timeout time.Duration) error {
	var cleanupErrors []error
	cleanupContext, cancel := context.WithTimeout(
		context.Background(),
		timeout,
	)
	defer cancel()
	terminationStarted := false
	if p.process != nil {
		var terminateErr error
		if p.assigned && p.job != nil {
			terminateErr = p.api.terminateJobObject(
				p.job.handle,
				windowsTerminateExitCode,
			)
		} else {
			terminateErr = p.api.terminateProcess(
				p.process.handle,
				windowsTerminateExitCode,
			)
		}
		terminationStarted = terminateErr == nil
		if terminateErr != nil {
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("terminate failed Windows launch: %w", terminateErr),
			)
		}
		event, waitErr := p.api.waitForSingleObject(
			p.process.handle,
			windowsContextMilliseconds(cleanupContext),
		)
		switch {
		case waitErr != nil:
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("wait for failed Windows launch: %w", waitErr),
			)
			if !p.assigned && !terminationStarted {
				p.deferStartProcessTermination()
			} else {
				p.deferStartProcessWait()
			}
		case event == windows.WAIT_OBJECT_0:
			if _, err := p.api.getExitCodeProcess(p.process.handle); err != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("read failed Windows launch exit code: %w", err),
				)
				p.deferStartProcessWait()
			} else if err := p.process.close(); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		default:
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf(
					"failed Windows launch wait event %#x",
					event,
				),
			)
			if !p.assigned && !terminationStarted {
				p.deferStartProcessTermination()
			} else {
				p.deferStartProcessWait()
			}
		}
	}
	if p.thread != nil {
		if err := p.thread.close(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if p.job != nil {
		if p.assigned {
			accounting, emptyErr := waitWindowsJobEmpty(
				cleanupContext,
				p.api,
				p.job.handle,
			)
			if emptyErr != nil {
				cleanupErrors = append(cleanupErrors, emptyErr)
				p.deferJobContainment(terminationStarted)
			} else if err := p.closeVerifiedJob(accounting); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		} else if err := p.job.close(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	for _, owner := range []*windowsHandleOwner{
		p.stderrParent,
		p.stdoutParent,
		p.stdinParent,
	} {
		if err := owner.close(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

type windowsRootWait struct {
	done  <-chan waitResult
	ready <-chan struct{}
}

func (p *windowsProcess) startRootWait() windowsRootWait {
	done := make(chan waitResult, 1)
	ready := make(chan struct{})
	go func() {
		result := waitWindowsProcess(p.api, p.process)
		done <- result
		close(ready)
	}()
	return windowsRootWait{done: done, ready: ready}
}

func waitWindowsProcess(
	api windowsAPI,
	process *windowsHandleOwner,
) waitResult {
	result := waitResult{exitCode: -1}
	if process == nil {
		result.err = errors.New("missing Windows process handle")
		return result
	}
	event, err := api.waitForSingleObject(process.handle, windows.INFINITE)
	if err != nil {
		result.err = fmt.Errorf("wait for provider process: %w", err)
	} else if event != windows.WAIT_OBJECT_0 {
		result.err = fmt.Errorf("provider process wait event %#x", event)
	} else {
		exitCode, exitErr := api.getExitCodeProcess(process.handle)
		if exitErr != nil {
			result.err = fmt.Errorf(
				"read provider process exit code: %w",
				exitErr,
			)
		} else {
			result.exitCode = int(exitCode)
		}
	}
	if result.err == nil {
		result.err = process.close()
	}
	return result
}

func (p *windowsProcess) deferStartProcessWait() {
	p.deferProcessWait(nil, false, false)
}

func (p *windowsProcess) deferStartProcessTermination() {
	p.deferProcessWait(nil, false, true)
}

func (p *windowsProcess) deferPendingProcessWait(
	pending <-chan waitResult,
) {
	p.deferProcessWait(pending, true, false)
}

func (p *windowsProcess) deferProcessWait(
	pending <-chan waitResult,
	preservePendingError bool,
	retryTermination bool,
) {
	if p.process == nil {
		return
	}
	p.deferProcess.Do(func() {
		done := make(chan waitResult, 1)
		go func() {
			if retryTermination {
				result := p.retryProcessTermination()
				done <- result
				return
			}
			var result waitResult
			if pending != nil {
				result = <-pending
			} else {
				result = waitWindowsProcess(p.api, p.process)
			}
			pendingErr := result.err
			for {
				if result.err == nil || p.process.isClosed() {
					if preservePendingError {
						result.err = errors.Join(pendingErr, result.err)
					}
					done <- result
					return
				}
				time.Sleep(2 * time.Millisecond)
				result = waitWindowsProcess(p.api, p.process)
			}
		}()
		p.completions.deferWait(int(p.pid), done)
	})
}

func (p *windowsProcess) retryProcessTermination() waitResult {
	result := waitResult{exitCode: -1}
	terminationStarted := false
	for !p.process.isClosed() {
		if !terminationStarted {
			if err := p.api.terminateProcess(
				p.process.handle,
				windowsTerminateExitCode,
			); err == nil {
				terminationStarted = true
			}
		}
		event, waitErr := p.api.waitForSingleObject(
			p.process.handle,
			windowsDurationMilliseconds(2*time.Millisecond),
		)
		if waitErr != nil || event != windows.WAIT_OBJECT_0 {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		exitCode, exitErr := p.api.getExitCodeProcess(p.process.handle)
		if exitErr != nil {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		result.exitCode = int(exitCode)
		result.err = p.process.close()
		return result
	}
	return result
}

func (p *windowsProcess) deferJobContainment(terminationStarted bool) {
	if p.job == nil || p.job.isClosed() {
		return
	}
	p.deferJob.Do(func() {
		done := make(chan waitResult, 1)
		go func() {
			var err error
			for {
				if p.job.isClosed() {
					break
				}
				accounting, queryErr := p.api.queryJobAccounting(p.job.handle)
				if queryErr == nil &&
					accounting.TotalProcesses > 0 &&
					accounting.ActiveProcesses == 0 {
					err = p.closeVerifiedJob(accounting)
					break
				}
				if !terminationStarted {
					if terminateErr := p.api.terminateJobObject(
						p.job.handle,
						windowsTerminateExitCode,
					); terminateErr == nil {
						terminationStarted = true
					}
				}
				time.Sleep(2 * time.Millisecond)
			}
			done <- waitResult{err: err, exitCode: -1}
		}()
		p.completions.deferWait(int(p.pid), done)
	})
}

func writeWindowsStdin(
	api windowsAPI,
	ledger *windowsHandleLedger,
	owner *windowsHandleOwner,
	data []byte,
	started chan<- windowsIOWorker,
	done chan<- error,
) {
	runtime.LockOSThread()
	var writeErr error
	cancellation := newWindowsIOCancellation()
	threadHandle, threadErr := api.openCurrentThread()
	var thread *windowsHandleOwner
	if threadErr == nil {
		thread = ledger.acquire(threadHandle, "provider stdin I/O thread")
	}
	started <- windowsIOWorker{
		thread:       thread,
		cancellation: cancellation,
	}
	defer func() {
		done <- errors.Join(
			writeErr,
			cancellation.finish(),
			owner.close(),
			thread.close(),
		)
		runtime.UnlockOSThread()
	}()
	if threadErr != nil {
		writeErr = fmt.Errorf("open provider stdin I/O thread: %w", threadErr)
		return
	}
	for len(data) != 0 {
		if cancellation.requested() {
			return
		}
		written, err := api.writeFile(owner.handle, data)
		if err != nil {
			if windowsPipeClosed(err) ||
				(cancellation.requested() &&
					errors.Is(err, windows.ERROR_OPERATION_ABORTED)) {
				return
			}
			writeErr = err
			return
		}
		if written == 0 || uint64(written) > uint64(len(data)) {
			writeErr = io.ErrShortWrite
			return
		}
		data = data[written:]
	}
}

func readWindowsPipe(
	api windowsAPI,
	ledger *windowsHandleLedger,
	stream string,
	owner *windowsHandleOwner,
	destination io.Writer,
	started chan<- windowsIOWorker,
	done chan<- readerResult,
) {
	runtime.LockOSThread()
	var readErr error
	cancellation := newWindowsIOCancellation()
	threadHandle, threadErr := api.openCurrentThread()
	var thread *windowsHandleOwner
	if threadErr == nil {
		thread = ledger.acquire(
			threadHandle,
			"provider "+stream+" I/O thread",
		)
	}
	started <- windowsIOWorker{
		thread:       thread,
		cancellation: cancellation,
	}
	defer func() {
		readErr = errors.Join(
			readErr,
			cancellation.finish(),
			owner.close(),
			thread.close(),
		)
		done <- readerResult{stream: stream, err: readErr}
		runtime.UnlockOSThread()
	}()
	if threadErr != nil {
		readErr = fmt.Errorf(
			"open provider %s I/O thread: %w",
			stream,
			threadErr,
		)
		return
	}
	buffer := make([]byte, 32*1024)
	for {
		if cancellation.requested() {
			return
		}
		count, err := api.readFile(owner.handle, buffer)
		if count > uint32(len(buffer)) {
			readErr = errors.New("Windows pipe returned an invalid byte count")
			return
		}
		if count != 0 {
			if _, writeErr := destination.Write(buffer[:count]); writeErr != nil {
				readErr = writeErr
				return
			}
		}
		if err != nil {
			if windowsPipeClosed(err) ||
				(cancellation.requested() &&
					errors.Is(err, windows.ERROR_OPERATION_ABORTED)) {
				return
			}
			readErr = err
			return
		}
		if count == 0 {
			readErr = io.ErrNoProgress
			return
		}
	}
}

func windowsPipeClosed(err error) bool {
	return errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
		errors.Is(err, windows.ERROR_NO_DATA) ||
		errors.Is(err, windows.ERROR_HANDLE_EOF)
}

func terminateWindows(
	process *windowsProcess,
	ioWorkers windowsIOWorkers,
	state terminalState,
	timeout time.Duration,
	rootWait windowsRootWait,
	writerDone <-chan error,
	stdoutDone <-chan readerResult,
	stderrDone <-chan readerResult,
) (waitResult, StopAction, error) {
	waited := waitResult{exitCode: -1}
	var cleanupErrors []error

	action := StopActionNone
	jobTerminationStarted := false
	forced := state.kind == ErrorOutputLimit ||
		state.kind == ErrorCanceled ||
		state.kind == ErrorTimeout
	if forced {
		action = StopActionTerminateJob
		if err := process.api.terminateJobObject(
			process.job.handle,
			windowsTerminateExitCode,
		); err != nil {
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("terminate provider Job Object: %w", err),
			)
		} else {
			jobTerminationStarted = true
		}
	}

	hardContext, hardCancel := context.WithTimeout(
		context.Background(),
		timeout,
	)
	defer hardCancel()
	primaryDuration := timeout - min(timeout/4, 25*time.Millisecond)
	if primaryDuration <= 0 {
		primaryDuration = timeout
	}
	primaryContext, primaryCancel := context.WithTimeout(
		context.Background(),
		primaryDuration,
	)
	defer primaryCancel()

	terminatedForDescendants, jobErr := finishWindowsJob(
		primaryContext,
		process,
		!forced,
	)
	if terminatedForDescendants {
		action = StopActionTerminateJob
		jobTerminationStarted = true
	}
	if jobErr != nil {
		cleanupErrors = append(cleanupErrors, jobErr)
		if !process.job.isClosed() {
			action = StopActionTerminateJob
			if !jobTerminationStarted {
				if err := process.api.terminateJobObject(
					process.job.handle,
					windowsTerminateExitCode,
				); err != nil {
					cleanupErrors = append(
						cleanupErrors,
						fmt.Errorf(
							"terminate provider Job after cleanup failure: %w",
							err,
						),
					)
				} else {
					jobTerminationStarted = true
				}
			}
			ioWorkers.cancel(process.api)
			process.deferJobContainment(jobTerminationStarted)
		}
	}

	completion := windowsCompletionState{wait: waited}
	completion, completionErr := joinWindowsCompletions(
		hardContext,
		primaryContext.Done(),
		process,
		ioWorkers,
		rootWait,
		writerDone,
		stdoutDone,
		stderrDone,
		completion,
	)
	waited = completion.wait
	if completionErr != nil {
		cleanupErrors = append(cleanupErrors, completionErr)
	}
	return waited, action, errors.Join(cleanupErrors...)
}

func finishWindowsJob(
	ctx context.Context,
	process *windowsProcess,
	terminateRemaining bool,
) (bool, error) {
	terminated := false
	for {
		accounting, err := process.api.queryJobAccounting(process.job.handle)
		if err != nil {
			return terminated, fmt.Errorf(
				"query provider Job accounting: %w",
				err,
			)
		}
		if accounting.TotalProcesses > 0 &&
			accounting.ActiveProcesses == 0 {
			if err := process.closeVerifiedJob(accounting); err != nil {
				return terminated, err
			}
			return terminated, nil
		}
		if terminateRemaining &&
			!terminated &&
			accounting.ActiveProcesses > 0 {
			if err := process.api.terminateJobObject(
				process.job.handle,
				windowsTerminateExitCode,
			); err != nil {
				return false, fmt.Errorf(
					"terminate remaining provider descendants: %w",
					err,
				)
			}
			terminated = true
		}
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return terminated, fmt.Errorf(
				"wait for provider Job to become empty: %w",
				ctx.Err(),
			)
		case <-timer.C:
		}
	}
}

func (p *windowsProcess) closeVerifiedJob(
	accounting windowsJobAccounting,
) error {
	if accounting.TotalProcesses == 0 || accounting.ActiveProcesses != 0 {
		return fmt.Errorf(
			"refuse to close provider Job without zero-active accounting: %+v",
			accounting,
		)
	}
	if p.beforeJobClose != nil {
		p.beforeJobClose(windowsJobCloseView{
			job:        p.job.handle,
			accounting: accounting,
		})
	}
	return p.job.close()
}

func waitWindowsJobEmpty(
	ctx context.Context,
	api windowsAPI,
	job windows.Handle,
) (windowsJobAccounting, error) {
	for {
		accounting, err := api.queryJobAccounting(job)
		if err != nil {
			return windowsJobAccounting{}, fmt.Errorf(
				"query provider Job accounting: %w",
				err,
			)
		}
		if accounting.TotalProcesses > 0 &&
			accounting.ActiveProcesses == 0 {
			return accounting, nil
		}
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return windowsJobAccounting{}, ctx.Err()
		case <-timer.C:
		}
	}
}

type windowsCompletionState struct {
	wait       waitResult
	waitSeen   bool
	writerSeen bool
	stdoutSeen bool
	stderrSeen bool
	errs       []error
}

func joinWindowsCompletions(
	ctx context.Context,
	force <-chan struct{},
	process *windowsProcess,
	ioWorkers windowsIOWorkers,
	rootWait windowsRootWait,
	writerDone <-chan error,
	stdoutDone <-chan readerResult,
	stderrDone <-chan readerResult,
	state windowsCompletionState,
) (windowsCompletionState, error) {
	forceChannel := force
	for !state.complete() {
		waitChannel := rootWait.done
		writerChannel := writerDone
		stdoutChannel := stdoutDone
		stderrChannel := stderrDone
		if state.waitSeen {
			waitChannel = nil
		}
		if state.writerSeen {
			writerChannel = nil
		}
		if state.stdoutSeen {
			stdoutChannel = nil
		}
		if state.stderrSeen {
			stderrChannel = nil
		}
		select {
		case waited := <-waitChannel:
			state = acceptWindowsRootWait(process, state, waited)
		case writerErr := <-writerChannel:
			state.writerSeen = true
			if writerErr != nil {
				state.errs = append(state.errs, fmt.Errorf(
					"write provider stdin: %w",
					writerErr,
				))
			}
		case result := <-stdoutChannel:
			state.stdoutSeen = true
			if result.err != nil {
				state.errs = append(state.errs, fmt.Errorf(
					"drain provider %s: %w",
					result.stream,
					result.err,
				))
			}
		case result := <-stderrChannel:
			state.stderrSeen = true
			if result.err != nil {
				state.errs = append(state.errs, fmt.Errorf(
					"drain provider %s: %w",
					result.stream,
					result.err,
				))
			}
		case <-forceChannel:
			forceChannel = nil
			ioWorkers.cancel(process.api)
			state.errs = append(
				state.errs,
				errors.New("forced Windows pipe I/O cancellation during cleanup"),
			)
		case <-ctx.Done():
			ioWorkers.cancel(process.api)
			process.deferIOCompletions(
				writerDone,
				stdoutDone,
				stderrDone,
				state,
			)
			if !state.waitSeen {
				state = settleWindowsRootWaitAtDeadline(
					process,
					rootWait.done,
					state,
				)
			}
			return state, errors.Join(
				append(state.errs, ctx.Err())...,
			)
		}
	}
	return state, errors.Join(state.errs...)
}

func acceptWindowsRootWait(
	process *windowsProcess,
	state windowsCompletionState,
	waited waitResult,
) windowsCompletionState {
	state.wait = waited
	state.waitSeen = true
	if waited.err != nil {
		state.errs = append(state.errs, waited.err)
		if !process.process.isClosed() {
			process.deferStartProcessWait()
		}
	}
	return state
}

func settleWindowsRootWaitAtDeadline(
	process *windowsProcess,
	rootDone <-chan waitResult,
	state windowsCompletionState,
) windowsCompletionState {
	select {
	case waited := <-rootDone:
		return acceptWindowsRootWait(process, state, waited)
	default:
		process.deferPendingProcessWait(rootDone)
		return state
	}
}

func (p *windowsProcess) deferIOCompletions(
	writerDone <-chan error,
	stdoutDone <-chan readerResult,
	stderrDone <-chan readerResult,
	state windowsCompletionState,
) {
	if state.writerSeen && state.stdoutSeen && state.stderrSeen {
		return
	}
	p.deferIO.Do(func() {
		done := make(chan waitResult, 1)
		go func() {
			var completionErrors []error
			if !state.writerSeen {
				if err := <-writerDone; err != nil {
					completionErrors = append(completionErrors, fmt.Errorf(
						"deferred provider stdin write: %w",
						err,
					))
				}
			}
			if !state.stdoutSeen {
				result := <-stdoutDone
				if result.err != nil {
					completionErrors = append(completionErrors, fmt.Errorf(
						"deferred provider %s drain: %w",
						result.stream,
						result.err,
					))
				}
			}
			if !state.stderrSeen {
				result := <-stderrDone
				if result.err != nil {
					completionErrors = append(completionErrors, fmt.Errorf(
						"deferred provider %s drain: %w",
						result.stream,
						result.err,
					))
				}
			}
			done <- waitResult{
				err:      errors.Join(completionErrors...),
				exitCode: -1,
			}
		}()
		p.completions.deferWait(int(p.pid), done)
	})
}

func (s windowsCompletionState) complete() bool {
	return s.waitSeen &&
		s.writerSeen &&
		s.stdoutSeen &&
		s.stderrSeen
}

func windowsDurationMilliseconds(duration time.Duration) uint32 {
	if duration <= 0 {
		return 0
	}
	milliseconds := (duration + time.Millisecond - 1) / time.Millisecond
	if milliseconds >= time.Duration(windows.INFINITE) {
		return windows.INFINITE - 1
	}
	return uint32(milliseconds)
}

func windowsContextMilliseconds(ctx context.Context) uint32 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return windows.INFINITE
	}
	return windowsDurationMilliseconds(time.Until(deadline))
}
