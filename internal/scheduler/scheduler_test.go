package scheduler

import (
	"context"
	"errors"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testDeadline         = 10 * time.Second
	abnormalWorkDeadline = 2 * time.Second
	raceIterations       = 10_000
)

func TestNewValidatesExactTask5LimitsWithoutDefaults(t *testing.T) {
	valid := Limits{
		Concurrency:  1,
		QueueSize:    1,
		QueueBytes:   1,
		QueueTimeout: time.Nanosecond,
	}
	tests := []struct {
		name   string
		mutate func(*Limits)
	}{
		{"zero concurrency", func(limits *Limits) { limits.Concurrency = 0 }},
		{"negative concurrency", func(limits *Limits) { limits.Concurrency = -1 }},
		{"concurrency above ceiling", func(limits *Limits) { limits.Concurrency = 65 }},
		{"zero queue size", func(limits *Limits) { limits.QueueSize = 0 }},
		{"negative queue size", func(limits *Limits) { limits.QueueSize = -1 }},
		{"queue size above ceiling", func(limits *Limits) { limits.QueueSize = 4_097 }},
		{"zero queue bytes", func(limits *Limits) { limits.QueueBytes = 0 }},
		{"negative queue bytes", func(limits *Limits) { limits.QueueBytes = -1 }},
		{"queue bytes above ceiling", func(limits *Limits) { limits.QueueBytes = (1 << 30) + 1 }},
		{"zero queue timeout", func(limits *Limits) { limits.QueueTimeout = 0 }},
		{"negative queue timeout", func(limits *Limits) { limits.QueueTimeout = -1 }},
		{"queue timeout above ceiling", func(limits *Limits) {
			limits.QueueTimeout = 24*time.Hour + time.Nanosecond
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := valid
			test.mutate(&limits)
			scheduler, err := New(limits)
			if err == nil {
				if scheduler != nil {
					_ = scheduler.Shutdown(context.Background())
				}
				t.Fatal("New() accepted invalid limits")
			}
			if scheduler != nil {
				t.Fatalf("New() scheduler=%v after error", scheduler)
			}
			for _, sensitive := range []string{"4097", "1073741825", "86400000000001"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("New() error exposed configured value: %q", err)
				}
			}
		})
	}

	scheduler, err := New(Limits{
		Concurrency:  64,
		QueueSize:    4_096,
		QueueBytes:   1 << 30,
		QueueTimeout: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("New() rejected exact Task 5 ceilings: %v", err)
	}
	if err := scheduler.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error=%v", err)
	}
}

func TestDoRejectsInvalidInputsWithoutPanicking(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	work := func(context.Context) error { return nil }

	for _, weight := range []int64{0, -1} {
		if err := scheduler.Do(context.Background(), weight, work); err == nil {
			t.Fatalf("Do() accepted weight %d", weight)
		}
	}
	var nilContext context.Context
	if err := scheduler.Do(nilContext, 1, work); err == nil {
		t.Fatal("Do() accepted a nil context")
	}
	if err := scheduler.Do(context.Background(), 1, nil); err == nil {
		t.Fatal("Do() accepted nil work")
	}
	if got := scheduler.Stats(); got != (Stats{}) {
		t.Fatalf("Stats()=%+v after rejected work", got)
	}
}

func TestConcurrencyAndFIFO(t *testing.T) {
	scheduler := newTestScheduler(t, Limits{
		Concurrency:  1,
		QueueSize:    2,
		QueueBytes:   100,
		QueueTimeout: time.Second,
	})

	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan int, 3)
	run := func(id int) func(context.Context) error {
		return func(context.Context) error {
			started <- id
			if id == 0 {
				<-block
			}
			return nil
		}
	}

	errs := make(chan error, 3)
	go func() { errs <- scheduler.Do(context.Background(), 1, run(0)) }()
	if id := receive(t, started); id != 0 {
		t.Fatalf("first=%d", id)
	}
	go func() { errs <- scheduler.Do(context.Background(), 1, run(1)) }()
	waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 1, Running: 1})
	go func() { errs <- scheduler.Do(context.Background(), 1, run(2)) }()
	waitForStats(t, scheduler, Stats{Queued: 2, QueuedBytes: 2, Running: 1})

	closeBlock()
	if id := receive(t, started); id != 1 {
		t.Fatalf("second=%d", id)
	}
	if id := receive(t, started); id != 2 {
		t.Fatalf("third=%d", id)
	}
	for range 3 {
		if err := receive(t, errs); err != nil {
			t.Fatalf("Do() error=%v", err)
		}
	}
	waitForStats(t, scheduler, Stats{})
}

