// cli_route.go — MVP-4 (feat-075c110d): bounded, fallback-safe daemon routing
// for the two highest-contention CLI derived-index writes (work-item status
// transitions and session insert/status). This mirrors the committed MVP-3 hook
// path in internal/hooks/dbgate.go (routeDerivedEventViaDaemon): build a typed
// DerivedOp, ship it to the per-project writer daemon under a hard deadline,
// auto-spawning the daemon when absent, and report whether the op was applied.
//
// LIVE-SESSION SAFETY: these run inside foreground CLI commands that the user
// is waiting on (feature/bug/spike start+complete, session start+end). The
// attempt MUST be strictly bounded — on ErrWriterUnavailable / any error /
// deadline / error-ack the CALLER falls back to its existing direct-open +
// RetryOnBusy path. This helper NEVER blocks beyond CLISubmitBudget and never
// surfaces an error: a false return means "fall back to direct write".
//
// Canonical .html is written FIRST by the command; only this derived-index
// (SQLite) write is delegated. Reads stay direct.
package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/models"
)

// CLISubmitBudget is the TOTAL wall-clock budget for a daemon-routed CLI
// derived-write attempt (dial + auto-spawn + readiness-wait + submit). It
// matches the hook tree's daemonSubmitBudget — the failure boundary is the
// same: exceed it and the caller falls back to the direct path. Foreground CLI
// commands must never hang on a wedged or absent daemon.
const CLISubmitBudget = 2 * time.Second

// cliOpID derives a deterministic, bounded dedup key for a CLI op from its
// op_type and the natural identity of the row it touches (feature/session id
// plus the target status). Replaying the same transition dedups against its
// first application within the daemon's dedup window; a DISTINCT transition
// (e.g. in-progress → done) yields a distinct key so it is not swallowed.
func cliOpID(opType, id, status string) string {
	h := sha256.Sum256([]byte(opType + ":" + id + ":" + status))
	return hex.EncodeToString(h[:16])
}

// AsyncEnqueueBudget is the TOTAL wall-clock budget for an ENQUEUE-ONLY
// daemon-routed write (RouteSQLAsync). It is the failure boundary for the hot
// path: once the daemon is warm the op is acked on a sub-millisecond local
// round-trip (enqueue, not apply), so this budget exists only to bound the
// cold/auto-spawn window and to degrade a wedged-or-full queue to false rather
// than blocking. It deliberately matches CLISubmitBudget so a one-time
// auto-spawn still fits; the steady-state cost is nowhere near it. roborev
// 451/452: because the ack is enqueue-only, a reachable-but-busy writer never
// pushes this toward the budget — that is the whole point of the async mode.
const AsyncEnqueueBudget = 2 * time.Second

// routeViaDaemon performs the bounded SYNCHRONOUS (applied-ack) daemon
// round-trip for an already-built DerivedOp. opID is the dedup key; projectRoot
// is the project root (the PARENT of .wipnote/). It returns true ONLY when the
// writer APPLIED (or deduped) the op. This is the path every existing typed
// route (RouteFeatureStatus/RouteSessionStatus/RouteSessionInsert) uses; its
// semantics are unchanged.
func routeViaDaemon(projectRoot, opID, opType string, op DerivedOp) bool {
	return submitViaDaemon(projectRoot, opID, opType, op, false, CLISubmitBudget)
}

// submitViaDaemon is the shared bounded round-trip for both ack modes. When
// async is false it requests an applied-ack (AckApplied/AckDuplicate ⇒ true);
// when async is true it requests an enqueue-only ack (AckEnqueued/AckDuplicate,
// and also AckApplied for a warm fast-path, all ⇒ true). budget bounds the
// whole dial+spawn+submit. Empty projectRoot, encode failure, unavailability,
// ctx deadline, queue-full, or any error ack all return false so the caller
// falls back to its direct write.
func submitViaDaemon(projectRoot, opID, opType string, op DerivedOp, async bool, budget time.Duration) bool {
	if projectRoot == "" {
		return false
	}
	payload, err := Encode(op)
	if err != nil {
		return false
	}
	env := daemon.Envelope{
		OpID:    opID,
		OpType:  opType,
		Payload: payload,
		Async:   async,
	}

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	selfExe, _ := os.Executable()
	client := daemon.NewWriterClient(projectRoot)
	ack, err := client.SubmitOrSpawn(ctx, projectRoot, selfExe, env)
	if err != nil {
		return false // ErrWriterUnavailable / ctx deadline → fall back
	}
	switch ack.Status {
	case daemon.AckApplied, daemon.AckDuplicate:
		// Applied (sync) or deduped — durable either way. AckApplied can also
		// surface on the async path if the warm writer happened to commit
		// before replying; still a success for the enqueue-only caller.
		return true
	case daemon.AckEnqueued:
		// Enqueue-only success: the op is durably queued (FIFO) and will apply
		// after any op ahead of it. Only valid to accept when we asked for it.
		return async
	default:
		return false // error ack (e.g. queue full) → fall back to direct write
	}
}

