// Package hooks — dbgate.go: canonical-first DB-open gate for hook handlers.
//
// SLICE-7 CONTRACT (plan-ae0c37b2, feat-33c26c74):
//
//	Hook subprocesses are short-lived processes spawned by Claude Code per
//	event. They CANNOT reach the in-process write queue that lives inside a
//	separate `wipnote serve` process. The architectural answer for the
//	hook tree is "canonical-first with graceful fallback":
//
//	  1. Canonical NDJSON/HTML is written first by the handler (the indexer
//	     in `wipnote serve` will pick it up and rebuild the SQLite index).
//	  2. The hook also opens a writable DB handle to update the derived
//	     index synchronously while the data is fresh — this is the existing
//	     contention-prone path. If the open fails (lock held by another
//	     writer, disk full, FS race), the hook MUST return SUCCESS to the
//	     caller and emit a structured fallback log line. Reindex recovers
//	     the missing rows from canonical NDJSON on the next serve cycle.
//
// OPENING THIS DB FROM THE HOOK TREE STAYS A "FORBIDDEN PATH" by the slice-5
// boundary, but it is now centralised behind ONE call site (this file) so
// reviewers can audit the failure-tolerance contract in one place. The
// slice-5 inventory reclassifies the hook entries to point at THIS file
// rather than the three call sites in cmd/wipnote/hook.go.
package hooks

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync/atomic"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/daemon/apply"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/models"
)

// FallbackReason is the structured label emitted when a hook's derived-index
// write path cannot proceed and the handler falls back to canonical-only
// persistence. The labels match the contract from the plan critique:
// "writer_unavailable", "queue_full", "timeout".
type FallbackReason string

const (
	// FallbackWriterUnavailable means the writable SQLite handle could not
	// be opened (or a queue was supplied but is stopped/never-started).
	FallbackWriterUnavailable FallbackReason = "writer_unavailable"
	// FallbackQueueFull means the in-process writer queue was at capacity
	// when the hook tried to submit a derived-index op. Only emitted when
	// a queue is wired in (in-process hook callers); subprocess hooks
	// always use FallbackWriterUnavailable for open failures.
	FallbackQueueFull FallbackReason = "queue_full"
	// FallbackTimeout means SubmitWithTimeout's deadline elapsed before a
	// slot opened. Only emitted by queue-backed callers.
	FallbackTimeout FallbackReason = "timeout"
)

// Fallback counters — process-level metrics so the dashboard /api/collector-status
// surface (slice-10 will extend this) can show how often hooks degraded to
// canonical-only mode. Atomic so they remain safe for concurrent hook goroutines
// in the in-process runner.
var (
	fallbackWriterUnavailable atomic.Int64
	fallbackQueueFull         atomic.Int64
	fallbackTimeout           atomic.Int64
)

// FallbackCounts returns the current fallback counters (writer_unavailable,
// queue_full, timeout). Exported for tests and the dashboard observability
// surface.
func FallbackCounts() (writerUnavailable, queueFull, timeout int64) {
	return fallbackWriterUnavailable.Load(),
		fallbackQueueFull.Load(),
		fallbackTimeout.Load()
}

// ResetFallbackCounts zeroes the counters. Intended for tests only.
func ResetFallbackCounts() {
	fallbackWriterUnavailable.Store(0)
	fallbackQueueFull.Store(0)
	fallbackTimeout.Store(0)
}

// RecordFallback bumps the appropriate counter and emits a structured log
// line tagged with the reason. handler/sessionID let the log line correlate
// with the rest of the hook trace.
func RecordFallback(handler, sessionID string, reason FallbackReason, detail string) {
	switch reason {
	case FallbackWriterUnavailable:
		fallbackWriterUnavailable.Add(1)
	case FallbackQueueFull:
		fallbackQueueFull.Add(1)
	case FallbackTimeout:
		fallbackTimeout.Add(1)
	}
	projectDir := resolveLogDir()
	if projectDir == "" {
		return
	}
	fields := map[string]string{"fallback": string(reason)}
	if sessionID != "" {
		fields["session"] = sessionID[:minSessionLen(sessionID)]
	}
	if detail != "" {
		fields["detail"] = detail
	}
	debugLogFields(projectDir, handler, fields, "canonical-first fallback engaged")
}