func TestQueueCountFull(t *testing.T) {
	scheduler := newTestScheduler(t, Limits{
		Concurrency:  1,
		QueueSize:    1,
		QueueBytes:   100,
		QueueTimeout: time.Second,
	})
	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan struct{}, 1)
	first := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		started <- struct{}{}
		<-block
		return nil
	})
	receive(t, started)

	second := doAsync(context.Background(), scheduler, 1, func(context.Context) error { return nil })
	waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 1, Running: 1})
	full := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		t.Error("queue-full work ran")
		return nil
	})
	if err := receive(t, full); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Do() error=%v, want ErrQueueFull", err)
	}

	closeBlock()
	if err := receive(t, first); err != nil {
		t.Fatalf("first Do() error=%v", err)
	}
	if err := receive(t, second); err != nil {
		t.Fatalf("second Do() error=%v", err)
	}
}

func TestQueuedByteFullAndCheckedAddition(t *testing.T) {
	scheduler := newTestScheduler(t, Limits{
		Concurrency:  1,
		QueueSize:    2,
		QueueBytes:   1 << 30,
		QueueTimeout: time.Second,
	})
	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan struct{}, 1)
	first := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		started <- struct{}{}
		<-block
		return nil
	})
	receive(t, started)

	second := doAsync(context.Background(), scheduler, 1, func(context.Context) error { return nil })
	waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 1, Running: 1})
	overflow := doAsync(context.Background(), scheduler, math.MaxInt64, func(context.Context) error {
		t.Error("overflowing queued-byte work ran")
		return nil
	})
	if err := receive(t, overflow); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Do() overflow error=%v, want ErrQueueFull", err)
	}
	if got := scheduler.Stats(); got != (Stats{Queued: 1, QueuedBytes: 1, Running: 1}) {
		t.Fatalf("Stats()=%+v after overflowing admission", got)
	}

	closeBlock()
	if err := receive(t, first); err != nil {
		t.Fatalf("first Do() error=%v", err)
	}
	if err := receive(t, second); err != nil {
		t.Fatalf("second Do() error=%v", err)
	}
}

func TestQueuedByteCapacity(t *testing.T) {
	scheduler := newTestScheduler(t, Limits{
		Concurrency:  1,
		QueueSize:    2,
		QueueBytes:   2,
		QueueTimeout: time.Second,
	})
	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan struct{}, 1)
	first := doAsync(context.Background(), scheduler, 2, func(context.Context) error {
		started <- struct{}{}
		<-block
		return nil
	})
	receive(t, started)

	second := doAsync(context.Background(), scheduler, 2, func(context.Context) error { return nil })
	waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 2, Running: 1})
	full := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		t.Error("queued-byte-full work ran")
		return nil
	})
	if err := receive(t, full); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Do() error=%v, want ErrQueueFull", err)
	}

	closeBlock()
	if err := receive(t, first); err != nil {
		t.Fatalf("first Do() error=%v", err)
	}
	if err := receive(t, second); err != nil {
		t.Fatalf("second Do() error=%v", err)
	}
}

func TestCanceledBeforeEnqueue(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	var called atomic.Bool

	err := scheduler.Do(caller, 1, func(context.Context) error {
		called.Store(true)
		return nil
	})
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("Do() error=%v, want ErrCanceled", err)
	}
	if called.Load() {
		t.Fatal("work ran for a pre-canceled caller")
	}
	if got := scheduler.Stats(); got != (Stats{}) {
		t.Fatalf("Stats()=%+v", got)
	}
}

