package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxSupervisorDuration = 24 * time.Hour
	maxSupervisorStdout   = int64(64 << 20)
	maxSupervisorStderr   = int64(16 << 20)
)

type runnerResult struct {
	result Result
	pgid   int
}

type runnerFunc func(
	context.Context,
	*Root,
	Runtime,
	CommandSpec,
	Limits,
) (runnerResult, error)

// Supervisor owns materialization, execution containment, and bounded cleanup
// for one locked runtime root.
type Supervisor struct {
	root             *Root
	limits           Limits
	runner           runnerFunc
	completions      *completionOwner
	lifecycle        *supervisorLifecycle
	hooks            runnerHooks
	identity         uint64
	leaseNext        atomic.Uint64
	selfTestPrepared func(Runtime)
}

var (
	selfTestSequence   atomic.Uint64
	supervisorSequence atomic.Uint64
)

var errSupervisorShuttingDown = errors.New(
	"process supervisor is shutting down",
)

var errRuntimeLeaseInvalid = errors.New(
	"runtime supervisor lease is invalid or already consumed",
)

type supervisorLifecycle struct {
	mu           sync.Mutex
	shuttingDown bool
	active       int
	changed      chan struct{}
}

func newSupervisorLifecycle() *supervisorLifecycle {
	return &supervisorLifecycle{changed: make(chan struct{})}
}

func (l *supervisorLifecycle) begin() (func(), error) {
	if l == nil {
		return nil, errSupervisorShuttingDown
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.shuttingDown {
		return nil, errSupervisorShuttingDown
	}
	l.active++
	return l.end, nil
}

func (l *supervisorLifecycle) end() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
		l.notifyLocked()
	}
	l.mu.Unlock()
}

func (l *supervisorLifecycle) shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if !l.shuttingDown {
		l.shuttingDown = true
		l.notifyLocked()
	}
	for l.active != 0 {
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		l.mu.Lock()
	}
	l.mu.Unlock()
	return nil
}

func (l *supervisorLifecycle) notifyLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

type runtimeLeaseState uint8

const (
	runtimeLeasePrepared runtimeLeaseState = iota
	runtimeLeaseConsuming
	runtimeLeaseConsumed
)

type runtimeLease struct {
	mu                sync.Mutex
	supervisor        *Supervisor
	supervisorID      uint64
	generation        uint64
	runtimeID         string
	runtimeDir        string
	runtimeOwner      *Root
	runtimeRecord     *runtimeRecord
	runtimeGeneration uint64
	state             runtimeLeaseState
	release           func()
}

func (l *runtimeLease) finish() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.state != runtimeLeaseConsuming {
		l.mu.Unlock()
		return
	}
	l.state = runtimeLeaseConsumed
	release := l.release
	l.release = nil
	l.mu.Unlock()
	if release != nil {
		release()
	}
}

type ownedWait struct {
	pid        int
	completion <-chan waitResult
	result     waitResult
	completed  bool
}

type waitResult struct {
	err      error
	exitCode int
}

type completionOwner struct {
	mu      sync.Mutex
	next    uint64
	pending map[uint64]*ownedWait
	changed chan struct{}
}

func newCompletionOwner() *completionOwner {
	return &completionOwner{
		pending: make(map[uint64]*ownedWait),
		changed: make(chan struct{}),
	}
}

func (o *completionOwner) deferWait(
	pid int,
	completion <-chan waitResult,
) {
	if o == nil || completion == nil {
		return
	}
	owned := &ownedWait{
		pid:        pid,
		completion: completion,
		result:     waitResult{exitCode: -1},
	}
	o.mu.Lock()
	o.next++
	id := o.next
	o.pending[id] = owned
	o.notifyLocked()
	o.mu.Unlock()
	go func() {
		result := <-owned.completion
		o.mu.Lock()
		owned.result = result
		owned.completed = true
		o.notifyLocked()
		o.mu.Unlock()
	}()
}

func (o *completionOwner) drain(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	var reapErrors []error
	for len(o.pending) != 0 {
		for id, owned := range o.pending {
			if !owned.completed {
				continue
			}
			if err := unexpectedWaitError(owned.result.err); err != nil {
				reapErrors = append(reapErrors, fmt.Errorf(
					"reap provider process %d: %w",
					owned.pid,
					err,
				))
			}
			delete(o.pending, id)
		}
		if len(o.pending) == 0 {
			break
		}
		changed := o.changed
		o.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return errors.Join(
				append(reapErrors, ctx.Err())...,
			)
		}
		o.mu.Lock()
	}
	o.mu.Unlock()
	return errors.Join(reapErrors...)
}

func (o *completionOwner) count() int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending)
}

func (o *completionOwner) completedCount() int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, owned := range o.pending {
		if owned.completed {
			count++
		}
	}
	return count
}

func (o *completionOwner) notifyLocked() {
	close(o.changed)
	o.changed = make(chan struct{})
}

func unexpectedWaitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}

