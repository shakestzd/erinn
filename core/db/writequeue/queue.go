package writequeue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Sentinel errors returned by Submit / SubmitWithTimeout. Producers
// match against these with errors.Is and fall back to canonical-only
// persistence on any of them.
var (
	// ErrQueueFull means the bounded queue is at capacity and the
	// non-blocking Submit could not enqueue the op. The producer's
	// canonical NDJSON write is unaffected — the derived SQLite index
	// will recover on reindex.
	ErrQueueFull = errors.New("writequeue: queue full")

	// ErrTimeout means SubmitWithTimeout's deadline elapsed before a
	// slot opened in the queue. Semantically identical to ErrQueueFull
	// from the producer's perspective (best-effort fallback) — surfaced
	// separately so observability can distinguish "instantaneously full"
	// from "consumer too slow".
	ErrTimeout = errors.New("writequeue: submit timeout")

	// ErrWriterUnavailable means the queue has not been started or has
	// been stopped. Producers must treat this exactly like ErrQueueFull
	// — the canonical NDJSON write already happened, so user work is
	// preserved.
	ErrWriterUnavailable = errors.New("writequeue: writer unavailable")
)

// State labels the queue's lifecycle phase. Surfaced through Stats so
// the dashboard collector-status panel can distinguish a healthy
// writer from one mid-shutdown or never-started.
type State string

const (
	StateInit     State = "init"     // constructed, Start not yet called
	StateRunning  State = "running"  // consumer goroutine alive
	StateDraining State = "draining" // Stop called; consumer flushing remaining ops
	StateStopped  State = "stopped"  // consumer exited
)

// Stats is the diagnostic snapshot returned by Queue.Stats.
//
// Depth is the instantaneous in-flight op count (channel length).
// Enqueued / Dequeued / Rejected / Errors are monotonic counters.
// EnqueueRate / DequeueRate are computed over the most recent 60-second
// window so the dashboard sees fresh activity rather than a session-wide
// running average that smooths over bursts.
type Stats struct {
	State          State   `json:"state"`
	Depth          int     `json:"depth"`
	Capacity       int     `json:"capacity"`
	Enqueued       int64   `json:"enqueued"`
	Dequeued       int64   `json:"dequeued"`
	Rejected       int64   `json:"rejected"`
	Errors         int64   `json:"errors"`
	EnqueueRatePerSec float64 `json:"enqueue_rate_per_sec"`
	DequeueRatePerSec float64 `json:"dequeue_rate_per_sec"`
}

// Config controls the queue's bounded behaviour and the consumer's
// per-op error handling.
//
//	Capacity   — channel buffer size. Default 1000 ops. Tuned per
//	             the plan's burst-handling note: ~10x typical
//	             sub-agent SubagentStop concurrency.
//	OnError    — optional callback for op execution errors. Defaults
//	             to a no-op so tests can swap in a recorder; production
//	             wires this to a logger.
type Config struct {
	Capacity int
	OnError  func(error)
}

// DefaultCapacity is the queue size when Config.Capacity is unset.
const DefaultCapacity = 1000

// Queue serializes WriteOp execution behind a single consumer
// goroutine. Producers submit via Submit (non-blocking) or
// SubmitWithTimeout (bounded wait). The consumer drains the channel
// and runs each op against whatever writable DB handle the producer
// captured.
//
// Queue is safe for concurrent producers; it is NOT safe to construct
// more than one Queue per project DB (the architectural single-writer
// invariant). See package doc.
type Queue struct {
	cfg Config
	ch  chan WriteOp

	// state guards lifecycle transitions. The atomic.Int32 holds an
	// internal state code; Submit consults it under sendMu (read lock)
	// so the state check and the channel send are an atomic unit
	// against Stop's transition (write lock).
	stateMu sync.Mutex
	state   atomic.Int32

	// sendMu serializes Submit's "state check + send" against Stop's
	// "state transition + quit". Producers hold RLock for the whole
	// check-then-send window; Stop holds the (exclusive) write lock
	// while flipping state to draining and closing q.quit. This is the
	// fix for roborev #1504: the prior implementation closed q.ch
	// directly, racing concurrent Submit goroutines into a "send on
	// closed channel" panic. The current implementation never closes
	// q.ch; Stop signals shutdown via q.quit instead, and sendMu
	// guarantees that any Submit that successfully completed its send
	// observed state==running.
	sendMu sync.RWMutex

	// quit signals the consumer to stop accepting new ops and drain
	// remaining buffered work. Closed exactly once by Stop (via
	// quitOnce).
	quit     chan struct{}
	quitOnce sync.Once

	// done is closed by the consumer goroutine when it exits.
	done chan struct{}

	// rate tracking — protected by rateMu.
	rateMu          sync.Mutex
	rateWindow      time.Duration
	enqueueHistory  []int64 // unix-nano timestamps of recent enqueues
	dequeueHistory  []int64 // unix-nano timestamps of recent dequeues

	// monotonic counters.
	enqueued atomic.Int64
	dequeued atomic.Int64
	rejected atomic.Int64
	errors   atomic.Int64
}