func TestCanceledWhileQueued(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan struct{}, 1)
	first := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		started <- struct{}{}
		<-block
		return nil
	})
	receive(t, started)

	caller, cancel := context.WithCancel(context.Background())
	var called atomic.Bool
	queued := doAsync(caller, scheduler, 5, func(context.Context) error {
		called.Store(true)
		return nil
	})
	waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 5, Running: 1})
	cancel()
	if err := receive(t, queued); !errors.Is(err, ErrCanceled) {
		t.Fatalf("queued Do() error=%v, want ErrCanceled", err)
	}
	if called.Load() {
		t.Fatal("canceled queued work ran")
	}
	waitForStats(t, scheduler, Stats{Running: 1})

	closeBlock()
	if err := receive(t, first); err != nil {
		t.Fatalf("first Do() error=%v", err)
	}
}

func TestQueueDeadline(t *testing.T) {
	limits := defaultTestLimits()
	limits.QueueTimeout = 10 * time.Millisecond
	scheduler := newTestScheduler(t, limits)
	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan struct{}, 1)
	first := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		started <- struct{}{}
		<-block
		return nil
	})
	receive(t, started)

	var called atomic.Bool
	queued := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		called.Store(true)
		return nil
	})
	if err := receive(t, queued); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("queued Do() error=%v, want ErrQueueTimeout", err)
	}
	if called.Load() {
		t.Fatal("expired queued work ran")
	}
	waitForStats(t, scheduler, Stats{Running: 1})

	closeBlock()
	if err := receive(t, first); err != nil {
		t.Fatalf("first Do() error=%v", err)
	}
}

func TestCancellationObservedAfterDequeueBeforeWork(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	queueContext, queueCancel := context.WithCancel(context.Background())
	defer queueCancel()
	var called atomic.Bool
	current := &item{
		caller:   caller,
		queueCtx: queueContext,
		queueCancel: func() {
			queueCancel()
			cancel()
		},
		weight: 1,
		work: func(context.Context) error {
			called.Store(true)
			return nil
		},
		done:  make(chan error, 1),
		state: stateQueued,
	}

	scheduler.mu.Lock()
	current.elem = scheduler.queue.PushBack(current)
	scheduler.queuedBytes += current.weight
	scheduler.cond.Signal()
	scheduler.mu.Unlock()

	if err := receive(t, current.done); !errors.Is(err, ErrCanceled) {
		t.Fatalf("dequeued item error=%v, want ErrCanceled", err)
	}
	if called.Load() {
		t.Fatal("work ran after cancellation was observed at the starting transition")
	}
	waitForStats(t, scheduler, Stats{})
}

func TestQueuedTerminalPrecedenceWhenSignalsAreAlreadyReady(t *testing.T) {
	canceledCaller, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	expiredQueue, cancelQueue := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelQueue()
	stopped, stop := context.WithCancelCause(context.Background())
	stop(errCauseSchedulerShutdown)

	tests := []struct {
		name      string
		scheduler *Scheduler
		current   *item
		want      error
	}{
		{
			name:      "shutdown before caller and queue timeout",
			scheduler: &Scheduler{closed: true, stop: stopped},
			current:   &item{caller: canceledCaller, queueCtx: expiredQueue},
			want:      ErrShuttingDown,
		},
		{
			name:      "caller before queue timeout",
			scheduler: &Scheduler{stop: context.Background()},
			current:   &item{caller: canceledCaller, queueCtx: expiredQueue},
			want:      ErrCanceled,
		},
		{
			name:      "queue timeout",
			scheduler: &Scheduler{stop: context.Background()},
			current:   &item{caller: context.Background(), queueCtx: expiredQueue},
			want:      ErrQueueTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.scheduler.mu.Lock()
			got := test.scheduler.queuedTerminalLocked(test.current)
			test.scheduler.mu.Unlock()
			if !errors.Is(got, test.want) {
				t.Fatalf("queued terminal=%v, want %v", got, test.want)
			}
		})
	}
}