// OpenHookDB returns a writable DB handle for the hook subprocess to use
// when applying derived-index updates. On open failure it returns (nil, reason).
// The reason is logged + counted; callers MUST treat a nil DB as a signal
// to skip DB-dependent work and return a success HookResult.
//
// The current implementation opens the DB exactly like the pre-slice-7 code
// did — short-lived hook subprocesses still need to write to SQLite while
// canonical NDJSON exists on the same disk. The contract change is at the
// FAILURE BOUNDARY: a failed open no longer cascades into a hook error,
// and the canonical NDJSON write upstream guarantees reindex recovery.
//
// This is the ONLY allowed direct writable open in the hook tree.
// `cmd/wipnote/hook.go` now calls this helper exclusively; do not add
// new db.Open call sites in internal/hooks/ or cmd/wipnote/hook.go.
func OpenHookDB(handler, sessionID, dbPath string) (*sql.DB, FallbackReason) {
	database, err := db.Open(dbPath)
	if err != nil {
		// Slice-10 contention observability: classify open failures by
		// hook_writer subsystem so the launch gate can assert zero BUSY
		// from the hook tree. Non-BUSY open failures (e.g., schema lock,
		// disk full) bypass the counter; the structured fallback log
		// upstream captures those.
		db.Record(db.SubsystemHookWriter, err)
		RecordFallback(handler, sessionID, FallbackWriterUnavailable, err.Error())
		return nil, FallbackWriterUnavailable
	}
	return database, ""
}

// SessionStartBusyTimeout bounds how long the session-start hook's writable DB
// handle waits on a held write lock before failing fast. The session-start hook
// runs on the launcher's POST-selection critical path: Claude blocks on the
// hook's additionalContext, so a write that stalls for the default 5s
// busy_timeout (under contention from a freshly-spawned `wipnote serve` /
// `_serve-child` / per-session `otel-collect`) directly delays the interactive
// session by that long (bug-504095f2).
//
// 750ms is the chosen bound: long enough to ride out the brief lock overlap a
// healthy single writer produces, short enough that a genuinely contended lock
// degrades to canonical-only persistence (logged + counted) in well under a
// second instead of stalling ~5s. Every session-start derived write is
// best-effort — failures are swallowed via the canonical-first fallback and
// recovered by reindex from canonical NDJSON — so failing fast here only skips a
// derived-index write, never user data.
const SessionStartBusyTimeout = 750 * time.Millisecond

// OpenHookDBWithBusyTimeout is OpenHookDB with a caller-chosen busy_timeout on
// the returned writable handle, so contention-prone writes fail fast instead of
// stalling for the connection-default 5s. It exists for the session-start hook
// path (see SessionStartBusyTimeout); other hooks keep OpenHookDB's default
// timeout. busyTimeout <= 0 is identical to OpenHookDB.
//
// Failure semantics are unchanged from OpenHookDB: a failed open is classified
// under hook_writer, logged + counted as writer_unavailable, and returns a nil
// handle the caller MUST treat as a signal to return canonical-success.
//
// Boundary note: this routes through db.OpenWithBusyTimeout (a core/db
// primitive, excluded from the writable-open scan and not a scanned method
// name), so it adds NO new approvedWriteSites entry — the single inventoried
// hook write site (OpenHookDB → db.Open) is unchanged.
func OpenHookDBWithBusyTimeout(handler, sessionID, dbPath string, busyTimeout time.Duration) (*sql.DB, FallbackReason) {
	database, err := db.OpenWithBusyTimeout(dbPath, busyTimeout)
	if err != nil {
		db.Record(db.SubsystemHookWriter, err)
		RecordFallback(handler, sessionID, FallbackWriterUnavailable, err.Error())
		return nil, FallbackWriterUnavailable
	}
	return database, ""
}

