package hooks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

// openWipnoteTestDB opens the canonical per-project SQLite DB AND pins
// WIPNOTE_DB_PATH to that same file for the test's duration.
//
// Why the env pin is required (plan-2390966a slice-3): SessionStart now routes
// every writable Exec through the writer daemon (RouteHookWrite). In unit tests
// no daemon is running, so RouteHookWrite takes its bounded DIRECT fallback,
// which resolves the target file via DBPath(projectRoot) — by default the host
// cache dir, NOT projectDir/.wipnote/wipnote.db. Without this pin the routed
// writes would land in the cache DB while the test reads back from the handle
// returned here, yielding spurious "no rows" failures. In production the hook
// subprocess opens its handle at exactly DBPath(projectRoot), so the handle and
// the fallback target already coincide; this helper reproduces that invariant
// for tests.
func openWipnoteTestDB(t *testing.T, projectDir string) (*sql.DB, error) {
	t.Helper()
	dbPath := filepath.Join(projectDir, ".wipnote", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	return db.Open(dbPath)
}

// clearNestedSessionEnv unsets the env vars that, when leaked from a nested
// Claude Code session, would change the resolved session ID or mark the hook as
// a subagent — making the session-start writes land under the wrong ID and the
// read-backs return no rows (see MEMORY: nested-session-hooks-test).
func clearNestedSessionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")
}

// holdWriteLock opens a second connection to dbPath and holds a RESERVED write
// lock (BEGIN IMMEDIATE) until the returned release func runs. Used to prove the
// session-start path does not stall on a held external write lock.
func holdWriteLock(t *testing.T, dbPath string) (release func()) {
	t.Helper()
	holderDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	holder, err := holderDB.Conn(context.Background())
	if err != nil {
		holderDB.Close()
		t.Fatalf("acquire holder conn: %v", err)
	}
	if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		holder.Close()
		holderDB.Close()
		t.Fatalf("acquire write lock: %v", err)
	}
	return func() {
		_, _ = holder.ExecContext(context.Background(), "ROLLBACK")
		holder.Close()
		holderDB.Close()
	}
}

// bootstrapSessionDB creates projectDir/.wipnote, bootstraps the schema at the
// canonical path, points WIPNOTE_DB_PATH at it (so DBPath/RouteHookWrite resolve
// the same file), and returns the path.
func bootstrapSessionDB(t *testing.T, projectDir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	dbPath := filepath.Join(projectDir, ".wipnote", "wipnote.db")
	boot, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap open: %v", err)
	}
	boot.Close()
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	return dbPath
}

// TestSessionStartRoutesAllWritesViaDaemon is the slice-3 (plan-2390966a) proof
// that SessionStart performs NO direct writable Exec when the daemon is reachable
// and still completes in well under a second even while an external connection
// holds the write lock. The daemon enqueue seam (routeSQLAsync) is stubbed to ack
// instantly AND capture every (sql,args) op; after SessionStart returns we release
// the lock and drain the captured ops onto a writable handle, modelling the
// single-writer daemon's enqueue-now / apply-later semantics. We then read back
// the session row, its family id, the session-start event, and the transcript
// path to prove all four writes landed.
func TestSessionStartRoutesAllWritesViaDaemon(t *testing.T) {
	projectDir := t.TempDir()
	dbPath := bootstrapSessionDB(t, projectDir)
	clearNestedSessionEnv(t)

	const sid = "sess-daemon-routed"
	transcript := filepath.Join(projectDir, "transcript.jsonl")

	// Capture every routed op instantly (enqueue-only ack === instant return).
	type routedOp struct {
		sql  string
		args []any
	}
	var captured []routedOp
	prev := routeSQLAsync
	routeSQLAsync = func(_ string, sqlStmt string, args ...any) bool {
		captured = append(captured, routedOp{sql: sqlStmt, args: args})
		return true // daemon acked the enqueue
	}
	t.Cleanup(func() { routeSQLAsync = prev })
	// roborev-473 finding 5: the session_family_id update now routes APPLIED-ack
	// (routeSQLApplied) so it is visible before routeFamilyAttribution reads the
	// family. Stub it the same way — capture the op and ack instantly — so this
	// daemon-reachable proof still observes a single-writer FIFO apply and zero
	// canonical-first fallbacks.
	prevApplied := routeSQLApplied
	routeSQLApplied = func(_ string, sqlStmt string, args ...any) bool {
		captured = append(captured, routedOp{sql: sqlStmt, args: args})
		return true // daemon committed (applied-ack)
	}
	t.Cleanup(func() { routeSQLApplied = prevApplied })

	ResetFallbackCounts()

	// Hold the write lock for the duration of the SessionStart call.
	release := holdWriteLock(t, dbPath)

	// The handle passed to SessionStart is used only for READS now; open it before
	// the lock matters (reads don't block on a write lock under WAL).
	readDB, err := db.Open(dbPath)
	if err != nil {
		release()
		t.Fatalf("open read handle: %v", err)
	}
	defer readDB.Close()

	event := &CloudEvent{SessionID: sid, CWD: projectDir, TranscriptPath: transcript}

	start := time.Now()
	_, hookErr := SessionStart(event, readDB, projectDir)
	elapsed := time.Since(start)
	release() // drop the external lock so the captured ops can apply

	if hookErr != nil {
		t.Fatalf("SessionStart returned an error on the daemon path: %v", hookErr)
	}
	// Enqueue-only routing must not open a direct writable handle, so a held lock
	// cannot slow us down: the whole hook must finish in well under a second.
	if elapsed > 1*time.Second {
		t.Fatalf("SessionStart took %v with the daemon reachable; enqueue-only routing must keep it <1s under a held write lock", elapsed)
	}
	// No write degraded to the canonical-only fallback — the daemon handled all of them.
	if wu, qf, to := FallbackCounts(); wu != 0 || qf != 0 || to != 0 {
		t.Fatalf("expected zero canonical-first fallbacks on the daemon path; got writer_unavailable=%d queue_full=%d timeout=%d", wu, qf, to)
	}
	if len(captured) == 0 {
		t.Fatal("SessionStart routed no writes through the daemon seam")
	}

	// Model the single-writer daemon: apply the captured ops in FIFO order on one
	// writable handle now that the lock is free.
	applyDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open apply handle: %v", err)
	}
	defer applyDB.Close()
	for i, op := range captured {
		if _, err := applyDB.Exec(op.sql, op.args...); err != nil {
			t.Fatalf("apply captured op #%d failed: %v\nsql=%s", i, err, op.sql)
		}
	}

	// Read-back: the session row must exist with the routed metadata.
	sess, err := db.GetSession(readDB, sid)
	if err != nil {
		t.Fatalf("session row not found after daemon apply: %v", err)
	}
	if sess.SessionID != sid {
		t.Fatalf("session_id mismatch: got %q want %q", sess.SessionID, sid)
	}

	// Family id: SessionStart with no WIPNOTE_SESSION_FAMILY_ID treats the session
	// as its own family, so session_family_id == sid.
	var familyID sql.NullString
	if err := readDB.QueryRow(`SELECT session_family_id FROM sessions WHERE session_id = ?`, sid).Scan(&familyID); err != nil {
		t.Fatalf("read session_family_id: %v", err)
	}
	if familyID.String != sid {
		t.Fatalf("session_family_id mismatch: got %q want %q", familyID.String, sid)
	}

	// Transcript path must be persisted.
	var gotTranscript sql.NullString
	if err := readDB.QueryRow(`SELECT transcript_path FROM sessions WHERE session_id = ?`, sid).Scan(&gotTranscript); err != nil {
		t.Fatalf("read transcript_path: %v", err)
	}
	if gotTranscript.String != transcript {
		t.Fatalf("transcript_path mismatch: got %q want %q", gotTranscript.String, transcript)
	}

	// The session-start event must be present in agent_events.
	var eventCount int
	if err := readDB.QueryRow(
		`SELECT COUNT(1) FROM agent_events WHERE session_id = ? AND tool_name = 'SessionStart'`, sid,
	).Scan(&eventCount); err != nil {
		t.Fatalf("count session-start events: %v", err)
	}
	if eventCount == 0 {
		t.Fatal("no SessionStart agent_event landed after daemon apply")
	}
}

