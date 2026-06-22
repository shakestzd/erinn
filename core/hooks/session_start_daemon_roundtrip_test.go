package hooks

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/daemon/apply"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestRouteSessionUpsert_DaemonRoundTrip is the regression gate for
// bug-a782badf / roborev-476 finding 1.
//
// routeSessionUpsert routes the session-row INSERT through RouteHookWrite →
// routeSQLAsync (apply.RouteSQLAsync), which JSON-ENCODES the bind args into a
// DerivedOp, ships them across the writer-daemon process boundary, and the
// daemon DECODES + binds them via apply.NewApplier. Before the fix, the local
// nullableStr returned sql.NullString, which JSON-marshals to the object
// {"String":...,"Valid":...}; the decoder turns that back into a map the SQLite
// driver cannot bind, so the daemon Exec FAILED and the session row NEVER
// applied via the daemon (only a reindex recovered it). The enqueue-only ack
// hid the failure entirely.
//
// This test stubs the package-level routeSQLAsync seam so it routes through the
// REAL Encode → Decode → apply.NewApplier path against a live migrated DB —
// exactly the transport the production daemon uses — and asserts the session
// ROW is present afterwards with correct nullable semantics (empty → NULL, set
// → value). It FAILS before the nullableStr fix (no row, because the bind
// errored) and PASSES after.
func TestRouteSessionUpsert_DaemonRoundTrip(t *testing.T) {
	clearNestedEnv(t)

	database, err := db.Open(filepath.Join(t.TempDir(), "roundtrip.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Route every async op through the genuine daemon transport: Encode the
	// (sql, args) into a DerivedOp payload, Decode it back (UseNumber +
	// NormalizeArgs, exactly like the daemon listener), build the applier
	// WriteOp via apply.NewApplier, and run it against the live DB. This is the
	// in-proc equivalent of the daemon's Encode→Decode→ExecContext, so a
	// non-JSON-bindable arg (the old sql.NullString) fails the Exec here just as
	// it did across the real socket.
	applier := apply.NewApplier(database)
	prev := routeSQLAsync
	t.Cleanup(func() { routeSQLAsync = prev })
	routeSQLAsync = func(_ string, sqlStmt string, args ...any) bool {
		payload, encErr := apply.Encode(apply.DerivedOp{Type: apply.OpTypeSQL, SQL: sqlStmt, Args: args})
		if encErr != nil {
			t.Fatalf("daemon round-trip: encode: %v", encErr)
		}
		op, opErr := applier(daemon.Envelope{OpType: apply.OpTypeSQL, Payload: payload})
		if opErr != nil {
			t.Fatalf("daemon round-trip: applier build: %v", opErr)
		}
		if execErr := op(context.Background()); execErr != nil {
			// A bind failure (the bug) surfaces here. Fail loudly so a regression
			// to sql.NullString cannot pass as a silently-dropped write.
			t.Fatalf("daemon round-trip: apply exec failed (args=%#v): %v", args, execErr)
		}
		return true
	}

	now := time.Now().UTC().Truncate(time.Second)
	s := &models.Session{
		SessionID:     "roundtrip-sess-1",
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     now,
		IsSubagent:    true, // bool must round-trip + bind as 0/1
		StartCommit:   "abc1234",
		Model:         "sonnet-4",
		Harness:       "claude",
		ProjectDir:    ".",
		// Deliberately leave the rest of the nullable fields EMPTY so we assert
		// empty → SQL NULL (not the literal "" the old sql.NullString-as-map
		// path could never even reach).
	}

	routeSessionUpsert("/does/not/matter", s.SessionID, s)

	// (1) The row must exist. Pre-fix it never applied via the daemon transport.
	got, err := db.GetSession(database, s.SessionID)
	if err != nil {
		t.Fatalf("GetSession after routed upsert: %v (the session row never applied "+
			"via the daemon transport — bug-a782badf regression)", err)
	}
	if got == nil {
		t.Fatal("routed session upsert produced no row via the daemon transport (bug-a782badf)")
	}
	if got.SessionID != s.SessionID || got.AgentAssigned != s.AgentAssigned || got.Status != s.Status {
		t.Fatalf("routed row mismatch: got %+v", got)
	}
	if !got.IsSubagent {
		t.Fatalf("is_subagent did not round-trip as true (bool bind broken): got %+v", got)
	}
	// GetSession does not SELECT start_commit; step (3) below asserts it via a
	// direct column query. Model + Harness ARE read back by GetSession.
	if got.Model != s.Model || got.Harness != s.Harness {
		t.Fatalf("set nullable fields lost their values: got model=%q harness=%q",
			got.Model, got.Harness)
	}

	// (2) Empty nullable fields must be SQL NULL, not "" — assert directly so the
	// NULL semantics (mirroring db.nullStr) are pinned across the transport.
	assertColumnNull(t, database, s.SessionID, "parent_session_id")
	assertColumnNull(t, database, s.SessionID, "branch")
	assertColumnNull(t, database, s.SessionID, "continued_from")

	// (3) A set nullable column must be non-NULL with the value.
	var startCommit sql.NullString
	if err := database.QueryRow(
		`SELECT start_commit FROM sessions WHERE session_id = ?`, s.SessionID,
	).Scan(&startCommit); err != nil {
		t.Fatalf("scan start_commit: %v", err)
	}
	if !startCommit.Valid || startCommit.String != s.StartCommit {
		t.Fatalf("start_commit not bound through the transport: valid=%v value=%q",
			startCommit.Valid, startCommit.String)
	}
}

// assertColumnNull fails the test unless the named TEXT column of the session
// row is SQL NULL, proving the empty-string → typed-NULL mapping survived the
// JSON encode/decode/bind round-trip.
func assertColumnNull(t *testing.T, database *sql.DB, sessionID, column string) {
	t.Helper()
	var v sql.NullString
	if err := database.QueryRow(
		`SELECT `+column+` FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&v); err != nil {
		t.Fatalf("scan %s: %v", column, err)
	}
	if v.Valid {
		t.Fatalf("%s should be SQL NULL for an empty input, got %q", column, v.String)
	}
}