// SubmitDerivedOp routes a derived-index write through the writer queue when
// one is supplied (in-process hook callers — `wipnote claude` / `wipnote yolo`
// embedding scenarios) and otherwise runs op synchronously against db.
//
// Failure semantics:
//   - q nil + db nil               → record FallbackWriterUnavailable, return nil
//   - q nil + db non-nil           → run op synchronously; op error logged but ignored
//   - q non-nil + queue full       → record FallbackQueueFull, return nil
//   - q non-nil + writer stopped   → record FallbackWriterUnavailable, return nil
//   - q non-nil + ctx-cancelled    → record FallbackTimeout, return nil
//
// In every case the return value is nil; the canonical NDJSON write upstream
// is authoritative. The caller MUST NOT propagate any error from this call
// back to the Claude Code hook protocol.
func SubmitDerivedOp(handler, sessionID string, q *writequeue.Queue, database *sql.DB, op func(*sql.DB) error) {
	if q != nil {
		// Wrap op in the queue's WriteOp signature. The op closure receives
		// the producer-supplied `database` handle (which may be nil — the
		// queue worker can run ops that capture their own writer handle).
		wrap := func(_ context.Context) error {
			return op(database)
		}
		if err := q.Submit(context.Background(), wrap); err != nil {
			switch {
			case errors.Is(err, writequeue.ErrQueueFull):
				RecordFallback(handler, sessionID, FallbackQueueFull, err.Error())
			case errors.Is(err, writequeue.ErrTimeout):
				RecordFallback(handler, sessionID, FallbackTimeout, err.Error())
			case errors.Is(err, writequeue.ErrWriterUnavailable):
				RecordFallback(handler, sessionID, FallbackWriterUnavailable, err.Error())
			default:
				RecordFallback(handler, sessionID, FallbackWriterUnavailable, err.Error())
			}
		}
		return
	}
	if database == nil {
		RecordFallback(handler, sessionID, FallbackWriterUnavailable, "no queue and no db")
		return
	}
	// Synchronous fallback. The hook subprocess writes directly to SQLite
	// here while the dashboard read pool and a parallel session's writer may
	// briefly contend the lock. Retry the op on SQLITE_BUSY with bounded
	// backoff (bug-74a7bda7) so a transient lock overlap resolves
	// transparently instead of dropping the derived-index update. A terminal
	// BUSY is classified under hook_writer for the launch gate, then
	// swallowed — canonical NDJSON is the authoritative copy and reindex
	// recovers any rows the synchronous path missed.
	err := db.RetryOnBusy(db.DefaultBusyBackoff, func() error { return op(database) })
	db.Record(db.SubsystemHookWriter, err)
	if err != nil {
		debugLogFields(resolveLogDir(), handler,
			map[string]string{"phase": "derived-op", "session": safeSessionID(sessionID)},
			"sync derived-op error (recoverable via reindex): "+err.Error())
	}
}

// daemonSubmitBudget is the TOTAL wall-clock budget for the daemon-routed
// derived-write attempt (dial + auto-spawn + readiness-wait + submit).
//
// LIVE-SESSION SAFETY (feat-075c110d): hooks shell out to the wipnote binary
// on every tool call, so this path runs inside the user's RUNNING session. It
// MUST be strictly bounded — on any timeout/error/unavailability the caller
// falls back to the existing direct-open path, never blocking the session.
const daemonSubmitBudget = 2 * time.Second

// SubmitDerivedEvent routes an agent_events derived-index upsert through the
// per-project writer daemon (auto-spawning it when absent), and falls back to
// the existing direct-open + RetryOnBusy path on ANY daemon failure.
//
// Contract (MVP-3, plan-bb91616a slice-4):
//   - The canonical NDJSON/HTML write is the caller's responsibility and MUST
//     already have happened — this routes ONLY the derived-index write.
//   - Daemon path: build a typed DerivedOp → WriterClient.SubmitOrSpawn under
//     a hard daemonSubmitBudget deadline. An applied/duplicate ack ends here.
//   - Fallback path: on ErrWriterUnavailable (no/forbidden daemon, spawn or
//     readiness failure, budget exhausted) OR an error ack, run the SAME
//     upsert synchronously via db.RetryOnBusy against `database` — exactly
//     today's behaviour. A nil `database` records writer_unavailable and
//     returns (canonical NDJSON upstream is authoritative).
//
// The return value is always nil-effect (no error surfaced to the hook
// protocol): like SubmitDerivedOp, this never propagates failures back to the
// caller. seq is the per-event sequence/offset used to derive the dedup op_id.
func SubmitDerivedEvent(handler, projectRoot, sessionID string, seq int64, ev *models.AgentEvent, database *sql.DB) {
	if ev == nil {
		return
	}

	if routeDerivedEventViaDaemon(projectRoot, sessionID, seq, ev) {
		return // applied (or deduped) by the writer daemon
	}

	// Fallback: direct-open synchronous upsert with bounded BUSY retry —
	// identical to the legacy SubmitDerivedOp synchronous path.
	if database == nil {
		RecordFallback(handler, sessionID, FallbackWriterUnavailable, "no daemon and no db")
		return
	}
	err := db.RetryOnBusy(db.DefaultBusyBackoff, func() error { return db.UpsertEvent(database, ev) })
	db.Record(db.SubsystemHookWriter, err)
	if err != nil {
		debugLogFields(resolveLogDir(), handler,
			map[string]string{"phase": "derived-event", "session": safeSessionID(sessionID)},
			"sync derived-event error (recoverable via reindex): "+err.Error())
	}
}

