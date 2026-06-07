// Package writequeue implements the single-writer transport that
// serializes SQLite write operations against the per-project DB. It is
// the slice-6 deliverable of plan-ae0c37b2 — the architectural
// counter-measure to the SQLITE_BUSY contention the plan targets.
//
// CANONICAL-FIRST CONTRACT (HIGH-risk architectural rule):
//
//	Producers MUST persist user work to canonical storage (NDJSON / HTML)
//	BEFORE submitting to the queue. The queue is the derived-index update
//	channel only. If Submit returns ErrQueueFull / ErrTimeout /
//	ErrWriterUnavailable, the producer keeps moving — the canonical
//	NDJSON is already on disk and reindex will recover the SQLite index
//	on the next start.
//
//	WRONG (loses user work on queue overflow):
//	    if err := q.Submit(ctx, op); err == nil {
//	        appendNDJSON(...)
//	    }
//
//	RIGHT (canonical always wins):
//	    appendNDJSON(...)            // user-work durability
//	    _ = q.Submit(ctx, op)        // best-effort index update
//
// SINGLE-OWNER INVARIANT:
//
//	Exactly one Queue per project DB, owned by `wipnote serve` for v1.
//	A second writer process (`wipnote daemon`) is the post-launch
//	graduation path (plan q-service-owner). Inside the process every
//	producer in serve — indexer, OTLP receiver, sub-agent auto-ingest —
//	submits to the same Queue instance.
package writequeue

import "context"

// WriteOp is the producer-supplied closure the worker executes against
// the writable DB handle. The op runs in the writer goroutine's
// goroutine, never in the producer's, so the producer is free to
// continue once Submit returns.
//
// Implementations should be self-contained: they capture whatever
// arguments they need (signals, batches, IDs) and call into the underlying
// *receiver.Writer or *sql.DB at execute time. Errors returned from a
// WriteOp are surfaced via stats counters and logged, but they never
// flow back to the original producer — the producer has already moved
// on after the canonical NDJSON write.
type WriteOp func(ctx context.Context) error