// sqlOpID derives a deterministic, bounded dedup key for a generic SQL op from
// the statement text plus its bind args. Replaying the identical statement+args
// within the daemon's dedup window collapses to a single application; a
// different statement or different args yields a distinct key.
//
// The args are hashed via a TYPE-TAGGED canonical serialization, NOT %v
// (roborev-473 finding 7): %v renders the string "1" and the int 1 identically,
// so a later distinct SQL op could collide with an earlier one and be wrongly
// deduped/dropped. Each arg is first normalized to the SAME primitive the wire
// payload carries (NormalizeArgs — int64 integers preserved exactly) and then
// JSON-encoded, which distinguishes "1" (a JSON string) from 1 (a JSON number)
// and keeps the key stable across encode→decode. Args are still bound as SQL
// parameters when the op is applied (never interpolated).
func sqlOpID(sqlStmt string, args ...any) string {
	h := sha256.New()
	_, _ = io.WriteString(h, sqlStmt)
	// Length-frame the statement so it cannot run together with the arg block
	// (e.g. sql="a", arg="b" must not collide with sql="ab", no args).
	_, _ = io.WriteString(h, "\x00")
	// JSON-encode the normalized args slice as one canonical, type-tagged blob.
	// On the rare encode error fall back to a stable type-tagged fmt rendering so
	// the key stays distinct rather than silently empty.
	if blob, err := json.Marshal(NormalizeArgs(args)); err == nil {
		_, _ = h.Write(blob)
	} else {
		for _, a := range args {
			_, _ = io.WriteString(h, "\x00")
			_, _ = io.WriteString(h, typeTaggedFallback(a))
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// typeTaggedFallback renders an arg with a type prefix for the (practically
// unreachable) json.Marshal-error path, so "1" and 1 still hash differently.
func typeTaggedFallback(a any) string {
	return fmt.Sprintf("%T:%v", a, a)
}

// RouteSQL routes an arbitrary PARAMETERIZED statement through the writer
// daemon with APPLIED-ack semantics (synchronous): it returns true only when
// the writer committed (or deduped) the statement, false — with NO error and NO
// panic — when the daemon is unreachable / the op is rejected, so the caller
// falls back to a direct write. Bounded by CLISubmitBudget. Mirrors
// RouteSessionInsert. args are bound as SQL parameters and MUST NOT be
// interpolated into sql by the caller.
//
// Use this for CLI / cold paths that want apply confirmation. Hot hooks that
// must stay under a sub-second bound should use RouteSQLAsync instead (roborev
// 451/452).
func RouteSQL(projectRoot, sql string, args ...any) bool {
	if sql == "" {
		return false
	}
	return submitViaDaemon(
		projectRoot,
		sqlOpID(sql, args...),
		OpTypeSQL,
		DerivedOp{Type: OpTypeSQL, SQL: sql, Args: args},
		false,
		CLISubmitBudget,
	)
}

// RouteSQLAsync routes an arbitrary PARAMETERIZED statement through the writer
// daemon with ENQUEUE-ONLY ack semantics: it returns true the instant the op is
// durably handed to the single-writer queue — WITHOUT waiting for it to apply —
// and false when the enqueue itself fails (daemon unreachable OR queue full).
// Bounded by AsyncEnqueueBudget so a full/wedged queue degrades to false rather
// than blocking. The op still applies in FIFO order on the single writer after
// this returns; durability of the not-yet-applied op rests on the caller's
// canonical NDJSON write + reindex backstop.
//
// This is the foundation hot-path primitive (slice-2 builds RouteHookWrite on
// it): even when the daemon is reachable but its writer is busy holding the
// lock, RouteSQLAsync returns in well under the applied-ack budget. args are
// bound as SQL parameters and MUST NOT be interpolated into sql by the caller.
func RouteSQLAsync(projectRoot, sql string, args ...any) bool {
	if sql == "" {
		return false
	}
	return submitViaDaemon(
		projectRoot,
		sqlOpID(sql, args...),
		OpTypeSQL,
		DerivedOp{Type: OpTypeSQL, SQL: sql, Args: args},
		true,
		AsyncEnqueueBudget,
	)
}

// RouteFeatureStatus routes a work-item status transition (db.UpdateFeatureStatus)
// through the writer daemon. Returns true when applied/deduped; false → caller
// must perform the direct write. Bounded by CLISubmitBudget.
func RouteFeatureStatus(projectRoot, featureID, status string) bool {
	return routeViaDaemon(
		projectRoot,
		cliOpID(OpTypeFeatureStatus, featureID, status),
		OpTypeFeatureStatus,
		DerivedOp{Type: OpTypeFeatureStatus, FeatureID: featureID, Status: status},
	)
}

// RouteSessionStatus routes a session status transition (db.UpdateSessionStatus)
// through the writer daemon. Bounded by CLISubmitBudget.
func RouteSessionStatus(projectRoot, sessionID, status string) bool {
	return routeViaDaemon(
		projectRoot,
		cliOpID(OpTypeSessionStatus, sessionID, status),
		OpTypeSessionStatus,
		DerivedOp{Type: OpTypeSessionStatus, SessionID: sessionID, Status: status},
	)
}

// RouteSessionInsert routes a session-row insert (db.InsertSession) through the
// writer daemon. The op_id is keyed on the unique session_id so a replay
// dedups. Bounded by CLISubmitBudget. Returns true on applied/dedup; false →
// caller performs the direct insert (and surfaces its error exactly as today).
func RouteSessionInsert(projectRoot string, s *models.Session) bool {
	if s == nil {
		return false
	}
	return routeViaDaemon(
		projectRoot,
		cliOpID(OpTypeSessionInsert, s.SessionID, s.Status),
		OpTypeSessionInsert,
		DerivedOp{Type: OpTypeSessionInsert, Session: s},
	)
}

// RouteSessionInsertAsync routes a session-row insert (db.InsertSession) through
// the writer daemon with ENQUEUE-ONLY ack semantics — identical to
// RouteSessionInsert except it returns true the instant the op is durably handed
// to the single-writer queue, WITHOUT waiting for the daemon to APPLY it. The
// op_id is keyed on the unique session_id so a replay dedups; the op applies in
// FIFO order on the single writer after this returns.
//
// This is the launcher new-session COLD-INSERT primitive (bug-d792aee6 finding
// 1): under a held external write lock the daemon cannot apply, so the
// applied-ack RouteSessionInsert waits the full CLISubmitBudget (~2.4s observed),
// exceeding the launcher's <1s bound. Enqueue-only acks sub-millisecond once the
// daemon is warm — bringing the cold insert under 1s even under contention.
//
// SAFETY (verified by slice-3): SessionStart later performs an idempotent
// INSERT OR IGNORE upsert of the same row (routeSessionUpsert), and ops apply
// FIFO on the single writer, so an enqueued-but-not-yet-applied cold insert is
// harmless; canonical NDJSON + reindex is the durability backstop. Returns true
// on enqueue/dedup/(warm) apply; false → caller performs the direct insert.
// Bounded by AsyncEnqueueBudget.
func RouteSessionInsertAsync(projectRoot string, s *models.Session) bool {
	if s == nil {
		return false
	}
	return submitViaDaemon(
		projectRoot,
		cliOpID(OpTypeSessionInsert, s.SessionID, s.Status),
		OpTypeSessionInsert,
		DerivedOp{Type: OpTypeSessionInsert, Session: s},
		true,
		AsyncEnqueueBudget,
	)
}