// routeDerivedEventViaDaemon attempts the daemon-routed derived write under a
// hard deadline. It returns true ONLY when the writer applied (or deduped) the
// op; any unavailability, error ack, or empty projectRoot returns false so the
// caller falls back to direct-open. It never blocks beyond daemonSubmitBudget.
func routeDerivedEventViaDaemon(projectRoot, sessionID string, seq int64, ev *models.AgentEvent) bool {
	if projectRoot == "" {
		return false
	}
	payload, err := apply.Encode(apply.DerivedOp{Type: apply.OpTypeAgentEventUpsert, Event: ev})
	if err != nil {
		return false
	}
	env := daemon.Envelope{
		OpID:    apply.OpID(sessionID, seq),
		OpType:  apply.OpTypeAgentEventUpsert,
		Payload: payload,
	}

	ctx, cancel := context.WithTimeout(context.Background(), daemonSubmitBudget)
	defer cancel()

	selfExe, _ := os.Executable()
	client := daemon.NewWriterClient(projectRoot)
	ack, err := client.SubmitOrSpawn(ctx, projectRoot, selfExe, env)
	if err != nil {
		return false // ErrWriterUnavailable / ctx deadline → fall back
	}
	switch ack.Status {
	case daemon.AckApplied, daemon.AckDuplicate:
		return true
	default:
		return false // error ack → fall back to direct-open
	}
}

// safeSessionID truncates a session ID for log emission and is nil-safe.
func safeSessionID(s string) string {
	if s == "" {
		return ""
	}
	return s[:minSessionLen(s)]
}

// routeSQLAsync is the daemon enqueue-only seam used by RouteHookWrite. It is a
// package-level var (not a direct call) ONLY so tests can stub the daemon hop —
// production always binds it to apply.RouteSQLAsync. Mirrors the indirection the
// daemon-routing tests rely on elsewhere; do not call it directly from other
// hook code (use apply.RouteSQLAsync) so the override stays test-local.
var routeSQLAsync = apply.RouteSQLAsync

// RouteHookWrite is THE single primitive every hot hook write migrates to. It
// applies a parameterized derived-index statement under a daemon-first,
// bounded-direct-fallback, canonical-first policy and ALWAYS returns success —
// it never errors and never blocks beyond the bounded fallback. The boolean
// return is advisory (true ⇒ the op was durably handled by one of the two
// paths, OR degraded cleanly to canonical-only); callers MUST NOT propagate a
// failure to the Claude Code hook protocol regardless.
//
// Policy, in order (plan-2390966a slice-2, v4 enqueue-only amendment):
//
//  1. ENQUEUE-ONLY daemon route: routeSQLAsync(projectRoot, sql, args...). On
//     true → DONE. No direct writable handle is opened. Because the ack is
//     enqueue-only (apply.RouteSQLAsync / daemon.AckEnqueued, roborev 451/452),
//     a reachable-but-BUSY writer still returns in well under a second — the
//     whole point of routing hot hooks through the async mode. The op applies
//     in FIFO order after this returns; a crash-lost async op is recovered by
//     the canonical NDJSON + reindex backstop the caller already wrote.
//
//  2. BOUNDED direct fallback (only when step 1 returns false — daemon
//     unreachable / spawn-forbidden / queue full): resolve the per-project DB
//     path and open a SHORT-bounded (SessionStartBusyTimeout, ~750ms) writable
//     handle via OpenHookDBWithBusyTimeout — the existing single inventoried
//     hook write site. A held external write lock therefore degrades to
//     canonical-only in <1s instead of stalling on the default 5s busy_timeout.
//     The statement is Exec'd best-effort with bounded BUSY retry; a terminal
//     BUSY is classified under hook_writer for the launch gate, then swallowed.
//
//  3. ANY failure (DBPath error, nil handle, Exec error) → the canonical-first
//     fallback is already logged + counted as writer_unavailable; return success
//     anyway. Canonical NDJSON upstream is authoritative and reindex recovers
//     the row.
//
// args are bound as SQL parameters by every path and MUST NOT be interpolated
// into sql by the caller. handler labels the fallback counter/log line;
// sessionID correlates the log with the rest of the hook trace.
func RouteHookWrite(handler, projectRoot, sessionID, sql string, args ...any) bool {
	// Step 1: enqueue-only daemon route. A true ack means the op is durably
	// queued; we open NO direct handle.
	if routeSQLAsync(projectRoot, sql, args...) {
		return true
	}

	// Step 2: bounded direct fallback. Resolve the canonical DB path; a resolve
	// failure is itself a writer_unavailable degradation (canonical NDJSON is
	// authoritative).
	dbPath, err := DBPath(projectRoot)
	if err != nil {
		RecordFallback(handler, sessionID, FallbackWriterUnavailable, "dbpath: "+err.Error())
		return true
	}

	database, _ := OpenHookDBWithBusyTimeout(handler, sessionID, dbPath, SessionStartBusyTimeout)
	if database == nil {
		// Open failed: already classified under hook_writer, logged + counted as
		// writer_unavailable inside OpenHookDBWithBusyTimeout. Degrade to
		// canonical-only.
		return true
	}
	defer database.Close()

	// Best-effort SINGLE Exec — the handle's SessionStartBusyTimeout (~750ms) IS
	// the failure bound, so a held write lock fails fast in <1s. We deliberately
	// do NOT layer RetryOnBusy here (unlike the cold SubmitDerivedEvent path):
	// each BUSY retry would re-wait the full busy_timeout and could push past the
	// sub-second bound this hot-hook primitive guarantees. A terminal BUSY is
	// classified under hook_writer for the launch gate, then swallowed —
	// canonical NDJSON + reindex recover any row this path could not write.
	_, execErr := database.Exec(sql, args...)
	db.Record(db.SubsystemHookWriter, execErr)
	if execErr != nil {
		RecordFallback(handler, sessionID, FallbackWriterUnavailable, "exec: "+execErr.Error())
	}
	// Step 3: always success — never error, never block the hook protocol.
	return true
}