func TestPermitRetainedUntilWorkReturns(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	caller, cancel := context.WithCancel(context.Background())
	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan struct{}, 1)
	first := doAsync(caller, scheduler, 1, func(context.Context) error {
		started <- struct{}{}
		<-block
		return nil
	})
	receive(t, started)

	secondStarted := make(chan struct{}, 1)
	second := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		secondStarted <- struct{}{}
		return nil
	})
	waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 1, Running: 1})
	cancel()
	waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 1, Running: 1})
	select {
	case err := <-first:
		t.Fatalf("running Do() returned before work: %v", err)
	case <-secondStarted:
		t.Fatal("second work started before first work returned")
	default:
	}

	closeBlock()
	if err := receive(t, first); !errors.Is(err, ErrCanceled) {
		t.Fatalf("first Do() error=%v, want ErrCanceled", err)
	}
	receive(t, secondStarted)
	if err := receive(t, second); err != nil {
		t.Fatalf("second Do() error=%v", err)
	}
}

func TestWorkResultIsPreserved(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	workError := errors.New("work failed")
	if err := scheduler.Do(context.Background(), 1, func(context.Context) error {
		return workError
	}); !errors.Is(err, workError) {
		t.Fatalf("Do() error=%v, want work error", err)
	}
}

func TestWorkPanicIsContainedWithoutExposingItsValue(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	const sensitivePanic = "private provider output"
	err := scheduler.Do(context.Background(), 1, func(context.Context) error {
		panic(sensitivePanic)
	})
	if err == nil {
		t.Fatal("Do() returned nil after work panicked")
	}
	if strings.Contains(err.Error(), sensitivePanic) {
		t.Fatalf("Do() error exposed panic value: %q", err)
	}
	if err.Error() != "scheduler work terminated abnormally" {
		t.Fatalf("Do() error=%q, want fixed abnormal-work error", err)
	}
	waitForStats(t, scheduler, Stats{})
}

func TestGoexitDoesNotLoseWorkerOrAccounting(t *testing.T) {
	t.Run("worker remains usable", func(t *testing.T) {
		scheduler := newTestScheduler(t, defaultTestLimits())
		started := make(chan struct{})
		result := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
			close(started)
			runtime.Goexit()
			return nil
		})
		receive(t, started)

		err := receiveWithin(t, result, abnormalWorkDeadline)
		if err == nil || err.Error() != "scheduler work terminated abnormally" {
			t.Fatalf("Goexit Do() error=%v, want fixed abnormal-work error", err)
		}
		waitForStats(t, scheduler, Stats{})

		subsequentStarted := make(chan struct{}, 1)
		subsequent := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
			subsequentStarted <- struct{}{}
			return nil
		})
		receiveWithin(t, subsequentStarted, abnormalWorkDeadline)
		if err := receiveWithin(t, subsequent, abnormalWorkDeadline); err != nil {
			t.Fatalf("subsequent Do() error=%v", err)
		}
		waitForStats(t, scheduler, Stats{})
	})

	t.Run("shutdown waits for terminal cleanup", func(t *testing.T) {
		scheduler := newTestScheduler(t, defaultTestLimits())
		started := make(chan struct{})
		exitWork := make(chan struct{})
		closeExitWork := closeAtCleanup(t, exitWork)
		result := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
			close(started)
			<-exitWork
			runtime.Goexit()
			return nil
		})
		receive(t, started)

		shutdown := shutdownAsync(context.Background(), scheduler)
		waitForClosed(t, scheduler)
		closeExitWork()
		if err := receiveWithin(t, shutdown, abnormalWorkDeadline); err != nil {
			t.Fatalf("Shutdown() error=%v", err)
		}
		if got := scheduler.Stats(); got != (Stats{}) {
			t.Errorf("Stats() after Shutdown=%+v, want zero accounting", got)
		}
		if err := receiveWithin(t, result, abnormalWorkDeadline); !errors.Is(err, ErrShuttingDown) {
			t.Fatalf("Goexit Do() error=%v, want ErrShuttingDown", err)
		}
	})
}

