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
const OpFormatVersion = 1

// AckStatus is the outcome the daemon reports for a submitted op.
type AckStatus string

const (
	// AckApplied — the op ran through the writequeue and committed.
	AckApplied AckStatus = "applied"
	// AckDuplicate — the op_id was already applied within the dedup
	// window; the op was NOT re-run (idempotent retry / spool replay).
	AckDuplicate AckStatus = "duplicate"
	// AckError — the op could not be applied. Error carries the reason.
	// Unknown op_format_version always yields this status.
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
