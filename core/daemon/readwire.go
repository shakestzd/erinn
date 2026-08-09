package daemon

import "encoding/json"

// Read protocol for the per-project daemon socket (feat-f6759e37).
//
// WHY THIS EXISTS. Hooks are fresh OS processes that fire on every tool call,
// so they cannot afford to parse canonical state — measured at roughly 100ms
// warm and 250ms cold against a 1,000-file corpus. The process spawn is fixed
// by the harness contract and cannot be avoided; the PARSE can, by amortising
// it in the resident daemon that already owns the socket, the write queue and
// background maintenance. That — and only that — is why hooks have historically
// read the derived SQLite index for work-item state. This protocol removes that
// reason.
//
// FRAMING. Reads share the Envelope's NDJSON transport (one request line, one
// response line) but are a DISTINCT frame, discriminated by the non-empty
// "read_op" field. The listener peeks at that field before choosing a decoder,
// so a write Envelope and a ReadRequest can never be confused for one another.
//
// VERSION SKEW IS DELIBERATELY LOUD. A ReadRequest carries read_format_version
// and NO op_format_version. An OLDER daemon (one built before this protocol)
// decodes the frame as an Envelope, sees op_format_version 0, and replies with
// an AckError — it can never mis-serve a read as a write. The client detects
// the missing read_status and reports the daemon as not speaking the read
// protocol. There is no silent degradation in either direction: a quiet
// fallback to the derived index is how two data paths diverge unnoticed, which
// is the defect class this whole track exists to remove.

// ReadFormatVersion is the wire-compatibility version of ReadRequest/
// ReadResponse. Bump only on a breaking change to either shape; a mismatch is
// rejected with ReadStatusUnsupported rather than answered on a guess.
//
// Version history:
//   - 1: initial read protocol (ping, session.attach, workitem.get,
//     workitem.list).
const ReadFormatVersion = 1

// Read op names. The set is deliberately CLOSED and small: these are exactly
// the queries hooks make against work-item state, not a general query
// protocol. Adding a general query surface would recreate the derived index
// behind a socket.
const (
	// ReadOpPing is the capability + liveness probe. It is what a launcher
	// uses to decide whether a resident daemon speaks this protocol at all,
	// before it promises availability to the session's hooks.
	ReadOpPing = "ping"

	// ReadOpAttach registers a launcher process with the daemon so idle-exit
	// is suppressed for as long as that process lives. See PingResult and
	// Listener.Attach for the ownership rationale.
	ReadOpAttach = "session.attach"

	// ReadOpWorkItemGet resolves one work item by ID from canonical state.
	ReadOpWorkItemGet = "workitem.get"

	// ReadOpWorkItemList lists work items filtered by track and/or status.
	ReadOpWorkItemList = "workitem.list"
)

// ReadStatus is the outcome the daemon reports for a read request.
type ReadStatus string

const (
	// ReadStatusOK — the request was served; Result holds the op-specific body.
	ReadStatusOK ReadStatus = "ok"
	// ReadStatusUnsupported — the daemon does not speak this read_format_version,
	// or does not know this read_op, or has no Reader wired. The caller must
	// treat this as "no read path here", never as an empty result.
	ReadStatusUnsupported ReadStatus = "unsupported"
	// ReadStatusError — the daemon understood the request but could not serve
	// it (canonical state unreadable, malformed args). Error carries why.
	ReadStatusError ReadStatus = "error"
)

// ReadRequest is one read frame. ProjectID mirrors Envelope.ProjectID so a
// future global coordinator (trk-cb80b7da) can route reads by project without
// a wire change. Args is the op-specific body, opaque to the transport.
type ReadRequest struct {
	// ReadOp is the discriminator: a non-empty value marks this line as a read
	// frame rather than a write Envelope. It is first in the struct so it also
	// lands first in the marshalled JSON, which keeps the peek cheap.
	ReadOp string `json:"read_op"`

	ReadFormatVersion int             `json:"read_format_version"`
	ProjectID         string          `json:"project_id,omitempty"`
	Args              json.RawMessage `json:"args,omitempty"`
}

// ReadResponse is the daemon's reply to a ReadRequest. Result is set only when
// Status is ReadStatusOK; Error only when Status is ReadStatusError or
// ReadStatusUnsupported.
//
// ReadStatus is a REQUIRED field with no omitempty: its presence is how a
// client distinguishes this protocol's reply from an older daemon's Ack (which
// carries "status" holding an AckStatus, not a ReadStatus). See readResponseOK.
type ReadResponse struct {
	ReadStatus        ReadStatus      `json:"read_status"`
	ReadFormatVersion int             `json:"read_format_version"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             string          `json:"error,omitempty"`

	// Cache reports how the daemon satisfied this request. It exists so
	// invalidation can be tested NON-VACUOUSLY: a test can assert that a read
	// following a canonical write was a miss (re-parsed) rather than a hit
	// (served from a stale entry). It is diagnostic only — no caller branches
	// on it.
	Cache *CacheStats `json:"cache,omitempty"`
}

// CacheStats reports per-request cache accounting for the canonical work-item
// cache. Hits are entries served from memory after revalidation proved them
// current; Misses are entries (re)parsed from disk during this request.
type CacheStats struct {
	Hits   int `json:"hits"`
	Misses int `json:"misses"`
}

// PingResult is the ReadOpPing body. It tells a launcher everything it needs
// to decide whether to promise daemon availability to this session's hooks.
type PingResult struct {
	ReadFormatVersion int    `json:"read_format_version"`
	PID               int    `json:"pid"`
	SocketPath        string `json:"socket_path"`
	// AttachedPIDs is how many live launcher processes currently suppress
	// idle-exit. Diagnostic.
	AttachedPIDs int `json:"attached_pids"`
}

// AttachArgs is the ReadOpAttach body. PID is the launcher process whose
// lifetime the daemon should outlive.
type AttachArgs struct {
	PID int `json:"pid"`
}

// AttachResult reports the attach outcome.
type AttachResult struct {
	Attached     bool `json:"attached"`
	AttachedPIDs int  `json:"attached_pids"`
}

// WorkItemGetArgs is the ReadOpWorkItemGet body.
type WorkItemGetArgs struct {
	ID string `json:"id"`
}

// WorkItem is the projection of canonical work-item state that hooks actually
// need. It is deliberately NARROW: every field here replaces a column a hook
// used to SELECT from the derived index. Widening it is how a read protocol
// turns into a second index, so add a field only when a real hook query needs
// it.
type WorkItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Title   string `json:"title,omitempty"`
	TrackID string `json:"track_id,omitempty"`
}

// WorkItemGetResult is the ReadOpWorkItemGet reply. Found distinguishes "no
// such work item" from "a work item with empty status" — a caller must never
// have to infer absence from a zero value.
type WorkItemGetResult struct {
	Found bool     `json:"found"`
	Item  WorkItem `json:"item,omitempty"`
}

// WorkItemListArgs is the ReadOpWorkItemList body. All filters are ANDed; an
// empty filter matches everything. Statuses and Types are ORed within
// themselves. Limit <= 0 means unlimited.
type WorkItemListArgs struct {
	TrackID  string   `json:"track_id,omitempty"`
	Statuses []string `json:"statuses,omitempty"`
	Types    []string `json:"types,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

// WorkItemListResult is the ReadOpWorkItemList reply. Items is ordered
// deterministically by the server (in-progress first, then by type, then by
// creation time descending) so callers never depend on filesystem order.
type WorkItemListResult struct {
	Items []WorkItem `json:"items"`
}
