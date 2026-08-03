// Package scheduler provides bounded FIFO admission and cancelable execution.
package scheduler

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

const (
	maxConcurrency  = 64
	maxQueueSize    = 4_096
	maxQueueBytes   = int64(1 << 30)
	maxQueueTimeout = 24 * time.Hour
)

var (
	// ErrQueueFull reports that accepting an item would exceed a queue bound.
	ErrQueueFull = errors.New("scheduler queue is full")
	// ErrQueueTimeout reports that an item expired while it was still queued.
	ErrQueueTimeout = errors.New("scheduler queue wait expired")
	// ErrCanceled reports cancellation by the caller.
	ErrCanceled = errors.New("scheduler request canceled")
	// ErrShuttingDown reports scheduler shutdown.
	ErrShuttingDown = errors.New("scheduler is shutting down")

	errCauseCallerCanceled    = errors.New("scheduler caller canceled")
	errCauseSchedulerShutdown = errors.New("scheduler shutdown")
	errWorkAbnormal           = errors.New("scheduler work terminated abnormally")
)

// Limits defines the scheduler's concurrency and bounded queue.
type Limits struct {
	Concurrency  int
	QueueSize    int
	QueueBytes   int64
	QueueTimeout time.Duration
}

// Stats is a point-in-time snapshot containing only aggregate counters.
type Stats struct {
	Queued      int
	QueuedBytes int64
	Running     int
}

type itemState uint8

const (
	stateQueued itemState = iota
	stateStarting
	stateRunning
	stateDone
)

type item struct {
	caller      context.Context
	queueCtx    context.Context
	queueCancel context.CancelFunc
	weight      int64
	work        func(context.Context) error
	done        chan error
	state       itemState
	elem        *list.Element
}

// Scheduler runs bounded work in FIFO dequeue order.
type Scheduler struct {
	mu          sync.Mutex
	cond        *sync.Cond
	queue       list.List
	queuedBytes int64
	running     int
	limits      Limits
	closed      bool
	stop        context.Context
	stopCancel  context.CancelCauseFunc
	workers     sync.WaitGroup
	workersDone chan struct{}
}

// New validates limits and starts the fixed worker pool.
func New(limits Limits) (*Scheduler, error) {
	if err := validateLimits(limits); err != nil {
		return nil, err
	}

	stop, stopCancel := context.WithCancelCause(context.Background())
	scheduler := &Scheduler{
		limits:      limits,
		stop:        stop,
		stopCancel:  stopCancel,
		workersDone: make(chan struct{}),
	}
	scheduler.cond = sync.NewCond(&scheduler.mu)
	scheduler.workers.Add(limits.Concurrency)
	for range limits.Concurrency {
		go scheduler.worker()
	}
	go func() {
		scheduler.workers.Wait()
		close(scheduler.workersDone)
	}()
	return scheduler, nil
}

func validateLimits(limits Limits) error {
	switch {
	case limits.Concurrency <= 0 || limits.Concurrency > maxConcurrency:
		return errors.New("scheduler: invalid concurrency")
	case limits.QueueSize <= 0 || limits.QueueSize > maxQueueSize:
		return errors.New("scheduler: invalid queue size")
	case limits.QueueBytes <= 0 || limits.QueueBytes > maxQueueBytes:
		return errors.New("scheduler: invalid queued byte limit")
	case limits.QueueTimeout <= 0 || limits.QueueTimeout > maxQueueTimeout:
		return errors.New("scheduler: invalid queue timeout")
	default:
		return nil
	}
}

// Do admits work to the FIFO queue and waits for its terminal result.
func (scheduler *Scheduler) Do(
	ctx context.Context,
	weight int64,
	work func(context.Context) error,
) error {
	if ctx == nil {
		return errors.New("scheduler: nil caller context")
	}
	if weight <= 0 {
		return errors.New("scheduler: invalid work weight")
	}
	if work == nil {
		return errors.New("scheduler: nil work")
	}

	queueCtx, queueCancel := context.WithTimeout(context.Background(), scheduler.limits.QueueTimeout)
	current := &item{
		caller:      ctx,
		queueCtx:    queueCtx,
		queueCancel: queueCancel,
		weight:      weight,
		work:        work,
		done:        make(chan error, 1),
		state:       stateQueued,
	}

	scheduler.mu.Lock()
	if terminal := scheduler.queuedTerminalLocked(current); terminal != nil {
		scheduler.mu.Unlock()
		queueCancel()
		return terminal
	}
	if scheduler.queue.Len() >= scheduler.limits.QueueSize ||
		weight > scheduler.limits.QueueBytes-scheduler.queuedBytes {
		scheduler.mu.Unlock()
		queueCancel()
		return ErrQueueFull
	}
	current.elem = scheduler.queue.PushBack(current)
	scheduler.queuedBytes += weight
	scheduler.cond.Signal()
	scheduler.mu.Unlock()

	for {
		select {
		case result := <-current.done:
			return result
		case <-ctx.Done():
		case <-queueCtx.Done():
		}

		scheduler.mu.Lock()
		if current.state != stateQueued {
			scheduler.mu.Unlock()
			return <-current.done
		}
		terminal := scheduler.queuedTerminalLocked(current)
		if terminal == nil {
			scheduler.mu.Unlock()
			continue
		}
		scheduler.completeQueuedLocked(current, terminal)
		scheduler.mu.Unlock()
		return <-current.done
	}
}

