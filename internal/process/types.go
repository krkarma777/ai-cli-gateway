// Package process owns request-local runtime files and provider process
// lifecycles.
package process

import (
	"errors"
	"fmt"
	"io/fs"
	"time"
)

// ErrExecutableUnavailable marks a launch-boundary failure caused by a
// provider executable that is missing or cannot be executed. Callers should
// inspect it with errors.Is; the underlying filesystem cause is preserved.
var ErrExecutableUnavailable = errors.New("provider executable is unavailable")

func markExecutableUnavailable(err error) error {
	if err == nil ||
		(!errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission)) {
		return err
	}
	return errors.Join(ErrExecutableUnavailable, err)
}

// FileSpec is one request-local file to materialize before process start.
type FileSpec struct {
	Name string
	Data []byte
	Mode fs.FileMode
}

// CommandSpec is a fully resolved provider command.
type CommandSpec struct {
	Executable string
	Args       []string
	Env        []string
	Dir        string
	Stdin      []byte
	Files      []FileSpec
}

// Result contains bounded output and closed process termination metadata.
type Result struct {
	Stdout      []byte
	Stderr      []byte
	StdoutTotal int64
	StderrTotal int64
	ExitCode    int
	StopReason  StopReason
	StopAction  StopAction
}

// Limits bounds one provider process lifecycle.
type Limits struct {
	Execution   time.Duration
	TermGrace   time.Duration
	Cleanup     time.Duration
	StdoutBytes int64
	StderrBytes int64
}

// Runtime identifies one request directory owned by a locked Root.
type Runtime struct {
	ID              string
	Dir             string
	owner           *Root
	record          *runtimeRecord
	generation      uint64
	supervisorID    uint64
	leaseGeneration uint64
	lease           *runtimeLease
}

// ErrorKind is a closed internal process failure category.
type ErrorKind string

// StopReason is a closed internal process stop category.
type StopReason string

// StopAction is a closed internal process containment action.
type StopAction string

const (
	// ErrorCanceled means the caller canceled execution.
	ErrorCanceled ErrorKind = "canceled"
	// ErrorTimeout means the supervisor execution deadline elapsed.
	ErrorTimeout ErrorKind = "timeout"
	// ErrorOutputLimit means a configured output byte limit was exceeded.
	ErrorOutputLimit ErrorKind = "output_limit"
	// ErrorStart means the provider process could not be started.
	ErrorStart ErrorKind = "start"
	// ErrorCleanup means bounded containment or filesystem cleanup failed.
	ErrorCleanup ErrorKind = "cleanup"
)

const (
	// StopReasonNormalExit means the supervised root process exited normally.
	StopReasonNormalExit StopReason = "normal_exit"
	// StopReasonCallerCancellation means the caller canceled execution.
	StopReasonCallerCancellation StopReason = "caller_cancellation"
	// StopReasonSupervisorTimeout means the execution deadline elapsed.
	StopReasonSupervisorTimeout StopReason = "supervisor_timeout"
	// StopReasonOutputOverflow means stdout or stderr exceeded its bound.
	StopReasonOutputOverflow StopReason = "output_overflow"
	// StopReasonCleanupFailure means containment or filesystem cleanup failed.
	StopReasonCleanupFailure StopReason = "cleanup_failure"
)

const (
	// StopActionNone means no containment signal or termination call was needed.
	StopActionNone StopAction = "none"
	// StopActionTERM means Unix SIGTERM was sent.
	StopActionTERM StopAction = "term"
	// StopActionKILL means Unix SIGKILL was sent.
	StopActionKILL StopAction = "kill"
	// StopActionTerminateJob means Windows TerminateJobObject was called.
	StopActionTerminateJob StopAction = "terminate_job"
)

// RunError wraps an internal process cause with a closed category. It must not
// be used directly as a public API error.
type RunError struct {
	Kind ErrorKind
	Err  error
}

// Error exposes the closed kind and its internal cause to internal callers.
func (e *RunError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

// Unwrap returns the internal cause.
func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