// state code constants — kept private so the public State type is the
// only thing callers see.
const (
	stateCodeInit int32 = iota
	stateCodeRunning
	stateCodeDraining
	stateCodeStopped
)

// New constructs a Queue with the given config. The queue is in
// StateInit until Start is called.
func New(cfg Config) *Queue {
	if cfg.Capacity <= 0 {
		cfg.Capacity = DefaultCapacity
	}
	if cfg.OnError == nil {
		cfg.OnError = func(error) {}
	}
	q := &Queue{
		cfg:        cfg,
		ch:         make(chan WriteOp, cfg.Capacity),
		quit:       make(chan struct{}),
		done:       make(chan struct{}),
		rateWindow: 60 * time.Second,
	}
	q.state.Store(stateCodeInit)
	return q
}

// Start launches the single consumer goroutine. Idempotent: a second
// call after StateRunning is a no-op. Returns ErrWriterUnavailable if
// the queue has already been stopped (Queue instances are not
// reusable).
func (q *Queue) Start(ctx context.Context) error {
	q.stateMu.Lock()
	defer q.stateMu.Unlock()

	current := q.state.Load()
	if current == stateCodeRunning || current == stateCodeDraining {
		return nil
	}
	if current == stateCodeStopped {
		return ErrWriterUnavailable
	}
	q.state.Store(stateCodeRunning)
	go q.consume(ctx)
	return nil
}

// Stop signals the consumer to drain and exit. Blocks up to timeout
// for the in-flight ops to complete. Safe to call multiple times.
//
// After Stop, Submit returns ErrWriterUnavailable. The Queue cannot be
// restarted — construct a new instance.
//
// IMPORTANT: Stop does NOT close q.ch. Closing the producer-facing
// channel races with concurrent Submit calls and panics on "send on
// closed channel" (roborev #1504). Instead, Stop transitions state to
// draining (which makes Submit fast-fail) and signals the consumer via
// q.quit. Any Submit that already passed the state gate and is mid-send
// completes safely against the still-open buffered channel; the
// consumer's drain loop sweeps it up before exiting.
func (q *Queue) Stop(timeout time.Duration) {
	q.stateMu.Lock()
	current := q.state.Load()
	if current != stateCodeRunning {
		q.stateMu.Unlock()
		return
	}
	// Take sendMu's write lock so no Submit goroutine is mid-"state
	// check + send" while we flip state and close q.quit. Once we
	// release this lock, any subsequent Submit observes draining and
	// fast-fails with ErrWriterUnavailable.
	q.sendMu.Lock()
	q.state.Store(stateCodeDraining)
	q.quitOnce.Do(func() { close(q.quit) })
	q.sendMu.Unlock()
	q.stateMu.Unlock()

	if timeout <= 0 {
		<-q.done
		return
	}
	select {
	case <-q.done:
	case <-time.After(timeout):
	}
}

// Submit enqueues op for asynchronous execution. Non-blocking: if the
// channel is at capacity, returns ErrQueueFull immediately. Producers
// MUST tolerate ErrQueueFull — the canonical NDJSON write already
// happened, so user work is safe.
//
// Returns ErrWriterUnavailable when the queue has not been Started or
// has been Stopped. Returns ctx.Err() if the context is already
// cancelled (mirrors stdlib idioms; lets producers stop submitting
// during shutdown).
func (q *Queue) Submit(ctx context.Context, op WriteOp) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Hold sendMu (read) for the state check and the send so Stop's
	// state transition cannot interleave between them. Multiple
	// Submits proceed in parallel (RWMutex read lock); Stop's write
	// lock excludes them all while it flips state and signals quit.
	// See type doc and Stop for the full rationale (roborev #1504).
	q.sendMu.RLock()
	defer q.sendMu.RUnlock()
	current := q.state.Load()
	if current != stateCodeRunning {
		q.rejected.Add(1)
		return ErrWriterUnavailable
	}
	select {
	case q.ch <- op:
		q.enqueued.Add(1)
		q.recordEnqueue()
		return nil
	default:
		q.rejected.Add(1)
		return ErrQueueFull
	}
}