func TestShutdownRejectsNewWorkAndCancelsQueuedWork(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan struct{}, 1)
	runCanceled := make(chan error, 1)
	first := doAsync(context.Background(), scheduler, 1, func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		runCanceled <- context.Cause(ctx)
		<-block
		return nil
	})
	receive(t, started)

	var queuedCalled atomic.Bool
	queued := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
		queuedCalled.Store(true)
		return nil
	})
	waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 1, Running: 1})

	shutdown := shutdownAsync(context.Background(), scheduler)
	if err := receive(t, queued); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("queued Do() error=%v, want ErrShuttingDown", err)
	}
	if queuedCalled.Load() {
		t.Fatal("queued work ran during shutdown")
	}
	if cause := receive(t, runCanceled); !errors.Is(cause, errCauseSchedulerShutdown) {
		t.Fatalf("run context cause=%v, want scheduler shutdown cause", cause)
	}
	if err := scheduler.Do(context.Background(), 1, func(context.Context) error { return nil }); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Do() after shutdown error=%v, want ErrShuttingDown", err)
	}

	closeBlock()
	if err := receive(t, first); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("running Do() error=%v, want ErrShuttingDown", err)
	}
	if err := receive(t, shutdown); err != nil {
		t.Fatalf("Shutdown() error=%v", err)
	}
}

func TestShutdownRejectsNilContextWithoutPanicking(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	var nilContext context.Context
	if err := scheduler.Shutdown(nilContext); err == nil {
		t.Fatal("Shutdown() accepted a nil context")
	}
	if err := scheduler.Do(context.Background(), 1, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("scheduler closed after rejected Shutdown(): %v", err)
	}
}

func TestShutdownDeadlineAndIdempotence(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	block := make(chan struct{})
	closeBlock := closeAtCleanup(t, block)
	started := make(chan struct{}, 1)
	runCanceled := make(chan struct{}, 1)
	running := doAsync(context.Background(), scheduler, 1, func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		runCanceled <- struct{}{}
		<-block
		return nil
	})
	receive(t, started)

	shutdownContext, cancelShutdown := context.WithCancel(context.Background())
	cancelShutdown()
	if err := scheduler.Shutdown(shutdownContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown() error=%v, want context.Canceled", err)
	}
	receive(t, runCanceled)
	if got := scheduler.Stats(); got != (Stats{Running: 1}) {
		t.Fatalf("Stats()=%+v during timed-out shutdown", got)
	}

	closeBlock()
	if err := receive(t, running); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("running Do() error=%v, want ErrShuttingDown", err)
	}
	if err := scheduler.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error=%v", err)
	}
	if err := scheduler.Shutdown(context.Background()); err != nil {
		t.Fatalf("third Shutdown() error=%v", err)
	}
}

func TestActiveCallerCancellationCause(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	caller, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	causeSeen := make(chan error, 1)
	result := doAsync(caller, scheduler, 1, func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		causeSeen <- context.Cause(ctx)
		return nil
	})
	receive(t, started)
	cancel()

	if cause := receive(t, causeSeen); !errors.Is(cause, errCauseCallerCanceled) {
		t.Fatalf("run context cause=%v, want caller cancellation cause", cause)
	}
	if err := receive(t, result); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Do() error=%v, want ErrCanceled", err)
	}
}

func TestActiveShutdownCause(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())
	started := make(chan struct{}, 1)
	causeSeen := make(chan error, 1)
	result := doAsync(context.Background(), scheduler, 1, func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		causeSeen <- context.Cause(ctx)
		return nil
	})
	receive(t, started)
	shutdown := shutdownAsync(context.Background(), scheduler)

	if cause := receive(t, causeSeen); !errors.Is(cause, errCauseSchedulerShutdown) {
		t.Fatalf("run context cause=%v, want scheduler shutdown cause", cause)
	}
	if err := receive(t, result); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Do() error=%v, want ErrShuttingDown", err)
	}
	if err := receive(t, shutdown); err != nil {
		t.Fatalf("Shutdown() error=%v", err)
	}
}

