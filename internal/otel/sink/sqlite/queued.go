package sqlite

import (
	"context"
	"errors"

	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/internal/otel"
	"github.com/shakestzd/wipnote/internal/otel/sink"
)

// QueuedSink is a sink.SignalSink that routes every WriteBatch through
// the single-writer queue introduced by slice 6 (feat-f3bcbcef,
// plan-ae0c37b2). It exists so that the indexer + OTLP HTTP receiver
// share exactly one writable SQLite connection — the queue worker's —
// instead of opening their own (which is what created the SQLITE_BUSY
// contention the plan targets).
//
// CANONICAL-FIRST CONTRACT:
//
//	Producers MUST persist user work to canonical NDJSON/HTML BEFORE
//	calling WriteBatch on this sink. Specifically:
//
//	  - The indexer reads NDJSON as its input — the canonical write
//	    happened upstream by the per-session collector. Submitting to
//	    the queue is purely a derived-index update.
//
//	  - The OTLP HTTP receiver, when wired in serve, must first append
//	    canonical NDJSON via the ndjson sink, THEN call this sink. We
//	    document the contract here so the upcoming receiver wiring
//	    follows the rule.
//
// On queue-side errors (ErrQueueFull / ErrTimeout / ErrWriterUnavailable)
// WriteBatch returns nil — the canonical write already preserved user
// work, and propagating an error here would tempt callers to retry into
// an already-overloaded writer. The queue's Stats counters expose
// rejection metrics for the dashboard collector-status panel.
type QueuedSink struct {
	queue *writequeue.Queue
	inner WriterCloser
}

// NewQueued wraps inner in a queue-backed sink. The caller is
// responsible for starting the queue (q.Start) and the inner Writer's
// lifecycle (close on shutdown). Returns a *QueuedSink (not the
// interface) so callers can introspect the underlying queue for
// diagnostics.
func NewQueued(q *writequeue.Queue, inner WriterCloser) *QueuedSink {
	return &QueuedSink{queue: q, inner: inner}
}

// WriteBatch submits a closure-wrapped WriteBatch to the queue. The
// closure captures harness/resourceAttrs/signals so the consumer
// goroutine can run the actual write later without touching the
// producer's frame.
//
// Returns nil on queue rejection (canonical-first contract — see type
// doc). The op-side error path is observable through queue Stats.
func (s *QueuedSink) WriteBatch(ctx context.Context, harness otel.Harness, resourceAttrs map[string]any, signals []otel.UnifiedSignal) error {
	if len(signals) == 0 {
		return nil
	}
	op := func(opCtx context.Context) error {
		_, err := s.inner.WriteBatch(opCtx, harness, resourceAttrs, signals)
		return err
	}
	err := s.queue.Submit(ctx, op)
	if err == nil {
		return nil
	}
	// Best-effort fallback: queue full / writer unavailable / timeout
	// all map to "canonical NDJSON already won, drop the index update".
	// We return nil so the producer's Stop/Tick path treats the
	// canonical write as authoritative.
	if errors.Is(err, writequeue.ErrQueueFull) ||
		errors.Is(err, writequeue.ErrWriterUnavailable) ||
		errors.Is(err, writequeue.ErrTimeout) {
		return nil
	}
	// Anything else (e.g. ctx.Err() from a cancelled producer context)
	// surfaces to the caller verbatim.
	return err
}

// WriteBatchSync submits the batch through the queue and BLOCKS until
// the consumer commits it (or the queue rejects/cancels). Returns the
// inner Writer's actual error so callers can distinguish "indexed
// successfully" from "queue full / writer unavailable / op error".
//
// Unlike WriteBatch — which fire-and-forgets after Submit and swallows
// queue rejection errors as nil to preserve the canonical-first
// contract — WriteBatchSync exists for callers whose own durable state
// depends on the SQLite commit succeeding. The indexer's
// `.index-offset` checkpoint is the canonical example: advancing it
// must follow, not precede, the actual DB write (roborev #1501).
//
// Callers must NOT advance their checkpoint when this returns a
// non-nil error. On ErrQueueFull / ErrWriterUnavailable / ErrTimeout
// the indexer should retry on the next tick — the canonical NDJSON
// is unchanged, so a retry is safe and idempotent (INSERT OR IGNORE
// in the underlying Writer).
func (s *QueuedSink) WriteBatchSync(ctx context.Context, harness otel.Harness, resourceAttrs map[string]any, signals []otel.UnifiedSignal) error {
	if len(signals) == 0 {
		return nil
	}
	op := func(opCtx context.Context) error {
		_, err := s.inner.WriteBatch(opCtx, harness, resourceAttrs, signals)
		return err
	}
	return s.queue.SubmitSync(ctx, op)
}

// Close releases the underlying writer. The caller MUST stop the queue
// FIRST (via queue.Stop) so the consumer drains any pending ops before
// the writer's prepared statements are torn down — otherwise an
// in-flight op would see a closed *sql.Conn and fail.
func (s *QueuedSink) Close() error { return s.inner.Close() }

// SyncSignalSink is an optional interface that signal sinks may
// implement to expose a synchronous write path. Callers (notably the
// indexer) can type-assert and use the sync variant when correctness
// requires "DB commit before I move my checkpoint forward".
//
// Defined in this package rather than internal/otel/sink to keep that
// package free of writequeue knowledge — the sink package is a thin
// abstraction; sync semantics are an implementation concern of the
// queued sqlite sink.
type SyncSignalSink interface {
	WriteBatchSync(ctx context.Context, harness otel.Harness, resourceAttrs map[string]any, signals []otel.UnifiedSignal) error
}

// Compile-time check that QueuedSink satisfies SyncSignalSink.
var _ SyncSignalSink = (*QueuedSink)(nil)

// Queue exposes the underlying queue so the dashboard collector-status
// handler can read Stats without holding a separate reference.
func (s *QueuedSink) Queue() *writequeue.Queue { return s.queue }

// Compile-time interface check.
var _ sink.SignalSink = (*QueuedSink)(nil)
