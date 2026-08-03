//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var (
	errExecutionTimedOut = errors.New("provider execution timed out")
	errOutputExceeded    = errors.New("provider output limit exceeded")
)

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

// runnerEventView is a deterministic observation seam for integration tests.
// It never consumes the process events that decide the terminal state.
type runnerEventView struct {
	waitReady    <-chan struct{}
	timeoutReady <-chan struct{}
	overflowed   func() bool
}

type runnerHooks struct {
	beforeCommit       func(runnerEventView)
	afterCommit        func(terminalState, runnerEventView)
	beforeWait         func()
	beforeGroupSignal  func(syscall.Signal)
	processGroupLookup func(int) (int, error)
	waitRelease        <-chan struct{}
}

type readerResult struct {
	stream string
	err    error
}

type processCompletionChannels struct {
	wait   <-chan waitResult
	writer <-chan error
	stdout <-chan readerResult
	stderr <-chan readerResult
}

type processCompletionState struct {
	wait       waitResult
	waitSeen   bool
	writerSeen bool
	stdoutSeen bool
	stderrSeen bool
	errs       []error
}

type unixPipes struct {
	stdinChild   *os.File
	stdinParent  *os.File
	stdoutChild  *os.File
	stdoutParent *os.File
	stderrChild  *os.File
	stderrParent *os.File
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
	return runUnixOwned(
		ctx,
		root,
		runtime,
		spec,
		limits,
		completions,
		hooks,
	)
}

func runUnixOwned(
	ctx context.Context,
	root *Root,
	runtime Runtime,
	spec CommandSpec,
	limits Limits,
	completions *completionOwner,
	hooks runnerHooks,
) (runnerResult, error) {
	result := Result{
		ExitCode:   -1,
		StopReason: StopReasonNormalExit,
		StopAction: StopActionNone,
	}
	if err := validateUnixCommand(runtime, spec); err != nil {
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

	pipes, err := openUnixPipes()
	if err != nil {
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  fmt.Errorf("open provider pipes: %w", err),
		}
	}

	// The executable/argv passed here were resolved and validated by doctor.
	// The supervisor deliberately owns process-group cancellation.
	//nolint:gosec,noctx
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append([]string{}, spec.Env...)
	cmd.Stdin = pipes.stdinChild
	cmd.Stdout = pipes.stdoutChild
	cmd.Stderr = pipes.stderrChild
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := root.validateRuntimePath(runtime); err != nil {
		_ = pipes.closeAll()
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err:  fmt.Errorf("validate runtime immediately before launch: %w", err),
		}
	}
	if err := cmd.Start(); err != nil {
		_ = pipes.closeAll()
		return runnerResult{result: result}, &RunError{
			Kind: ErrorStart,
			Err: fmt.Errorf(
				"start provider process: %w",
				markExecutableUnavailable(err),
			),
		}
	}

	pid := cmd.Process.Pid
	processGroupLookup := unix.Getpgid
	if hooks.processGroupLookup != nil {
		processGroupLookup = hooks.processGroupLookup
	}
	pgid, pgidErr := expectedUnixProcessGroup(pid, processGroupLookup)
	if pgidErr != nil {
		commandWait := startCommandWait(
			cmd,
			hooks.waitRelease,
			hooks.beforeWait,
		)
		cleanupErr := boundedUnverifiedStartCleanup(
			limits.Cleanup,
			pipes,
			pid,
			commandWait.done,
			cmd.Process.Kill,
			completions,
		)
		return runnerResult{result: result, pgid: pgid}, &RunError{
			Kind: ErrorCleanup,
			Err: errors.Join(
				fmt.Errorf(
					"verify provider process group: %w",
					pgidErr,
				),
				cleanupErr,
			),
		}
	}
	rootExit := observeRootExit(pid)

	_ = pipes.closeChildEnds()
	overflow := make(chan struct{}, 1)
	stdoutCapture := newCapture(limits.StdoutBytes, overflow)
	stderrCapture := newCapture(limits.StderrBytes, overflow)
	writerDone := make(chan error, 1)
	stdoutDone := make(chan readerResult, 1)
	stderrDone := make(chan readerResult, 1)
	stdinParent := pipes.stdinParent

	go func() {
		writerDone <- writeStdinAndClose(stdinParent, spec.Stdin)
	}()
	go copyPipe("stdout", pipes.stdoutParent, stdoutCapture, stdoutDone)
	go copyPipe("stderr", pipes.stderrParent, stderrCapture, stderrDone)

	if ctx == nil {
		ctx = context.Background()
	}
	executionExpired := make(chan struct{})
	executionTimer := time.AfterFunc(limits.Execution, func() {
		close(executionExpired)
	})
	defer executionTimer.Stop()
	events := runnerEventView{
		waitReady:    rootExit.ready,
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
			state.commit(terminalObservation{overflowed: true})
		case <-ctx.Done():
			state.commit(terminalObservation{
				overflowed: stdoutCapture.Overflowed() ||
					stderrCapture.Overflowed(),
				canceled:  true,
				cancelErr: ctx.Err(),
			})
		case <-executionExpired:
			state.commit(terminalObservation{
				overflowed: stdoutCapture.Overflowed() ||
					stderrCapture.Overflowed(),
				timedOut: true,
			})
		case observationErr := <-rootExit.done:
			if observationErr != nil {
				state = terminalState{
					committed: true,
					kind:      ErrorCleanup,
					reason:    StopReasonCleanupFailure,
					cause:     observationErr,
				}
				continue
			}
			observation := terminalObservation{
				overflowed: stdoutCapture.Overflowed() ||
					stderrCapture.Overflowed(),
				waited: true,
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
			state.commit(observation)
		}
	}
	if hooks.afterCommit != nil {
		hooks.afterCommit(state, events)
	}

	waited, action, cleanupErr := terminateUnix(
		cmd,
		rootExit,
		pid,
		pgid,
		pipes,
		limits,
		writerDone,
		stdoutDone,
		stderrDone,
		completions,
		hooks,
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
		return runnerResult{result: result, pgid: pgid}, &RunError{
			Kind: ErrorCleanup,
			Err:  errors.Join(terminalErr, cleanupErr),
		}
	}
	return runnerResult{result: result, pgid: pgid}, terminalErr
}