func TestActiveCancellationCauseIsStableAcrossOrderedRaces(t *testing.T) {
	t.Run("caller then shutdown", func(t *testing.T) {
		scheduler := newTestScheduler(t, defaultTestLimits())
		caller, cancelCaller := context.WithCancel(context.Background())
		started := make(chan struct{}, 1)
		firstCause := make(chan error, 1)
		secondCause := make(chan error, 1)
		recheck := make(chan struct{})
		release := make(chan struct{})
		closeRelease := closeAtCleanup(t, release)
		result := doAsync(caller, scheduler, 1, func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			firstCause <- context.Cause(ctx)
			<-recheck
			secondCause <- context.Cause(ctx)
			<-release
			return nil
		})
		receive(t, started)
		cancelCaller()
		if cause := receive(t, firstCause); !errors.Is(cause, errCauseCallerCanceled) {
			t.Fatalf("initial cause=%v, want caller cancellation", cause)
		}
		shutdown := shutdownAsync(context.Background(), scheduler)
		waitForClosed(t, scheduler)
		close(recheck)
		if cause := receive(t, secondCause); !errors.Is(cause, errCauseCallerCanceled) {
			t.Fatalf("changed cause=%v, want stable caller cancellation", cause)
		}
		closeRelease()
		if err := receive(t, result); !errors.Is(err, ErrShuttingDown) {
			t.Fatalf("Do() error=%v, want shutdown precedence", err)
		}
		if err := receive(t, shutdown); err != nil {
			t.Fatalf("Shutdown() error=%v", err)
		}
	})

	t.Run("shutdown then caller", func(t *testing.T) {
		scheduler := newTestScheduler(t, defaultTestLimits())
		caller, cancelCaller := context.WithCancel(context.Background())
		started := make(chan struct{}, 1)
		firstCause := make(chan error, 1)
		secondCause := make(chan error, 1)
		recheck := make(chan struct{})
		release := make(chan struct{})
		closeRelease := closeAtCleanup(t, release)
		result := doAsync(caller, scheduler, 1, func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			firstCause <- context.Cause(ctx)
			<-recheck
			secondCause <- context.Cause(ctx)
			<-release
			return nil
		})
		receive(t, started)
		shutdown := shutdownAsync(context.Background(), scheduler)
		if cause := receive(t, firstCause); !errors.Is(cause, errCauseSchedulerShutdown) {
			t.Fatalf("initial cause=%v, want scheduler shutdown", cause)
		}
		cancelCaller()
		close(recheck)
		if cause := receive(t, secondCause); !errors.Is(cause, errCauseSchedulerShutdown) {
			t.Fatalf("changed cause=%v, want stable scheduler shutdown", cause)
		}
		closeRelease()
		if err := receive(t, result); !errors.Is(err, ErrShuttingDown) {
			t.Fatalf("Do() error=%v, want ErrShuttingDown", err)
		}
		if err := receive(t, shutdown); err != nil {
			t.Fatalf("Shutdown() error=%v", err)
		}
	})
}

func TestCombinedRunContextBothReadyUsesStableShutdownPrecedence(t *testing.T) {
	for iteration := range raceIterations {
		caller, cancelCaller := context.WithCancel(context.Background())
		stopContext, stopScheduler := context.WithCancelCause(context.Background())
		scheduler := &Scheduler{stop: stopContext}
		current := &item{caller: caller}

		gate := make(chan struct{})
		callerDone := make(chan struct{}, 1)
		schedulerDone := make(chan struct{}, 1)
		go func() {
			<-gate
			cancelCaller()
			callerDone <- struct{}{}
		}()
		go func() {
			<-gate
			stopScheduler(errCauseSchedulerShutdown)
			schedulerDone <- struct{}{}
		}()
		close(gate)
		receive(t, callerDone)
		receive(t, schedulerDone)

		runContext, cleanup := scheduler.newRunContext(current)
		if cause := context.Cause(runContext); !errors.Is(cause, errCauseSchedulerShutdown) {
			cleanup()
			t.Fatalf(
				"iteration %d run context cause=%v, want stable scheduler shutdown precedence",
				iteration,
				cause,
			)
		}
		cleanup()
	}
}