// NewSupervisor binds process lifecycle limits to a locked runtime root.
func NewSupervisor(root *Root, limits Limits) (*Supervisor, error) {
	if root == nil {
		return nil, errors.New("process supervisor requires a runtime root")
	}
	if !validSupervisorDuration(limits.Execution) {
		return nil, errors.New("execution limit is invalid")
	}
	if !validSupervisorDuration(limits.TermGrace) {
		return nil, errors.New("termination grace is invalid")
	}
	if !validSupervisorDuration(limits.Cleanup) {
		return nil, errors.New("cleanup limit is invalid")
	}
	if limits.StdoutBytes <= 0 ||
		limits.StdoutBytes > maxSupervisorStdout {
		return nil, errors.New("stdout limit is invalid")
	}
	if limits.StderrBytes <= 0 ||
		limits.StderrBytes > maxSupervisorStderr {
		return nil, errors.New("stderr limit is invalid")
	}
	release, err := root.beginOperation()
	if err != nil {
		return nil, fmt.Errorf("validate runtime root: %w", err)
	}
	release()
	completions := newCompletionOwner()
	supervisor := &Supervisor{
		root:        root,
		limits:      limits,
		completions: completions,
		lifecycle:   newSupervisorLifecycle(),
		identity:    nextSupervisorIdentity(),
	}
	supervisor.runner = func(
		ctx context.Context,
		root *Root,
		runtime Runtime,
		spec CommandSpec,
		limits Limits,
	) (runnerResult, error) {
		return runPlatform(
			ctx,
			root,
			runtime,
			spec,
			limits,
			completions,
			supervisor.hooks,
		)
	}
	return supervisor, nil
}

func nextSupervisorIdentity() uint64 {
	identity := supervisorSequence.Add(1)
	if identity == 0 {
		identity = supervisorSequence.Add(1)
	}
	return identity
}

func validSupervisorDuration(value time.Duration) bool {
	return value > 0 && value <= maxSupervisorDuration
}

// Prepare creates one request-local runtime.
func (s *Supervisor) Prepare(id string) (Runtime, error) {
	if s == nil || s.root == nil {
		return Runtime{}, errors.New("process supervisor is unavailable")
	}
	release, err := s.lifecycle.begin()
	if err != nil {
		return Runtime{}, err
	}
	runtime, err := s.root.Prepare(id)
	if err != nil {
		release()
		return Runtime{}, err
	}
	generation := s.leaseNext.Add(1)
	if generation == 0 {
		generation = s.leaseNext.Add(1)
	}
	lease := &runtimeLease{
		supervisor:        s,
		supervisorID:      s.identity,
		generation:        generation,
		runtimeID:         runtime.ID,
		runtimeDir:        runtime.Dir,
		runtimeOwner:      runtime.owner,
		runtimeRecord:     runtime.record,
		runtimeGeneration: runtime.generation,
		state:             runtimeLeasePrepared,
		release:           release,
	}
	runtime.supervisorID = s.identity
	runtime.leaseGeneration = generation
	runtime.lease = lease
	return runtime, nil
}

// Discard performs bounded independent cleanup for a runtime whose command
// could not be built.
func (s *Supervisor) Discard(
	ctx context.Context,
	runtime Runtime,
) error {
	if s == nil || s.root == nil {
		return &RunError{
			Kind: ErrorCleanup,
			Err:  errors.New("process supervisor is unavailable"),
		}
	}
	lease, err := s.consumeRuntimeLease(runtime)
	if err != nil {
		return &RunError{Kind: ErrorCleanup, Err: err}
	}
	defer lease.finish()
	return s.cleanupRuntime(ctx, runtime)
}

// Execute materializes request files, runs the command under platform
// containment, synchronously drains retained safety ownership, and then always
// attempts independent bounded filesystem cleanup before releasing its lease.
func (s *Supervisor) Execute(
	ctx context.Context,
	runtime Runtime,
	spec CommandSpec,
) (Result, error) {
	result := Result{
		ExitCode:   -1,
		StopReason: StopReasonNormalExit,
		StopAction: StopActionNone,
	}
	if s == nil || s.root == nil {
		return result, &RunError{
			Kind: ErrorStart,
			Err:  errors.New("process supervisor is unavailable"),
		}
	}
	lease, leaseErr := s.consumeRuntimeLease(runtime)
	if leaseErr != nil {
		return result, &RunError{
			Kind: ErrorStart,
			Err:  leaseErr,
		}
	}
	defer lease.finish()
	var executionErr error
	switch {
	case s.runner == nil:
		executionErr = &RunError{
			Kind: ErrorStart,
			Err:  errors.New("process supervisor is unavailable"),
		}
	case spec.Dir != runtime.Dir:
		executionErr = &RunError{
			Kind: ErrorStart,
			Err:  errors.New("command directory does not match runtime"),
		}
	case s.validateRuntimeOwnership(runtime) != nil:
		executionErr = &RunError{
			Kind: ErrorStart,
			Err:  errors.New("runtime does not belong to supervisor root"),
		}
	case contextError(ctx) != nil:
		result.StopReason = StopReasonCallerCancellation
		executionErr = &RunError{
			Kind: ErrorCanceled,
			Err:  contextError(ctx),
		}
	default:
		if err := s.root.Materialize(runtime, spec.Files); err != nil {
			executionErr = &RunError{
				Kind: ErrorStart,
				Err:  fmt.Errorf("materialize command runtime: %w", err),
			}
			break
		}
		outcome, err := s.runner(ctx, s.root, runtime, spec, s.limits)
		result = outcome.result
		executionErr = err
	}

	ownershipErr := s.completions.drain(context.Background())
	cleanupErr := s.cleanupRuntime(ctx, runtime)
	if ownershipErr != nil || cleanupErr != nil {
		result.StopReason = StopReasonCleanupFailure
		return result, &RunError{
			Kind: ErrorCleanup,
			Err: errors.Join(
				executionErr,
				ownershipErr,
				cleanupErr,
			),
		}
	}
	return result, executionErr
}

