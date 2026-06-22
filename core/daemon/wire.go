package daemon

// Wire protocol for the per-project write-owner socket (plan-bb91616a
// slice-1). The envelope + ack are newline-delimited JSON over the Unix
// socket: client writes one envelope line, server replies with one ack
// line. NDJSON framing keeps the transport dependency-free (no length
// prefixes, no protobuf) and lets a future global coordinator
// (trk-cb80b7da) read the same frames.

// OpFormatVersion is the wire-compatibility version of the Envelope. A
// newer client hitting an older daemon (or vice-versa) is rejected with an
// error ack rather than risking a mis-applied write (slice-1 version-skew
// decision). Bump this only on a breaking envelope/ack change.
//
// Version history:
//   - 1: original synchronous applied-ack envelope (slice-1).
//   - 2: async ack semantics (Envelope.Async / AckEnqueued, slice-1) PLUS the
//     type-tagged DerivedOp.Args wire shape (round-3, roborev-478 finding 3).
//     Both changed how a peer must interpret the payload/ack contract: an old
//     (v1) daemon has no AckEnqueued path, so it would apply an async op
//     synchronously and a hot caller waiting on the ack could blow its <1s
//     budget; and it decodes DerivedOp.Args with the pre-round-3 untagged
//     shape, mis-typing the bind args. Bumping to 2 makes the daemon's
//     existing version-skew check (process, socket.go) reject any v1-vs-v2
//     mismatch with an AckError, so the client treats it as a route-miss and
//     falls back to the bounded direct write — never a silent mis-apply
//     (roborev-480 finding 2).
const OpFormatVersion = 2

// AckStatus is the outcome the daemon reports for a submitted op.
type AckStatus string

const (
	// AckApplied — the op ran through the writequeue and committed. This is
	// the outcome for a SYNCHRONOUS (default, Async=false) submission: the
	// ack reflects the real commit result, so a caller can depend on the
	// write having happened (the typed CLI routes and the applied-ack
	// RouteSQL rely on it).
	AckApplied AckStatus = "applied"
	// AckDuplicate — the op_id was already applied (sync) or already
	// enqueued (async) within the dedup window; the op was NOT re-run /
	// re-enqueued (idempotent retry / spool replay).
	AckDuplicate AckStatus = "duplicate"
	// AckEnqueued — the op was durably handed to the single-writer queue and
	// the daemon acked IMMEDIATELY, WITHOUT waiting for it to apply. This is
	// the outcome for an ENQUEUE-ONLY (Envelope.Async) submission. It bounds
	// hot-path latency to a sub-millisecond local round-trip even when another
	// writer holds the lock, at the cost of not reflecting the apply result
	// (roborev 451/452): a hot hook waiting on AckApplied could exceed its
	// <1s budget whenever the writer is busy. FIFO single-writer ordering
	// still guarantees the op applies after every op enqueued before it; the
	// canonical NDJSON write + reindex remain the durability backstop if a
	// crash loses a queued-but-unapplied op. A caller that needs apply
	// confirmation MUST use a synchronous submission (AckApplied) instead.
	AckEnqueued AckStatus = "enqueued"
	// AckError — the op could not be applied (sync) or could not be enqueued
	// (async — e.g. the queue is full / not running). Error carries the
	// reason. Unknown op_format_version always yields this status.
	AckError AckStatus = "error"
)

// Envelope is the versioned write-op the client submits. project_id makes
// the protocol coordinator-ready (slice-1 constraint): a global coordinator
// can route by project without a wire change. payload is the op-type-specific
// body, opaque to the transport — MVP-3 fills it with the dbgate derived-op
// representation; MVP-2 carries it verbatim to the registered op applier.
type Envelope struct {
	OpFormatVersion int    `json:"op_format_version"`
	OpID            string `json:"op_id"`
	OpType          string `json:"op_type"`
	ProjectID       string `json:"project_id,omitempty"`
	Payload         []byte `json:"payload,omitempty"`

	// Async selects the ack-timing mode. False (the default, and the only
	// mode the typed CLI routes use) is SYNCHRONOUS: the daemon funnels the
	// op through SubmitSync and acks AckApplied only after it commits. True is
	// ENQUEUE-ONLY: the daemon hands the op to the single-writer queue
	// (async Submit) and acks AckEnqueued the instant it is durably queued,
	// without waiting for apply. omitempty keeps the default-false wire form
	// byte-identical to the pre-existing envelope, so an older daemon that
	// ignores the field still behaves correctly (it speaks the same
	// op_format_version and simply applies synchronously). See AckEnqueued for
	// the durability contract.
	Async bool `json:"async,omitempty"`
}

// Ack is the daemon's reply. Seq is a monotonic per-listener counter
// assigned to every accepted submission (applied or duplicate) so SSE
// consumers (Phase C) can resume from a known position. Error is set only
// when Status == AckError.
type Ack struct {
	Status AckStatus `json:"status"`
	Seq    int64     `json:"seq"`
	Error  string    `json:"error,omitempty"`
}