// Stats returns an aggregate snapshot without retaining request data.
func (scheduler *Scheduler) Stats() Stats {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return Stats{
		Queued:      scheduler.queue.Len(),
		QueuedBytes: scheduler.queuedBytes,
		Running:     scheduler.running,
	}
}

// Shutdown rejects new work, cancels queued and active work, and waits for all
// workers to return or for ctx to finish.
func (scheduler *Scheduler) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scheduler: nil shutdown context")
	}

	scheduler.mu.Lock()
	if !scheduler.closed {
		scheduler.closed = true
		for front := scheduler.queue.Front(); front != nil; front = scheduler.queue.Front() {
			current := front.Value.(*item)
			scheduler.completeQueuedLocked(current, ErrShuttingDown)
		}
		scheduler.stopCancel(errCauseSchedulerShutdown)
		scheduler.cond.Broadcast()
	}
	scheduler.mu.Unlock()

	select {
	case <-scheduler.workersDone:
		return nil
	default:
	}
	select {
	case <-scheduler.workersDone:
		return nil
	case <-ctx.Done():
		select {
		case <-scheduler.workersDone:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func (scheduler *Scheduler) worker() {
	defer scheduler.workers.Done()
	for {
		scheduler.mu.Lock()
		for scheduler.queue.Len() == 0 && !scheduler.closed {
			scheduler.cond.Wait()
		}
		if scheduler.queue.Len() == 0 {
			scheduler.mu.Unlock()
			return
		}

		current := scheduler.queue.Front().Value.(*item)
		scheduler.queue.Remove(current.elem)
		current.elem = nil
		scheduler.queuedBytes -= current.weight
		current.state = stateStarting
		current.queueCancel()
		scheduler.mu.Unlock()

		scheduler.execute(current)
	}
}

func (scheduler *Scheduler) execute(current *item) {
	runCtx, cleanup := scheduler.newRunContext(current)

	scheduler.mu.Lock()
	if cause := scheduler.activeCancellationCauseLocked(current); cause != nil {
		current.state = stateDone
		result := terminalFromCause(cause)
		scheduler.mu.Unlock()
		cleanup()
		current.done <- result
		return
	}
	current.state = stateRunning
	scheduler.running++
	scheduler.mu.Unlock()

	workResult := invokeWork(runCtx, current.work)
	cleanup()

	scheduler.mu.Lock()
	scheduler.running--
	current.state = stateDone
	result := scheduler.activeTerminalLocked(current, workResult)
	scheduler.mu.Unlock()
	current.done <- result
}

func (scheduler *Scheduler) newRunContext(current *item) (context.Context, func()) {
	runCtx, runCancel := context.WithCancelCause(context.Background())

	scheduler.mu.Lock()
	initialCause := scheduler.activeCancellationCauseLocked(current)
	if initialCause != nil {
		runCancel(initialCause)
		scheduler.mu.Unlock()
		return runCtx, func() {
			runCancel(nil)
		}
	}
	scheduler.mu.Unlock()

	cancelFromSignals := func() {
		scheduler.mu.Lock()
		cause := scheduler.activeCancellationCauseLocked(current)
		if cause != nil {
			runCancel(cause)
		}
		scheduler.mu.Unlock()
	}
	stopCaller := context.AfterFunc(current.caller, cancelFromSignals)
	stopScheduler := context.AfterFunc(scheduler.stop, cancelFromSignals)
	cancelFromSignals()

	return runCtx, func() {
		stopCaller()
		stopScheduler()
		runCancel(nil)
	}
}

func (scheduler *Scheduler) queuedTerminalLocked(current *item) error {
	switch {
	case scheduler.closed || scheduler.stop.Err() != nil:
		return ErrShuttingDown
	case current.caller.Err() != nil:
		return ErrCanceled
	case current.queueCtx.Err() != nil:
		return ErrQueueTimeout
	default:
		return nil
	}
}

func (scheduler *Scheduler) activeCancellationCauseLocked(current *item) error {
	switch {
	case scheduler.closed || scheduler.stop.Err() != nil:
		return errCauseSchedulerShutdown
	case current.caller.Err() != nil:
		return errCauseCallerCanceled
	default:
		return nil
	}
}

func (scheduler *Scheduler) activeTerminalLocked(current *item, workResult error) error {
	if cause := scheduler.activeCancellationCauseLocked(current); cause != nil {
		return terminalFromCause(cause)
	}
	return workResult
}

func terminalFromCause(cause error) error {
	if errors.Is(cause, errCauseSchedulerShutdown) {
		return ErrShuttingDown
	}
	return ErrCanceled
}

func (scheduler *Scheduler) completeQueuedLocked(current *item, result error) {
	scheduler.queue.Remove(current.elem)
	current.elem = nil
	scheduler.queuedBytes -= current.weight
	current.state = stateDone
	current.queueCancel()
	current.done <- result
}

func invokeWork(ctx context.Context, work func(context.Context) error) error {
	terminal := make(chan error, 1)
	go func() {
		result := errWorkAbnormal
		defer func() {
			_ = recover()
			terminal <- result
		}()
		result = work(ctx)
	}()
	return <-terminal
}