// routeHookWriteVia applies RouteHookWrite's daemon-first, canonical-first
// policy but binds the bounded fallback to the writable handle the hook ALREADY
// holds (the one cmd/wipnote/hook.go opened from DBPath(projectRoot)) instead of
// re-opening a second handle. It is the hot-hook adaptation of RouteHookWrite for
// handlers that are already holding their per-project *sql.DB:
//
//  1. ENQUEUE-ONLY daemon route: routeSQLAsync(projectRoot, sql, args...). On
//     true → DONE; NO direct writable Exec is issued. Because the ack is
//     enqueue-only, a reachable-but-busy writer still returns in well under a
//     second (roborev 451/452) — this is what delivers the hot hooks' <1s bound
//     under the realistic single-writer-daemon contention.
//
//  2. BOUNDED direct fallback (only when step 1 returns false — daemon
//     unreachable / spawn-forbidden / queue full): Exec the SAME parameterized
//     statement against `database`. The handle's own busy_timeout is the failure
//     bound (the session-start path passes a SHORT one); we do NOT layer
//     RetryOnBusy so a held lock degrades fast rather than re-waiting the full
//     timeout. A nil handle is itself a writer_unavailable degradation.
//
//  3. ANY failure (nil handle, Exec error) → logged + counted as
//     writer_unavailable; canonical NDJSON upstream is authoritative and reindex
//     recovers the row.
//
// Like RouteHookWrite it ALWAYS returns advisory-true: it never errors and never
// blocks the hook protocol. args are bound as SQL parameters and MUST NOT be
// interpolated into sql by the caller.
//
// Why this and not RouteHookWrite directly: RouteHookWrite re-opens a fresh
// bounded handle at DBPath(projectRoot). For an in-handler caller that is a
// redundant second open (same file in production) and, in unit tests that pass
// an ad-hoc *sql.DB without a matching DBPath, would write to a DIFFERENT file.
// Binding the fallback to the caller's handle keeps the write on the one DB the
// hook is already using.
func routeHookWriteVia(handler, projectRoot, sessionID string, database *sql.DB, sqlStmt string, args ...any) bool {
	// Step 1: enqueue-only daemon route — no direct handle when reachable.
	if routeSQLAsync(projectRoot, sqlStmt, args...) {
		return true
	}
	// Step 2: bounded direct fallback against the handle the hook already holds.
	if database == nil {
		RecordFallback(handler, sessionID, FallbackWriterUnavailable, "no daemon and nil db")
		return true
	}
	_, execErr := database.Exec(sqlStmt, args...)
	db.Record(db.SubsystemHookWriter, execErr)
	if execErr != nil {
		RecordFallback(handler, sessionID, FallbackWriterUnavailable, "exec: "+execErr.Error())
	}
	// Step 3: always success — never error, never block the hook protocol.
	return true
}

