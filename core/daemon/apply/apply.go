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
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/models"
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

// OpTypeSQL is the GENERIC SQL-envelope op (plan-2390966a slice-1, the
// foundation): it carries an arbitrary PARAMETERIZED statement (DerivedOp.SQL)
// plus its bind arguments (DerivedOp.Args) so the long tail of hot-path
// derived-index writes can route to the single writer WITHOUT hand-defining a
// typed op per write kind. The applier Execs the statement on the daemon's
// existing writable handle with the args bound as parameters (NEVER
// interpolated), wrapped in the shared RetryOnBusy backoff.
//
// SAFETY (constraint q-sql-safety): the daemon socket is a LOCAL, per-project,
// per-user Unix socket; only first-party wipnote code constructs OpTypeSQL ops;
// nothing crosses a network boundary. Args are always bound as SQL parameters,
// so even though the statement text is arbitrary, untrusted *values* cannot
// alter statement structure.
const OpTypeSQL = "sql.exec"

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

	// OpTypeSQL fields (plan-2390966a slice-1). SQL is the parameterized
	// statement; Args are its bind parameters, JSON-encoded for transport.
	// On decode, JSON numbers are read with json.Decoder.UseNumber() and
	// NORMALIZED (NormalizeArgs) so an integral value within int64 range arrives
	// as int64 (NOT float64) — preserving exact integer precision for values
	// above 2^53 (roborev-473 finding 6). Non-integral or out-of-range numbers
	// become float64; strings/bools/nil round-trip as-is. All are JSON-transport
	// -safe primitives the SQLite driver binds directly. Args are bound as SQL
	// parameters and are NEVER interpolated into SQL.
	SQL  string `json:"sql,omitempty"`
	Args []any  `json:"args,omitempty"`
}

// Encode marshals a DerivedOp for Envelope.Payload.
func Encode(op DerivedOp) ([]byte, error) {
	return json.Marshal(op)
}

// Decode unmarshals an Envelope.Payload back into a DerivedOp. JSON numbers in
// OpTypeSQL Args are decoded with UseNumber() and normalized so int64-range
// integers survive the round-trip exactly (roborev-473 finding 6): a plain
// json.Unmarshal would widen every JSON number to float64, silently truncating
// int64 values above 2^53 before they reach the SQLite bind.
func Decode(payload []byte) (DerivedOp, error) {
	var op DerivedOp
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&op); err != nil {
		return DerivedOp{}, fmt.Errorf("decode derived op: %w", err)
	}
	op.Args = NormalizeArgs(op.Args)
	return op, nil
}

// NormalizeArgs returns args with every element coerced to a stable,
// JSON-transport-safe primitive the SQLite driver binds losslessly. Its job is
// to undo JSON's "all numbers are float64" widening: a json.Number (produced by
// UseNumber()) or a float64 that is integral and fits int64 becomes an int64 so
// large integers bind EXACTLY; a number with a fractional part or outside int64
// range becomes float64; all other kinds (string, bool, nil, []byte) pass
// through unchanged. The result is also the canonical form sqlOpID hashes, so
// the dedup key is stable across encode→decode (roborev-473 findings 6 & 7).
func NormalizeArgs(args []any) []any {
	if args == nil {
		return nil
	}
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = normalizeArg(a)
	}
	return out
}

// normalizeArg coerces a single decoded/raw arg to its canonical primitive.
func normalizeArg(a any) any {
	switch v := a.(type) {
	case json.Number:
		return numberFromString(v.String())
	case float64:
		// json.Unmarshal without UseNumber yields float64; preserve integral
		// values exactly when they fit int64.
		if i := int64(v); float64(i) == v {
			return i
		}
		return v
	default:
		return a
	}
}

// numberFromString parses a json.Number's textual form into int64 when it is an
// integer within int64 range, else float64. Using the ORIGINAL text (not a
// float intermediate) is what preserves precision for integers above 2^53 — a
// detour through float64 would already have rounded.
func numberFromString(s string) any {
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	// Unparseable as a number (should not happen for a json.Number); keep the
	// string so the value still binds rather than vanishing.
	return s
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
				if err := db.EnsureSession(database, ev.SessionID); err != nil {
					return err
				}
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
				// Use UpsertSession rather than InsertSession so that a
				// placeholder row created by EnsureSession (from an earlier
				// out-of-order agent_event.upsert for this session) is
				// upgraded with the real metadata rather than causing a PK
				// conflict and leaving the row stuck with "__hook__" values.
				return db.UpsertSession(database, &s)
			}, nil
		case OpTypeSessionStatus:
			if op.SessionID == "" {
				return nil, fmt.Errorf("%s: empty session_id", OpTypeSessionStatus)
			}
			sid, status := op.SessionID, op.Status
			return func(_ context.Context) error {
				return db.UpdateSessionStatus(database, sid, status)
			}, nil
		case OpTypeSQL:
			if op.SQL == "" {
				return nil, fmt.Errorf("%s: empty sql", OpTypeSQL)
			}
			// Capture statement + args by value so the WriteOp is
			// self-contained on the single-writer worker. Args are passed as
			// bind parameters to ExecContext — NEVER interpolated into the
			// statement text (constraint q-sql-safety). op.Args were already
			// normalized in Decode (int64 integers preserved exactly), so the
			// values bound here match what the caller sent (roborev-473 finding 6).
			stmt, args := op.SQL, op.Args
			return func(ctx context.Context) error {
				// Run on the daemon's EXISTING writable handle, wrapped in the
				// shared bounded busy-backoff so a transient SHARED→RESERVED
				// race on a DELETE-journal host resolves transparently (the
				// same protection the direct CLI/hook writers get). The whole
				// statement is idempotent-or-safely-re-runnable as far as the
				// retry budget is concerned: only BUSY/locked errors retry.
				return db.RetryOnBusy(db.DefaultBusyBackoff, func() error {
					_, execErr := database.ExecContext(ctx, stmt, args...)
					return execErr
				})
			}, nil
		default:
			return nil, fmt.Errorf("unknown derived op_type %q", op.Type)
		}
	}
}