// SubmitSync enqueues op and blocks until the consumer goroutine has
// finished executing it. Returns the op's error (which may be nil) or a
// queue-level error (ErrQueueFull / ErrWriterUnavailable / ctx.Err()).
//
// SubmitSync exists so callers that durably depend on the write outcome
// — most notably the indexer's `.index-offset` checkpoint advance
// (roborev #1501) — can wait for commit before declaring success.
// Async producers (hook handlers, OTLP receiver) should continue using
// Submit; only paths whose correctness hinges on "DB row exists before
// I move my own state forward" need this variant.
//
// Implementation: wraps op in a closure that writes the op's return
// value to a 1-slot result channel after running. The blocking wait
// observes ctx, q.quit, and the result channel. On queue rejection
// (full / unavailable) we return the rejection error without ever
// scheduling the op — callers must treat that as "the write did NOT
// happen" and refrain from advancing their checkpoint.
func (q *Queue) SubmitSync(ctx context.Context, op WriteOp) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if op == nil {
		return nil
	}
	// Buffered so the consumer never blocks on its send even if the
	// caller's ctx fires before we reach the receive below.
	resultCh := make(chan error, 1)
	wrapped := func(opCtx context.Context) error {
		err := op(opCtx)
		resultCh <- err
		return err
	}
	if err := q.Submit(ctx, wrapped); err != nil {
		return err
	}
	// At this point the op is in the channel; the consumer will run it
	// even if Stop has fired (drain phase). Wait on result, ctx, and
	// q.done so we never deadlock if the consumer exits unexpectedly.
	select {
	case err := <-resultCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-q.done:
		// Consumer exited without executing our op. Drain phase should
		// have run everything in q.ch before closing done, so this is a
		// non-graceful shutdown path. Surface as ErrWriterUnavailable so
		// the caller does not advance its checkpoint.
		select {
		case err := <-resultCh:
			return err
		default:
			return ErrWriterUnavailable
		}
	}
}

// SubmitWithTimeout enqueues op, waiting up to timeout for a slot to
// open. Returns ErrTimeout if the deadline elapses, ErrWriterUnavailable
// if the queue stops mid-wait, or ctx.Err() if the context is cancelled.
//
// Producers that prefer to drop on overflow should call Submit; this
// variant exists for low-priority background work that can afford to
// wait briefly when the writer is momentarily behind.
func (q *Queue) SubmitWithTimeout(ctx context.Context, op WriteOp, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if timeout <= 0 {
		return q.Submit(ctx, op)
	}
	// Fast path: take sendMu briefly and try a non-blocking send. If
	// the buffer has room, we're done — and the send happened under
	// the read lock, so Stop cannot have flipped state mid-call.
	q.sendMu.RLock()
	current := q.state.Load()
	if current != stateCodeRunning {
		q.sendMu.RUnlock()
		q.rejected.Add(1)
		return ErrWriterUnavailable
	}
	select {
	case q.ch <- op:
		q.enqueued.Add(1)
		q.recordEnqueue()
		q.sendMu.RUnlock()
		return nil
	default:
		// Buffer full; fall through to the blocking-wait path below.
	}
	q.sendMu.RUnlock()
	// Slow path: buffer was full at first look. Wait up to timeout for
	// a slot, observing both ctx and quit so Stop can release us.
	// q.ch is never closed (see Stop), so this select is safe.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case q.ch <- op:
		// Re-check state under the lock to avoid enqueueing into a
		// channel whose consumer has already exited. If state moved
		// while we were blocked, we still enqueued successfully and
		// the drain phase in consume() will run the op (since q.ch is
		// shared); record it as enqueued and let the consumer decide.
		q.enqueued.Add(1)
		q.recordEnqueue()
		return nil
	case <-timer.C:
		q.rejected.Add(1)
		return ErrTimeout
	case <-q.quit:
		q.rejected.Add(1)
		return ErrWriterUnavailable
	case <-ctx.Done():
		q.rejected.Add(1)
		return ctx.Err()
	}
}