// Shutdown permanently rejects new supervisor work and waits for active
// operations and every retained process, Job, and stream completion to finish.
// Callers must complete Shutdown before closing the Root. A context timeout
// does not discard ownership; a later Shutdown call continues the same drain.
func (s *Supervisor) Shutdown(ctx context.Context) error {
	if s == nil || s.root == nil ||
		s.lifecycle == nil || s.completions == nil {
		return &RunError{
			Kind: ErrorCleanup,
			Err:  errors.New("process supervisor is unavailable"),
		}
	}
	if err := s.lifecycle.shutdown(ctx); err != nil {
		return &RunError{Kind: ErrorCleanup, Err: err}
	}
	if err := s.completions.drain(ctx); err != nil {
		return &RunError{Kind: ErrorCleanup, Err: err}
	}
	return nil
}

// SelfTest exercises the ordinary containment path with the same gateway
// executable and fixed hidden argv.
func (s *Supervisor) SelfTest(
	ctx context.Context,
	gatewayExecutable string,
) error {
	if !filepath.IsAbs(gatewayExecutable) {
		return &RunError{
			Kind: ErrorStart,
			Err:  errors.New("self-test executable must be absolute"),
		}
	}
	id := fmt.Sprintf(
		"selftest-%d-%d",
		os.Getpid(),
		selfTestSequence.Add(1),
	)
	runtime, err := s.Prepare(id)
	if err != nil {
		return &RunError{Kind: ErrorStart, Err: err}
	}
	if s.selfTestPrepared != nil {
		s.selfTestPrepared(runtime)
	}
	result, err := s.Execute(ctx, runtime, CommandSpec{
		Executable: gatewayExecutable,
		Args:       []string{"__process-selftest", "parent"},
		Env:        []string{},
		Dir:        runtime.Dir,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 ||
		string(result.Stdout) != "ready\n" ||
		len(result.Stderr) != 0 ||
		(result.StopAction != StopActionTERM &&
			result.StopAction != StopActionKILL &&
			result.StopAction != StopActionTerminateJob) {
		return &RunError{
			Kind: ErrorStart,
			Err:  errors.New("process self-test returned an invalid result"),
		}
	}
	return nil
}

func (s *Supervisor) validateRuntimeOwnership(runtime Runtime) error {
	if runtime.owner != s.root ||
		runtime.record == nil ||
		runtime.generation == 0 ||
		runtime.ID == "" ||
		runtime.Dir == "" {
		return errInvalidRuntime
	}
	return nil
}

func (s *Supervisor) consumeRuntimeLease(
	runtime Runtime,
) (*runtimeLease, error) {
	if s == nil || s.root == nil ||
		s.identity == 0 ||
		s.validateRuntimeOwnership(runtime) != nil ||
		runtime.supervisorID != s.identity ||
		runtime.leaseGeneration == 0 ||
		runtime.lease == nil {
		return nil, errRuntimeLeaseInvalid
	}
	lease := runtime.lease
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.supervisor != s ||
		lease.supervisorID != s.identity ||
		lease.generation != runtime.leaseGeneration ||
		lease.runtimeID != runtime.ID ||
		lease.runtimeDir != runtime.Dir ||
		lease.runtimeOwner != runtime.owner ||
		lease.runtimeRecord != runtime.record ||
		lease.runtimeGeneration != runtime.generation ||
		lease.state != runtimeLeasePrepared ||
		lease.release == nil {
		return nil, errRuntimeLeaseInvalid
	}
	lease.state = runtimeLeaseConsuming
	return lease, nil
}

func (s *Supervisor) cleanupRuntime(
	ctx context.Context,
	runtime Runtime,
) error {
	cleanupCtx, cancel := independentCleanupContext(ctx, s.limits.Cleanup)
	defer cancel()
	return s.root.Cleanup(cleanupCtx, runtime)
}

func independentCleanupContext(
	ctx context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
