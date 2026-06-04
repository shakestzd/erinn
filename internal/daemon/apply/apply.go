// Package apply defines the hook-derived write-op payload encoding and the
// daemon Applier that reproduces, on the writer side, the SAME derived-index
// write the hook tree would otherwise perform via a direct DB open
// (internal/hooks/dbgate.go SubmitDerivedOp).
//
// MVP-3 (feat-075c110d, plan-bb91616a slice-4): the hook side builds a typed,
// serializable DerivedOp, ships it over the per-project writer socket, and the
// writer-owning process decodes it back into the identical SQL the hook would
// have run. Because the op is funnelled through the single-writer queue, the
// hook subprocess no longer needs its own writable SQLite handle when the
// daemon is up — eliminating the cross-process SQLITE_BUSY contention the plan
// targets. When the daemon is NOT up, the dbgate caller falls back to the
// existing direct-open path, so behaviour is identical to today.
//
// SCOPE: this package encodes the ONE canonical hook-derived index write —
// the agent_events upsert (db.UpsertEvent). It is the representative
// derived-index mutation hooks emit. Additional op types are added by
// extending DerivedOp.Type + the Applier dispatch; the wire envelope and
// transport are unchanged.
package apply

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/shakestzd/wipnote/internal/daemon"
	"github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/db/writequeue"
	"github.com/shakestzd/wipnote/internal/models"
)

// OpTypeAgentEventUpsert is the op_type for the agent_events upsert — the
// canonical hook-derived index write. It maps to db.UpsertEvent, the SAME
// INSERT OR REPLACE the synchronous dbgate path would run.
const OpTypeAgentEventUpsert = "agent_event.upsert"

// MVP-4 (feat-075c110d): the two highest-contention CLI derived-index writes
// are routed through the same writer daemon as the hook tree. Each maps to the
// IDENTICAL db.* call the direct CLI path runs today.
const (
	// OpTypeFeatureStatus is the work-item status transition emitted by
	// feature/bug/spike start + complete (internal/workitem). It maps to
	// db.UpdateFeatureStatus — the bug-74a7bda7 user-visible contended write.
	OpTypeFeatureStatus = "feature.status"

	// OpTypeSessionInsert is the session-row insert emitted by
	// `wipnote session start` (cmd/wipnote/session.go). Maps to db.InsertSession.
	OpTypeSessionInsert = "session.insert"

	// OpTypeSessionStatus is the session status transition emitted by
	// `wipnote session end` (cmd/wipnote/session.go). Maps to
	// db.UpdateSessionStatus.
	OpTypeSessionStatus = "session.status"
)

// DerivedOp is the serializable representation of one derived index write.
// Type selects the apply dispatch; the type-specific body lives in the
// remaining fields. The agent_events upsert (Event) is the hook-tree op;
// the Feature*/Session* fields carry the MVP-4 CLI work-item / session ops.
//
// DerivedOp is JSON-encoded into Envelope.Payload. Keeping it a single typed
// struct (rather than an opaque closure) is what makes the op cross a process
// boundary — the direct path's historical `func(*sql.DB) error` closure could
// not be shipped to the writer.
type DerivedOp struct {
	Type  string             `json:"type"`
	Event *models.AgentEvent `json:"event,omitempty"`

	// MVP-4 fields. FeatureID + Status carry OpTypeFeatureStatus and the
	// session status op; Session carries the full row for OpTypeSessionInsert;
	// SessionID + Status carry OpTypeSessionStatus.
	FeatureID string          `json:"feature_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Status    string          `json:"status,omitempty"`
	Session   *models.Session `json:"session,omitempty"`
}

// Encode marshals a DerivedOp for Envelope.Payload.
func Encode(op DerivedOp) ([]byte, error) {
	return json.Marshal(op)
}

// Decode unmarshals an Envelope.Payload back into a DerivedOp.
func Decode(payload []byte) (DerivedOp, error) {
	var op DerivedOp
	if err := json.Unmarshal(payload, &op); err != nil {
		return DerivedOp{}, fmt.Errorf("decode derived op: %w", err)
	}
	return op, nil
}

// OpID derives the dedup key for an event op from the session ID and a
// per-event sequence/offset (plan slice-4: op_id = hash(sessionID + seq)).
// The hash keeps the key bounded and opaque while remaining deterministic so
// a replayed op (retry / spool) dedups against its first application.
func OpID(sessionID string, seq int64) string {
	h := sha256.Sum256([]byte(sessionID + ":" + strconv.FormatInt(seq, 10)))
	return hex.EncodeToString(h[:16])
}

// NewApplier returns a daemon.Applier bound to the writer-owned writable DB
// handle. It dispatches by op_type and returns a writequeue.WriteOp that runs
// the identical mutation the hook tree's direct path would run. An unknown
// op_type returns an error so the listener acks error (never a mis-applied
// write).
//
// The returned closure runs on the listener goroutine and is cheap +
// side-effect-free: the inner WriteOp (which actually touches the DB) executes
// later inside the single-writer queue worker.
func NewApplier(database *sql.DB) daemon.Applier {
	return func(env daemon.Envelope) (writequeue.WriteOp, error) {
		op, err := Decode(env.Payload)
		if err != nil {
			return nil, err
		}
		switch op.Type {
		case OpTypeAgentEventUpsert:
			if op.Event == nil {
				return nil, fmt.Errorf("%s: nil event", OpTypeAgentEventUpsert)
			}
			ev := *op.Event // capture by value so the closure is self-contained
			return func(_ context.Context) error {
				return db.UpsertEvent(database, &ev)
			}, nil
		case OpTypeFeatureStatus:
			if op.FeatureID == "" {
				return nil, fmt.Errorf("%s: empty feature_id", OpTypeFeatureStatus)
			}
			id, status := op.FeatureID, op.Status
			return func(_ context.Context) error {
				return db.UpdateFeatureStatus(database, id, status)
			}, nil
		case OpTypeSessionInsert:
			if op.Session == nil {
				return nil, fmt.Errorf("%s: nil session", OpTypeSessionInsert)
			}
			s := *op.Session // capture by value so the closure is self-contained
			return func(_ context.Context) error {
				return db.InsertSession(database, &s)
			}, nil
		case OpTypeSessionStatus:
			if op.SessionID == "" {
				return nil, fmt.Errorf("%s: empty session_id", OpTypeSessionStatus)
			}
			sid, status := op.SessionID, op.Status
			return func(_ context.Context) error {
				return db.UpdateSessionStatus(database, sid, status)
			}, nil
		default:
			return nil, fmt.Errorf("unknown derived op_type %q", op.Type)
		}
	}
}