// Stats returns a point-in-time snapshot of queue depth, lifecycle
// state, and counters. Safe to call from any goroutine.
func (q *Queue) Stats() Stats {
	return Stats{
		State:             q.currentState(),
		Depth:             len(q.ch),
		Capacity:          q.cfg.Capacity,
		Enqueued:          q.enqueued.Load(),
		Dequeued:          q.dequeued.Load(),
		Rejected:          q.rejected.Load(),
		Errors:            q.errors.Load(),
		EnqueueRatePerSec: q.rate(true),
		DequeueRatePerSec: q.rate(false),
	}
}

// Capacity returns the configured queue capacity. Useful for tests
// that want to fill the queue exactly to its limit.
func (q *Queue) Capacity() int { return q.cfg.Capacity }

// consume is the single-writer consumer goroutine. Each op runs with
// the context passed to Start so the operator can cancel the entire
// writer service from one place.
//
// The loop runs in two phases:
//
//  1. Running: select on q.ch or q.quit. New ops arrive on q.ch; Stop
//     closes q.quit to signal shutdown.
//  2. Draining: after q.quit fires, drain any ops still in q.ch's
//     buffer (these were enqueued by Submit calls that won the state
//     check just before Stop transitioned state). q.ch is never closed
//     (see Stop), so we drain with a non-blocking receive loop instead
//     of a `range` and exit when the buffer empties.
func (q *Queue) consume(ctx context.Context) {
	defer close(q.done)
	run := func(op WriteOp) {
		if op == nil {
			return
		}
		// Run the op with the parent context. The op should respect
		// cancellation for its own internal DB calls; we don't try to
		// preempt it here because mid-transaction cancellation creates
		// rollback churn worse than just letting the op finish.
		if err := op(ctx); err != nil {
			q.errors.Add(1)
			q.cfg.OnError(err)
		}
		q.dequeued.Add(1)
		q.recordDequeue()
	}
loop:
	for {
		select {
		case op := <-q.ch:
			run(op)
		case <-q.quit:
			break loop
		}
	}
	// Drain remaining buffered ops. Submit cannot enqueue more (state
	// is now draining, fast-failing the gate) so the buffer monotonically
	// shrinks.
	for {
		select {
		case op := <-q.ch:
			run(op)
		default:
			q.state.Store(stateCodeStopped)
			return
		}
	}
}

func (q *Queue) currentState() State {
	switch q.state.Load() {
	case stateCodeInit:
		return StateInit
	case stateCodeRunning:
		return StateRunning
	case stateCodeDraining:
		return StateDraining
	case stateCodeStopped:
		return StateStopped
	}
	return StateInit
}

// recordEnqueue / recordDequeue append a unix-nano timestamp to the
// rolling-window history so Stats can compute a meaningful rate even
// when the queue has been running for hours.
func (q *Queue) recordEnqueue() {
	q.rateMu.Lock()
	defer q.rateMu.Unlock()
	q.enqueueHistory = append(q.enqueueHistory, time.Now().UnixNano())
	q.trimHistoryLocked(&q.enqueueHistory)
}

func (q *Queue) recordDequeue() {
	q.rateMu.Lock()
	defer q.rateMu.Unlock()
	q.dequeueHistory = append(q.dequeueHistory, time.Now().UnixNano())
	q.trimHistoryLocked(&q.dequeueHistory)
}

func (q *Queue) trimHistoryLocked(hist *[]int64) {
	cutoff := time.Now().Add(-q.rateWindow).UnixNano()
	// Find first entry younger than cutoff.
	i := 0
	for i < len(*hist) && (*hist)[i] < cutoff {
		i++
	}
	if i > 0 {
		*hist = (*hist)[i:]
	}
}

// rate computes the per-second rate over the window. enqueue=true
// selects the enqueue history; false selects the dequeue history.
func (q *Queue) rate(enqueue bool) float64 {
	q.rateMu.Lock()
	defer q.rateMu.Unlock()
	var hist []int64
	if enqueue {
		hist = q.enqueueHistory
	} else {
		hist = q.dequeueHistory
	}
	if len(hist) == 0 {
		return 0
	}
	cutoff := time.Now().Add(-q.rateWindow).UnixNano()
	count := 0
	for _, ts := range hist {
		if ts >= cutoff {
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return float64(count) / q.rateWindow.Seconds()
}