func TestCancelDequeueRace(t *testing.T) {
	scheduler := newTestScheduler(t, defaultTestLimits())

	for iteration := range raceIterations {
		block := make(chan struct{})
		started := make(chan struct{}, 1)
		first := doAsync(context.Background(), scheduler, 1, func(context.Context) error {
			started <- struct{}{}
			<-block
			return nil
		})
		receive(t, started)

		caller, cancel := context.WithCancel(context.Background())
		queued := doAsync(caller, scheduler, 1, func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		})
		waitForStats(t, scheduler, Stats{Queued: 1, QueuedBytes: 1, Running: 1})

		gate := make(chan struct{})
		released := make(chan struct{}, 1)
		canceled := make(chan struct{}, 1)
		go func() {
			<-gate
			close(block)
			released <- struct{}{}
		}()
		go func() {
			<-gate
			cancel()
			canceled <- struct{}{}
		}()
		close(gate)
		receive(t, released)
		receive(t, canceled)

		if err := receive(t, first); err != nil {
			t.Fatalf("iteration %d first Do() error=%v", iteration, err)
		}
		if err := receive(t, queued); !errors.Is(err, ErrCanceled) {
			t.Fatalf("iteration %d queued Do() error=%v, want ErrCanceled", iteration, err)
		}
		waitForStats(t, scheduler, Stats{})
	}
}

func defaultTestLimits() Limits {
	return Limits{
		Concurrency:  1,
		QueueSize:    2,
		QueueBytes:   100,
		QueueTimeout: time.Second,
	}
}

func newTestScheduler(t *testing.T, limits Limits) *Scheduler {
	t.Helper()
	scheduler, err := New(limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
		defer cancel()
		if err := scheduler.Shutdown(ctx); err != nil {
			t.Errorf("cleanup Shutdown() error=%v", err)
		}
	})
	return scheduler
}

func doAsync(
	ctx context.Context,
	scheduler *Scheduler,
	weight int64,
	work func(context.Context) error,
) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- scheduler.Do(ctx, weight, work)
	}()
	return result
}

func shutdownAsync(ctx context.Context, scheduler *Scheduler) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- scheduler.Shutdown(ctx)
	}()
	return result
}

func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(testDeadline):
		t.Fatal("timed out waiting for channel value")
		var zero T
		return zero
	}
}

func receiveWithin[T any](t *testing.T, values <-chan T, deadline time.Duration) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(deadline):
		t.Fatal("timed out waiting for channel value")
		var zero T
		return zero
	}
}

func waitForStats(t *testing.T, scheduler *Scheduler, want Stats) {
	t.Helper()
	deadline := time.Now().Add(testDeadline)
	for {
		if got := scheduler.Stats(); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Stats()=%+v, want %+v", scheduler.Stats(), want)
		}
		runtime.Gosched()
	}
}

func waitForClosed(t *testing.T, scheduler *Scheduler) {
	t.Helper()
	deadline := time.Now().Add(testDeadline)
	for {
		scheduler.mu.Lock()
		closed := scheduler.closed
		scheduler.mu.Unlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not enter the closed state")
		}
		runtime.Gosched()
	}
}

func closeAtCleanup(t *testing.T, channel chan struct{}) func() {
	t.Helper()
	var once sync.Once
	closeChannel := func() {
		once.Do(func() {
			close(channel)
		})
	}
	t.Cleanup(closeChannel)
	return closeChannel
}