func expectedUnixProcessGroup(
	pid int,
	lookup func(int) (int, error),
) (int, error) {
	if pid <= 0 {
		return pid, errors.New("provider root PID is invalid")
	}
	if lookup == nil {
		return pid, errors.New("provider process-group lookup is unavailable")
	}
	observed, err := lookup(pid)
	switch {
	case err == nil && observed == pid:
		return pid, nil
	case errors.Is(err, unix.ESRCH):
		return pid, nil
	case err != nil:
		return pid, err
	default:
		return pid, fmt.Errorf(
			"process group %d does not match root PID %d",
			observed,
			pid,
		)
	}
}

func validateUnixCommand(runtime Runtime, spec CommandSpec) error {
	if !filepath.IsAbs(spec.Executable) ||
		strings.IndexByte(spec.Executable, 0) >= 0 {
		return errors.New("provider executable must be an absolute safe path")
	}
	if spec.Dir != runtime.Dir || !filepath.IsAbs(spec.Dir) {
		return errors.New("provider directory must exactly match runtime")
	}
	for _, arg := range spec.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return errors.New("provider argument contains NUL")
		}
	}
	seen := make(map[string]struct{}, len(spec.Env))
	for _, entry := range spec.Env {
		if strings.IndexByte(entry, 0) >= 0 {
			return errors.New("provider environment contains NUL")
		}
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 {
			return errors.New("provider environment entry is malformed")
		}
		name := entry[:separator]
		if !validEnvironmentName(name) {
			return errors.New("provider environment name is malformed")
		}
		if _, exists := seen[name]; exists {
			return errors.New("provider environment contains a duplicate name")
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validEnvironmentName(name string) bool {
	for i, value := range []byte(name) {
		if (value >= 'a' && value <= 'z') ||
			(value >= 'A' && value <= 'Z') ||
			value == '_' ||
			(i > 0 && value >= '0' && value <= '9') {
			continue
		}
		return false
	}
	return name != ""
}

func openUnixPipes() (*unixPipes, error) {
	pipes := &unixPipes{}
	var err error
	if pipes.stdinChild, pipes.stdinParent, err = os.Pipe(); err != nil {
		return nil, err
	}
	if pipes.stdoutParent, pipes.stdoutChild, err = os.Pipe(); err != nil {
		_ = pipes.closeAll()
		return nil, err
	}
	if pipes.stderrParent, pipes.stderrChild, err = os.Pipe(); err != nil {
		_ = pipes.closeAll()
		return nil, err
	}
	return pipes, nil
}

func (p *unixPipes) closeChildEnds() error {
	err := errors.Join(
		closeUnixFile(p.stdinChild),
		closeUnixFile(p.stdoutChild),
		closeUnixFile(p.stderrChild),
	)
	p.stdinChild = nil
	p.stdoutChild = nil
	p.stderrChild = nil
	return err
}

func (p *unixPipes) closeAll() error {
	if p == nil {
		return nil
	}
	err := errors.Join(
		closeUnixFile(p.stdinChild),
		closeUnixFile(p.stdinParent),
		closeUnixFile(p.stdoutChild),
		closeUnixFile(p.stdoutParent),
		closeUnixFile(p.stderrChild),
		closeUnixFile(p.stderrParent),
	)
	p.stdinChild = nil
	p.stdinParent = nil
	p.stdoutChild = nil
	p.stdoutParent = nil
	p.stderrChild = nil
	p.stderrParent = nil
	return err
}

func closeUnixFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

type commandWait struct {
	done  <-chan waitResult
	ready <-chan struct{}
}

func startCommandWait(
	cmd *exec.Cmd,
	release <-chan struct{},
	beforeWait func(),
) commandWait {
	done := make(chan waitResult, 1)
	ready := make(chan struct{})
	if beforeWait != nil {
		beforeWait()
	}
	go func() {
		waitErr := cmd.Wait()
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		result := waitResult{
			err:      waitErr,
			exitCode: exitCode,
		}
		if release != nil {
			<-release
		}
		done <- result
		close(ready)
	}()
	return commandWait{done: done, ready: ready}
}

func boundedUnverifiedStartCleanup(
	timeout time.Duration,
	pipes *unixPipes,
	pid int,
	waitDone <-chan waitResult,
	kill func() error,
	completions *completionOwner,
) error {
	closeErr := pipes.closeAll()
	var killErr error
	if kill != nil {
		killErr = kill()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case waited := <-waitDone:
		return errors.Join(
			closeErr,
			killErr,
			unexpectedWaitError(waited.err),
		)
	case <-ctx.Done():
		select {
		case waited := <-waitDone:
			return errors.Join(
				closeErr,
				killErr,
				unexpectedWaitError(waited.err),
			)
		default:
		}
		completions.deferWait(pid, waitDone)
		return errors.Join(closeErr, killErr, ctx.Err())
	}
}

func writeStdinAndClose(file *os.File, data []byte) error {
	defer func() {
		_ = file.Close()
	}()
	for len(data) != 0 {
		count, err := file.Write(data)
		if err != nil {
			return err
		}
		if count <= 0 || count > len(data) {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func copyPipe(
	stream string,
	file *os.File,
	dst io.Writer,
	done chan<- readerResult,
) {
	_, err := io.Copy(dst, file)
	done <- readerResult{stream: stream, err: err}
}

func terminateUnix(
	cmd *exec.Cmd,
	rootExit *rootExitObserver,
	pid int,
	pgid int,
	pipes *unixPipes,
	limits Limits,
	writerDone <-chan error,
	stdoutDone <-chan readerResult,
	stderrDone <-chan readerResult,
	completions *completionOwner,
	hooks runnerHooks,
) (waitResult, StopAction, error) {
	var cleanupErrors []error
	_ = closeUnixFile(pipes.stdinParent)
	pipes.stdinParent = nil

	action := StopActionNone
	termSent, termErr := signalUnixProcessGroup(
		pgid,
		unix.SIGTERM,
		hooks,
	)
	if termErr != nil {
		cleanupErrors = append(cleanupErrors, termErr)
	}
	if termSent || termErr != nil {
		if termSent {
			action = StopActionTERM
		}
		gone, graceErr := waitForProcessGroupGone(
			context.Background(),
			pgid,
			limits.TermGrace,
			true,
		)
		if graceErr != nil {
			cleanupErrors = append(cleanupErrors, graceErr)
		}
		if !gone {
			killSent, killErr := signalUnixProcessGroup(
				pgid,
				unix.SIGKILL,
				hooks,
			)
			if killErr != nil {
				cleanupErrors = append(cleanupErrors, killErr)
			} else if killSent {
				action = StopActionKILL
			}
		}
	}

	if err := rootExit.Close(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	commandWait := startCommandWait(
		cmd,
		hooks.waitRelease,
		hooks.beforeWait,
	)
	cleanupCtx, cancel := context.WithTimeout(
		context.Background(),
		limits.Cleanup,
	)
	defer cancel()
	completionState, completionErr := joinProcessCompletions(
		cleanupCtx,
		pipes,
		completions,
		pid,
		processCompletionChannels{
			wait:   commandWait.done,
			writer: writerDone,
			stdout: stdoutDone,
			stderr: stderrDone,
		},
		processCompletionState{
			wait: waitResult{exitCode: -1},
		},
	)
	waited := completionState.wait
	if completionErr != nil {
		cleanupErrors = append(cleanupErrors, completionErr)
	}
	if gone, err := waitForProcessGroupGone(
		cleanupCtx,
		pgid,
		remainingCleanup(cleanupCtx),
		false,
	); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	} else if !gone {
		cleanupErrors = append(
			cleanupErrors,
			errors.New("provider process group remains after cleanup"),
		)
	}
	_ = closeUnixFile(pipes.stdoutParent)
	_ = closeUnixFile(pipes.stderrParent)
	pipes.stdoutParent = nil
	pipes.stderrParent = nil

	return waited, action, errors.Join(cleanupErrors...)
}

func signalUnixProcessGroup(
	pgid int,
	signal syscall.Signal,
	hooks runnerHooks,
) (bool, error) {
	if pgid <= 0 {
		return false, errors.New("invalid provider process group")
	}
	if hooks.beforeGroupSignal != nil {
		hooks.beforeGroupSignal(signal)
	}
	err := unix.Kill(-pgid, signal)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.ESRCH),
		retainedZombieSignalErrorMeansAbsent(err):
		return false, nil
	default:
		return false, fmt.Errorf(
			"signal provider process group %d with %s: %w",
			pgid,
			signal,
			err,
		)
	}
}

func joinProcessCompletions(
	ctx context.Context,
	pipes *unixPipes,
	owner *completionOwner,
	pid int,
	channels processCompletionChannels,
	state processCompletionState,
) (processCompletionState, error) {
	for !state.complete() {
		waitDone := channels.wait
		writerDone := channels.writer
		stdoutDone := channels.stdout
		stderrDone := channels.stderr
		if state.waitSeen {
			waitDone = nil
		}
		if state.writerSeen {
			writerDone = nil
		}
		if state.stdoutSeen {
			stdoutDone = nil
		}
		if state.stderrSeen {
			stderrDone = nil
		}
		select {
		case waited := <-waitDone:
			state.acceptWait(waited)
		case <-writerDone:
			state.writerSeen = true
		case result := <-stdoutDone:
			state.acceptReader(result, true)
		case result := <-stderrDone:
			state.acceptReader(result, false)
		case <-ctx.Done():
			closeErr := pipes.closeParentEnds()
			state.joinPipeOwners(channels)
			if !state.waitSeen {
				select {
				case waited := <-channels.wait:
					state.acceptWait(waited)
				default:
					owner.deferWait(pid, channels.wait)
				}
			}
			return state, errors.Join(
				append(state.errs, closeErr, ctx.Err())...,
			)
		}
	}
	return state, errors.Join(state.errs...)
}

func (s *processCompletionState) acceptWait(waited waitResult) {
	s.wait = waited
	s.waitSeen = true
	if err := unexpectedWaitError(waited.err); err != nil {
		s.errs = append(s.errs, err)
	}
}

func (s *processCompletionState) acceptReader(
	result readerResult,
	stdout bool,
) {
	if stdout {
		s.stdoutSeen = true
	} else {
		s.stderrSeen = true
	}
	if result.err != nil {
		s.errs = append(s.errs, fmt.Errorf(
			"drain provider %s: %w",
			result.stream,
			result.err,
		))
	}
}

func (s *processCompletionState) complete() bool {
	return s.waitSeen &&
		s.writerSeen &&
		s.stdoutSeen &&
		s.stderrSeen
}

func (s *processCompletionState) joinPipeOwners(
	channels processCompletionChannels,
) {
	if !s.writerSeen {
		<-channels.writer
		s.writerSeen = true
	}
	if !s.stdoutSeen {
		s.acceptReader(<-channels.stdout, true)
	}
	if !s.stderrSeen {
		s.acceptReader(<-channels.stderr, false)
	}
}

func (p *unixPipes) closeParentEnds() error {
	err := errors.Join(
		closeUnixFile(p.stdinParent),
		closeUnixFile(p.stdoutParent),
		closeUnixFile(p.stderrParent),
	)
	p.stdinParent = nil
	p.stdoutParent = nil
	p.stderrParent = nil
	return err
}

func processGroupExists(
	pgid int,
	retainedLeader bool,
) (bool, error) {
	if pgid <= 0 {
		return false, errors.New("invalid provider process group")
	}
	err := unix.Kill(-pgid, 0)
	switch {
	case err == nil:
		return true, nil
	case retainedZombieProbeErrorMeansAbsent(err, retainedLeader):
		return false, nil
	case errors.Is(err, unix.EPERM):
		return true, nil
	case errors.Is(err, unix.ESRCH):
		return false, nil
	default:
		return false, fmt.Errorf(
			"inspect provider process group %d: %w",
			pgid,
			err,
		)
	}
}

func waitForProcessGroupGone(
	ctx context.Context,
	pgid int,
	wait time.Duration,
	retainedLeader bool,
) (bool, error) {
	gone, err := processGroupExists(pgid, retainedLeader)
	if err != nil || !gone {
		return !gone, err
	}
	if wait <= 0 {
		return false, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	ticker := time.NewTicker(min(wait, 2*time.Millisecond))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			gone, err := processGroupExists(pgid, retainedLeader)
			return !gone, err
		case <-ticker.C:
			gone, err := processGroupExists(pgid, retainedLeader)
			if err != nil || !gone {
				return !gone, err
			}
		}
	}
}

func remainingCleanup(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}