// TestSessionStartFailsFastUnderContention verifies that when the daemon is NOT
// reachable (routeSQLAsync returns false), SessionStart's writes degrade to the
// BOUNDED direct fallback baked into RouteHookWrite (a ~750ms busy_timeout open)
// rather than stalling on the connection-default 5s busy_timeout per write. The
// hook must still return WITHOUT error (canonical-first swallows BUSY) and well
// inside the multi-second stall the bug produced. Regression test for
// bug-504095f2 (Driver B); now also guards the slice-3 daemon-unreachable path.
func TestSessionStartFailsFastUnderContention(t *testing.T) {
	projectDir := t.TempDir()
	dbPath := bootstrapSessionDB(t, projectDir)
	clearNestedSessionEnv(t)

	// Force the daemon-unreachable branch so RouteHookWrite takes its bounded
	// direct fallback (OpenHookDBWithBusyTimeout, SessionStartBusyTimeout).
	prev := routeSQLAsync
	routeSQLAsync = func(_ string, _ string, _ ...any) bool { return false }
	t.Cleanup(func() { routeSQLAsync = prev })
	// roborev-473 finding 5: the applied-ack family-id seam must also miss so its
	// fallback takes the bounded own-handle write (routeViaOwnBoundedHandle,
	// ~750ms) rather than the passed handle's default 5s busy_timeout — proving the
	// applied-ack path ALSO fails fast under contention when the daemon is gone.
	prevApplied := routeSQLApplied
	routeSQLApplied = func(_ string, _ string, _ ...any) bool { return false }
	t.Cleanup(func() { routeSQLApplied = prevApplied })

	ResetFallbackCounts()

	// A second connection holds the RESERVED write lock for the whole test, so the
	// bounded fallback's writable Exec must fail fast against the held lock.
	release := holdWriteLock(t, dbPath)
	defer release()

	// Read handle for SessionStart's lineage/family lookups.
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open read handle: %v", err)
	}
	defer readDB.Close()

	event := &CloudEvent{SessionID: "sess-contention", CWD: projectDir}

	start := time.Now()
	_, hookErr := SessionStart(event, readDB, projectDir)
	elapsed := time.Since(start)

	// The hook MUST NOT surface an error even when every derived write is blocked —
	// canonical-first fallback swallows BUSY so the launcher never sees a hook error.
	if hookErr != nil {
		t.Fatalf("SessionStart returned an error under contention (must swallow busy via canonical-first): %v", hookErr)
	}
	// Each blocked write fails fast at the ~750ms bound instead of the 5s default.
	// A regression to the default timeout would blow past this on the very first
	// blocked write (5s > 4.5s); the bounded fallback keeps the whole hook under it
	// even across the handful of session-start writes.
	if elapsed > 4500*time.Millisecond {
		t.Fatalf("SessionStart blocked %v under contention; expected the bounded busy_timeout fast-fail, not the 5s default stall", elapsed)
	}
	// The contended writes degraded to canonical-only via the writer_unavailable
	// fallback — prove the bounded direct path actually engaged.
	if wu, _, _ := FallbackCounts(); wu == 0 {
		t.Fatal("expected at least one writer_unavailable fallback under a held lock with the daemon unreachable")
	}
}
