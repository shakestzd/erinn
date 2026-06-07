package writequeue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWriteQueue_SerializesConcurrentProducers spawns N goroutines that
// each submit a marker WriteOp; the consumer must execute them strictly
// one-at-a-time. The recorder mutex panics if a second op enters while
// another is running, which directly proves serialization.
func TestWriteQueue_SerializesConcurrentProducers(t *testing.T) {
	const producers = 16
	const opsPerProducer = 25

	var inFlight atomic.Int32
	var maxConcurrent atomic.Int32
	var executed atomic.Int64

	op := func(_ context.Context) error {
		now := inFlight.Add(1)
		// Track the high-water mark so the test message reads cleanly
		// when serialization breaks.
		for {
			prev := maxConcurrent.Load()
			if now <= prev || maxConcurrent.CompareAndSwap(prev, now) {
				break
			}
		}
		time.Sleep(200 * time.Microsecond)
		inFlight.Add(-1)
		executed.Add(1)
		return nil
	}

	q := New(Config{Capacity: producers * opsPerProducer})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerProducer; i++ {
				if err := q.Submit(context.Background(), op); err != nil {
					t.Errorf("Submit: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	q.Stop(5 * time.Second)

	if got := executed.Load(); got != producers*opsPerProducer {
		t.Errorf("executed = %d, want %d", got, producers*opsPerProducer)
	}
	if peak := maxConcurrent.Load(); peak > 1 {
		t.Errorf("max concurrent in-flight ops = %d, want 1 (single-writer invariant)", peak)
	}
}

// TestWriteQueue_BoundedBackpressure fills the queue to capacity then
// asserts the next Submit returns ErrQueueFull without blocking. The
// consumer is started AFTER the fill so it cannot drain during the fill
// loop (the channel buffer is what we are exercising).
func TestWriteQueue_BoundedBackpressure(t *testing.T) {
	const capacity = 4
	q := New(Config{Capacity: capacity})
	// Build a queue that has not been started — Submit should return
	// ErrWriterUnavailable for a clean before-and-after comparison.
	if err := q.Submit(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrWriterUnavailable) {
		t.Fatalf("pre-start Submit error = %v, want ErrWriterUnavailable", err)
	}

	// Start, then immediately block the consumer with a permanent op so
	// the channel buffer is the only thing absorbing producer submits.
	started := make(chan struct{})
	blockingDone := make(chan struct{})
	blocker := func(ctx context.Context) error {
		close(started)
		<-blockingDone
		return nil
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		close(blockingDone)
		q.Stop(5 * time.Second)
	}()
	if err := q.Submit(context.Background(), blocker); err != nil {
		t.Fatalf("Submit blocker: %v", err)
	}
	// Wait for the consumer to pick the blocker off the channel so the
	// channel buffer is purely producer-visible.
	<-started

	noop := func(context.Context) error { return nil }
	for i := 0; i < capacity; i++ {
		if err := q.Submit(context.Background(), noop); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	err := q.Submit(context.Background(), noop)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overflow Submit error = %v, want ErrQueueFull", err)
	}

	stats := q.Stats()
	if stats.Rejected == 0 {
		t.Errorf("Stats.Rejected = 0, want > 0 after overflow")
	}
}

// TestWriteQueue_TimeoutReturnsError exercises SubmitWithTimeout: a
// permanently-blocked consumer plus a full buffer means the timeout
// branch wins.
func TestWriteQueue_TimeoutReturnsError(t *testing.T) {
	const capacity = 2
	q := New(Config{Capacity: capacity})

	started := make(chan struct{})
	blockingDone := make(chan struct{})
	defer close(blockingDone)
	blocker := func(context.Context) error {
		close(started)
		<-blockingDone
		return nil
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer q.Stop(time.Second)

	if err := q.Submit(context.Background(), blocker); err != nil {
		t.Fatalf("Submit blocker: %v", err)
	}
	// Wait until the consumer has actually pulled the blocker off the
	// channel — only then is the buffer purely producer-visible.
	<-started
	noop := func(context.Context) error { return nil }
	for i := 0; i < capacity; i++ {
		if err := q.Submit(context.Background(), noop); err != nil {
			t.Fatalf("fill Submit %d: %v", i, err)
		}
	}

	err := q.SubmitWithTimeout(context.Background(), noop, 50*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("SubmitWithTimeout error = %v, want ErrTimeout", err)
	}
}

// TestWriteQueue_BurstHandlesGracefully (review-2026-05-11 MED critique):
// submit 2x capacity in a tight loop and assert ~half succeed, the rest
// return ErrQueueFull cleanly — no panic, no lost-work surprises.
func TestWriteQueue_BurstHandlesGracefully(t *testing.T) {
	const capacity = 32
	q := New(Config{Capacity: capacity})

	started := make(chan struct{})
	blockingDone := make(chan struct{})
	defer close(blockingDone)
	blocker := func(context.Context) error {
		close(started)
		<-blockingDone
		return nil
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer q.Stop(time.Second)
	if err := q.Submit(context.Background(), blocker); err != nil {
		t.Fatalf("Submit blocker: %v", err)
	}
	// Wait until the consumer has dequeued the blocker so the buffer
	// is entirely available to the burst loop below.
	<-started

	const burst = 2 * capacity
	var success, full int
	for i := 0; i < burst; i++ {
		err := q.Submit(context.Background(), func(context.Context) error { return nil })
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrQueueFull):
			full++
		default:
			t.Fatalf("unexpected Submit error: %v", err)
		}
	}

	if success != capacity {
		t.Errorf("success count = %d, want exactly %d (burst capacity)", success, capacity)
	}
	if full != burst-capacity {
		t.Errorf("full count = %d, want %d (rejected overflow)", full, burst-capacity)
	}
}

// TestWriteQueue_StopDrainsRemaining asserts that pending ops in the
// channel run to completion when Stop is called.
func TestWriteQueue_StopDrainsRemaining(t *testing.T) {
	q := New(Config{Capacity: 8})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var executed atomic.Int32
	op := func(context.Context) error {
		time.Sleep(5 * time.Millisecond)
		executed.Add(1)
		return nil
	}
	for i := 0; i < 8; i++ {
		if err := q.Submit(context.Background(), op); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}

	q.Stop(5 * time.Second)
	if got := executed.Load(); got != 8 {
		t.Errorf("executed after Stop = %d, want 8 (drain on shutdown)", got)
	}
	if state := q.Stats().State; state != StateStopped {
		t.Errorf("state after Stop = %s, want %s", state, StateStopped)
	}
}

// TestWriteQueue_PostStopReturnsUnavailable verifies the lifecycle
// invariant: once stopped, Submit must not accept new work — the writer
// is gone and the producer's canonical NDJSON has already won.
func TestWriteQueue_PostStopReturnsUnavailable(t *testing.T) {
	q := New(Config{Capacity: 4})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	q.Stop(time.Second)
	err := q.Submit(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrWriterUnavailable) {
		t.Errorf("post-Stop Submit error = %v, want ErrWriterUnavailable", err)
	}
}

// TestWriteQueue_OpErrorIsObservable asserts that an op returning an
// error is surfaced via Stats.Errors and the OnError callback. This
// validates the diagnostic surface dashboard/collector-status reads.
func TestWriteQueue_OpErrorIsObservable(t *testing.T) {
	var captured atomic.Value
	q := New(Config{Capacity: 2, OnError: func(err error) {
		captured.Store(err.Error())
	}})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	wantErr := errors.New("op failed")
	if err := q.Submit(context.Background(), func(context.Context) error { return wantErr }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Stats().Errors > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	q.Stop(time.Second)

	if got := q.Stats().Errors; got != 1 {
		t.Errorf("Stats.Errors = %d, want 1", got)
	}
	if got, _ := captured.Load().(string); got != wantErr.Error() {
		t.Errorf("OnError captured = %q, want %q", got, wantErr.Error())
	}
}

// TestWriteQueue_StatsTracksDepth verifies that depth + counters move
// correctly through Submit → consume → drain. The collector-status
// endpoint depends on these counters; this test locks in their semantics.
func TestWriteQueue_StatsTracksDepth(t *testing.T) {
	q := New(Config{Capacity: 4})

	if got := q.Stats().State; got != StateInit {
		t.Errorf("init state = %s, want %s", got, StateInit)
	}

	blockingDone := make(chan struct{})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer q.Stop(time.Second)

	if got := q.Stats().State; got != StateRunning {
		t.Errorf("post-Start state = %s, want %s", got, StateRunning)
	}

	started := make(chan struct{})
	if err := q.Submit(context.Background(), func(context.Context) error {
		close(started)
		<-blockingDone
		return nil
	}); err != nil {
		t.Fatalf("Submit blocker: %v", err)
	}
	// The blocker is on the consumer goroutine, not the channel buffer.
	// Wait for the consumer to pick it up so we can observe a clean
	// "channel empty, one op executing" snapshot.
	<-started

	noop := func(context.Context) error { return nil }
	for i := 0; i < 3; i++ {
		if err := q.Submit(context.Background(), noop); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	stats := q.Stats()
	if stats.Depth != 3 {
		t.Errorf("Depth = %d, want 3 (after queueing 3 behind a blocked consumer)", stats.Depth)
	}
	if stats.Enqueued != 4 {
		t.Errorf("Enqueued = %d, want 4", stats.Enqueued)
	}
	if stats.Capacity != 4 {
		t.Errorf("Capacity = %d, want 4", stats.Capacity)
	}

	close(blockingDone)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if q.Stats().Dequeued == 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stats = q.Stats()
	if stats.Dequeued != 4 {
		t.Errorf("Dequeued = %d, want 4 after drain", stats.Dequeued)
	}
	if stats.Depth != 0 {
		t.Errorf("Depth = %d, want 0 after drain", stats.Depth)
	}
}

// TestWriteQueue_ContextCancelledRejects checks that a cancelled
// producer context returns the context error without enqueuing.
func TestWriteQueue_ContextCancelledRejects(t *testing.T) {
	q := New(Config{Capacity: 4})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer q.Stop(time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := q.Submit(ctx, func(context.Context) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Submit with cancelled ctx error = %v, want context.Canceled", err)
	}
	if err := q.SubmitWithTimeout(ctx, func(context.Context) error { return nil }, time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("SubmitWithTimeout with cancelled ctx error = %v, want context.Canceled", err)
	}
}

// TestWriteQueue_DefaultCapacity makes sure New(Config{}) lands on a
// non-zero capacity. This protects callers who forget to set it.
func TestWriteQueue_DefaultCapacity(t *testing.T) {
	q := New(Config{})
	if got := q.Capacity(); got != DefaultCapacity {
		t.Errorf("default Capacity = %d, want %d", got, DefaultCapacity)
	}
}

// TestWriteQueue_NoPanicOnSubmitDuringStop is the regression test for
// roborev #1504: the prior implementation closed q.ch in Stop, racing
// concurrent Submit goroutines into a "send on closed channel" panic.
// This test races 50 producers against Stop and asserts:
//  1. No panic.
//  2. Every Submit returns either nil (enqueued before Stop) or
//     ErrWriterUnavailable (rejected after Stop).
func TestWriteQueue_NoPanicOnSubmitDuringStop(t *testing.T) {
	const producers = 50
	const opsPerProducer = 20

	q := New(Config{Capacity: 256})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()

	op := func(_ context.Context) error { return nil }

	// Synchronize the producer kickoff so they all hit Submit at once.
	start := make(chan struct{})
	results := make(chan error, producers*opsPerProducer)
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			defer func() {
				if r := recover(); r != nil {
					results <- errors.New("PANIC: " + safeRecoverString(r))
				}
			}()
			for i := 0; i < opsPerProducer; i++ {
				results <- q.Submit(context.Background(), op)
			}
		}()
	}

	// Race producers and Stop. The window where they overlap is
	// exactly the window the old close(q.ch) code panicked in.
	close(start)
	// Yield briefly so producers actually start submitting before we
	// call Stop, but not so long that they all finish first.
	time.Sleep(time.Microsecond * 50)
	q.Stop(2 * time.Second)
	wg.Wait()
	close(results)

	var nilCount, unavailCount, otherCount int
	for err := range results {
		switch {
		case err == nil:
			nilCount++
		case errors.Is(err, ErrWriterUnavailable):
			unavailCount++
		case errors.Is(err, ErrQueueFull):
			// Acceptable: capacity-bounded backpressure.
			otherCount++
		default:
			t.Errorf("unexpected Submit error: %v", err)
			otherCount++
		}
	}

	// We expect SOME nil (pre-Stop) and SOME unavailable (post-Stop).
	// Tolerate any mix as long as no panic and no unexpected errors.
	if nilCount == 0 && unavailCount == 0 {
		t.Errorf("all submits errored unexpectedly: nil=%d unavail=%d other=%d",
			nilCount, unavailCount, otherCount)
	}
	t.Logf("submits: nil=%d unavail=%d other=%d (total=%d)",
		nilCount, unavailCount, otherCount, nilCount+unavailCount+otherCount)
}

// TestWriteQueue_SubmitSync_ReturnsOpResult verifies the blocking
// variant (roborev #1501 plumbing) waits for the consumer to run the op
// and surfaces the op's actual error verbatim — not nil, not a
// queue-level shim. The indexer's checkpoint advance relies on this.
func TestWriteQueue_SubmitSync_ReturnsOpResult(t *testing.T) {
	q := New(Config{Capacity: 4})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer q.Stop(time.Second)

	// Success path.
	var ran atomic.Bool
	if err := q.SubmitSync(context.Background(), func(context.Context) error {
		ran.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("SubmitSync success path returned %v, want nil", err)
	}
	if !ran.Load() {
		t.Error("SubmitSync returned before op ran")
	}

	// Failure path — op error must surface verbatim.
	wantErr := errors.New("simulated commit failure")
	if err := q.SubmitSync(context.Background(), func(context.Context) error {
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Errorf("SubmitSync failure path returned %v, want %v", err, wantErr)
	}
}

// TestWriteQueue_SubmitSync_StoppedQueueRejects verifies SubmitSync
// returns ErrWriterUnavailable when the queue has already been Stop()'d,
// so callers (indexer) can refuse to advance their checkpoint.
func TestWriteQueue_SubmitSync_StoppedQueueRejects(t *testing.T) {
	q := New(Config{Capacity: 2})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	q.Stop(time.Second)

	err := q.SubmitSync(context.Background(), func(context.Context) error {
		t.Error("op should not have run on a stopped queue")
		return nil
	})
	if !errors.Is(err, ErrWriterUnavailable) {
		t.Errorf("SubmitSync on stopped queue returned %v, want ErrWriterUnavailable", err)
	}
}

// safeRecoverString turns a recover() value into a string for test
// assertions without panicking if it's not stringer-compatible.
func safeRecoverString(r any) string {
	switch v := r.(type) {
	case string:
		return v
	case error:
		return v.Error()
	default:
		return "non-string panic value"
	}
}