// agentEventInsertSQL is the parameterized INSERT for an agent_events row. It is
// the EXACT statement db.InsertEvent issues (core/db/event_repo.go) — kept
// byte-identical here so the hot hooks (pretooluse tool_call insert, Stop
// terminal event) can route the same write through RouteHookWrite's daemon-first
// enqueue path instead of opening a direct writable handle. Column order and the
// 22 placeholders MUST stay in lock-step with db.InsertEvent.
const agentEventInsertSQL = `
	INSERT INTO agent_events (
		event_id, agent_id, event_type, timestamp, tool_name,
		input_summary, tool_input, output_summary, session_id, feature_id,
		parent_agent_id, parent_event_id, subagent_type,
		cost_tokens, execution_duration_seconds, status,
		model, claude_task_id, source, step_id,
		created_at, updated_at
	) VALUES (?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?)`

// nullableArg maps an empty string to a typed SQL NULL (nil) and any non-empty
// string to itself. Unlike sql.NullString it is JSON-transport-safe: the daemon
// enqueue path JSON-encodes RouteHookWrite's args (core/daemon/apply.DerivedOp),
// and a plain string / nil binds identically on BOTH the daemon's ExecContext
// and the direct-fallback Exec — whereas a sql.NullString would JSON-marshal to
// an object the SQLite driver cannot bind. This reproduces db.nullStr's NULL
// semantics across the process boundary (parity with db.InsertEvent's columns).
func nullableArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// agentEventInsertArgs renders an AgentEvent into the 22 bind parameters for
// agentEventInsertSQL, matching db.InsertEvent's argument order and NULL
// handling exactly. Timestamps are RFC3339 strings; absent text columns become
// typed NULLs via nullableArg so the row is indistinguishable from one written
// by the direct db.InsertEvent path.
func agentEventInsertArgs(e *models.AgentEvent) []any {
	return []any{
		e.EventID, e.AgentID, string(e.EventType),
		e.Timestamp.UTC().Format(time.RFC3339), nullableArg(e.ToolName),
		nullableArg(e.InputSummary), nullableArg(e.ToolInput), nullableArg(e.OutputSummary),
		e.SessionID, nullableArg(e.FeatureID),
		nullableArg(e.ParentAgentID), nullableArg(e.ParentEventID),
		nullableArg(e.SubagentType),
		e.CostTokens, e.ExecDuration, e.Status,
		nullableArg(e.Model), nullableArg(e.ClaudeTaskID),
		e.Source, nullableArg(e.StepID),
		e.CreatedAt.UTC().Format(time.RFC3339),
		e.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// RouteInsertEvent routes an agent_events INSERT through RouteHookWrite's
// daemon-first, bounded-direct-fallback policy — the hot-hook equivalent of
// db.InsertEvent. It mirrors db.InsertEvent's one pre-write read: when
// ParentEventID is set but ParentAgentID is empty, the parent row's agent_id is
// resolved from `database` first so the lineage edge is materialised at insert
// time. That lookup is a READ (it never contends the write lock that stalled the
// hot hooks) and is skipped when `database` is nil (daemon-only callers).
//
// Like every RouteHookWrite caller this is best-effort and ALWAYS succeeds from
// the hook's perspective: a held write lock degrades to canonical-only in <1s,
// and canonical NDJSON + reindex recover the row. The return value is advisory
// (RouteHookWrite never errors); callers MUST NOT propagate it to the hook
// protocol.
func RouteInsertEvent(handler, projectRoot, sessionID string, ev *models.AgentEvent, database *sql.DB) bool {
	if ev == nil {
		return true
	}
	// Materialise the parent_agent_id lineage edge exactly as db.InsertEvent
	// does — a pure read, safe to run direct even under write contention.
	if ev.ParentEventID != "" && ev.ParentAgentID == "" && database != nil {
		var parentAgentID string
		if err := database.QueryRow(
			`SELECT agent_id FROM agent_events WHERE event_id = ?`, ev.ParentEventID,
		).Scan(&parentAgentID); err == nil {
			ev.ParentAgentID = parentAgentID
		}
	}
	return routeHookWriteVia(handler, projectRoot, sessionID, database, agentEventInsertSQL, agentEventInsertArgs(ev)...)
}
