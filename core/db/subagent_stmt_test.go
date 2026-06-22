package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// bug-c9ec25a4: subagent-start's three remaining direct lineage writes
// (BackfillParentSession / InsertLineageTrace / UpsertPendingSubagentStart) are
// routed through the daemon's enqueue-only seam. The seam consumes the
// parameterized statement these builders return, so each test pins TWO contracts
// (mirroring claim_stmt_test.go):
//
//  1. EFFECT EQUIVALENCE — the (sql, args) the builder returns, when Exec'd
//     directly, produces the SAME database effect the original wrapper function
//     produces. If they ever diverge, a routed subagent-start would silently
//     write a different row than the legacy direct path.
//
//  2. JSON-TRANSPORT SAFETY — every arg round-trips through encoding/json as a
//     plain primitive (string / number / nil / bool). The daemon JSON-encodes
//     args over the wire; a sql.NullString or time.Time would marshal to a shape
//     the SQLite driver cannot re-bind. (assertJSONTransportSafe lives in
//     claim_stmt_test.go — same db_test package.)

// openStmtDB opens an ISOLATED on-disk DB (NOT the package-shared in-memory DB —
// these tests open TWO DBs concurrently to compare effects) with the full schema.
func openStmtDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open isolated db %s: %v", name, err)
	}
	return database
}

// seedSessionRow inserts a session row so BackfillParentSession's UPDATE matches.
func seedSessionRow(t *testing.T, database *sql.DB, sessionID string) {
	t.Helper()
	if err := db.InsertSession(database, &models.Session{
		SessionID: sessionID, AgentAssigned: "claude-code",
		CreatedAt: time.Now().UTC(), Status: "active",
	}); err != nil {
		t.Fatalf("seed session %s: %v", sessionID, err)
	}
}

// TestBackfillParentSessionStmt_EffectMatchesDirect asserts the builder's (sql,
// args), Exec'd directly, sets parent_session_id exactly as the wrapper
// BackfillParentSession does, and that the args are JSON-transport-safe.
func TestBackfillParentSessionStmt_EffectMatchesDirect(t *testing.T) {
	const child = "child-sess"
	const parent = "parent-sess"

	// --- Reference DB: the legacy direct wrapper ---
	refDB := openStmtDB(t, "backfill-ref.db")
	defer refDB.Close()
	seedSessionRow(t, refDB, parent) // parent must exist — sessions.parent_session_id is a FK
	seedSessionRow(t, refDB, child)
	if err := db.BackfillParentSession(refDB, child, parent); err != nil {
		t.Fatalf("ref BackfillParentSession: %v", err)
	}

	// --- Routed DB: the builder + Exec (what the daemon seam applies) ---
	stmtDB := openStmtDB(t, "backfill-stmt.db")
	defer stmtDB.Close()
	seedSessionRow(t, stmtDB, parent)
	seedSessionRow(t, stmtDB, child)
	bfSQL, bfArgs := db.BackfillParentSessionStmt(child, parent)
	assertJSONTransportSafe(t, bfArgs)
	if _, err := stmtDB.Exec(bfSQL, bfArgs...); err != nil {
		t.Fatalf("stmt Exec: %v", err)
	}

	// Effect: both rows carry the same parent_session_id.
	refParent := readParentSession(t, refDB, child)
	stmtParent := readParentSession(t, stmtDB, child)
	if refParent != parent {
		t.Fatalf("ref parent_session_id = %q, want %q — fixture invalid", refParent, parent)
	}
	if stmtParent != refParent {
		t.Errorf("stmt parent_session_id = %q, want %q (effect diverged from direct)", stmtParent, refParent)
	}
}

// TestUpsertPendingSubagentStartStmt_EffectMatchesDirect asserts the builder's
// (sql, args), Exec'd directly, inserts the same pending_subagent_starts row the
// wrapper UpsertPendingSubagentStart does, and that the args are JSON-transport-safe.
// Both a fully-populated and an empty-optional-fields case are exercised so the
// nullableStr(nil) → SQL NULL mapping is verified to match.
func TestUpsertPendingSubagentStartStmt_EffectMatchesDirect(t *testing.T) {
	cases := []struct {
		name string
		p    *db.PendingSubagentStart
	}{
		{
			name: "full",
			p: &db.PendingSubagentStart{
				AgentID: "agent-1", AgentType: "general-purpose", SessionID: "sess-1",
				CWD: "repo/sub", ParentAgentID: "parent-agent", CreatedAt: 1_700_000_000_000_000,
			},
		},
		{
			name: "empty-optionals",
			p: &db.PendingSubagentStart{
				AgentID: "agent-2", AgentType: "researcher", SessionID: "sess-2",
				CWD: "", ParentAgentID: "", CreatedAt: 1_700_000_000_000_001,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refDB := openStmtDB(t, "pending-ref.db")
			defer refDB.Close()
			if err := db.UpsertPendingSubagentStart(refDB, tc.p); err != nil {
				t.Fatalf("ref UpsertPendingSubagentStart: %v", err)
			}
			ref, err := db.GetPendingSubagentStart(refDB, tc.p.AgentID)
			if err != nil || ref == nil {
				t.Fatalf("ref GetPendingSubagentStart: %v (nil=%v)", err, ref == nil)
			}

			stmtDB := openStmtDB(t, "pending-stmt.db")
			defer stmtDB.Close()
			upSQL, upArgs := db.UpsertPendingSubagentStartStmt(tc.p)
			assertJSONTransportSafe(t, upArgs)
			if _, err := stmtDB.Exec(upSQL, upArgs...); err != nil {
				t.Fatalf("stmt Exec: %v", err)
			}
			got, err := db.GetPendingSubagentStart(stmtDB, tc.p.AgentID)
			if err != nil || got == nil {
				t.Fatalf("stmt GetPendingSubagentStart: %v (nil=%v)", err, got == nil)
			}

			if *got != *ref {
				t.Errorf("stmt row %+v diverged from direct %+v", *got, *ref)
			}
		})
	}
}

// TestInsertLineageTraceStmt_EffectMatchesDirect asserts the builder's (sql,
// args), Exec'd directly, inserts the same agent_lineage_trace row the wrapper
// InsertLineageTrace does, and that the args are JSON-transport-safe — proving
// the nullStr→nullableStr swap and the path/time/depth rendering preserve the
// DB effect while making every arg re-bindable over the daemon wire.
func TestInsertLineageTraceStmt_EffectMatchesDirect(t *testing.T) {
	// Optional fields (session_id, agent_name, feature_id) intentionally span
	// populated and empty so the nullableStr→NULL mapping is verified to match
	// the direct nullStr→NULL mapping.
	traces := []*models.LineageTrace{
		{
			TraceID: "trace-full", RootSessionID: "root-1", SessionID: "child-1",
			AgentName: "general-purpose", Depth: 1, Path: []string{"general-purpose"},
			FeatureID: "feat-1", StartedAt: time.Unix(1_700_000_000, 0).UTC(), Status: "active",
		},
		{
			TraceID: "trace-empty", RootSessionID: "root-2", SessionID: "",
			AgentName: "", Depth: 2, Path: []string{"a", "b"},
			FeatureID: "", StartedAt: time.Unix(1_700_000_100, 0).UTC(), Status: "active",
		},
	}
	for _, tr := range traces {
		t.Run(tr.TraceID, func(t *testing.T) {
			refDB := openStmtDB(t, "lineage-ref.db")
			defer refDB.Close()
			if err := db.InsertLineageTrace(refDB, tr); err != nil {
				t.Fatalf("ref InsertLineageTrace: %v", err)
			}
			ref := readLineageByTrace(t, refDB, tr.TraceID)

			stmtDB := openStmtDB(t, "lineage-stmt.db")
			defer stmtDB.Close()
			ltSQL, ltArgs, err := db.InsertLineageTraceStmt(tr)
			if err != nil {
				t.Fatalf("InsertLineageTraceStmt: %v", err)
			}
			assertJSONTransportSafe(t, ltArgs)
			if _, err := stmtDB.Exec(ltSQL, ltArgs...); err != nil {
				t.Fatalf("stmt Exec: %v", err)
			}
			got := readLineageByTrace(t, stmtDB, tr.TraceID)

			if got != ref {
				t.Errorf("stmt lineage row %+v diverged from direct %+v", got, ref)
			}
		})
	}
}

// lineageRow is the persisted shape of an agent_lineage_trace row, scanned with
// COALESCE so a NULL optional column reads as "" — exactly how the direct and
// routed paths must agree (nullStr-NULL vs nullableStr-NULL are indistinguishable).
type lineageRow struct {
	TraceID, RootSessionID, SessionID, AgentName, Path, FeatureID, StartedAt, Status string
	Depth                                                                            int
}

// readLineageByTrace reads the persisted row keyed by trace_id (robust to a NULL
// session_id, which GetLineageBySession("") cannot address).
func readLineageByTrace(t *testing.T, database *sql.DB, traceID string) lineageRow {
	t.Helper()
	var r lineageRow
	err := database.QueryRow(`
		SELECT trace_id, root_session_id,
		       COALESCE(session_id,''), COALESCE(agent_name,''),
		       depth, path, COALESCE(feature_id,''), started_at, status
		FROM agent_lineage_trace WHERE trace_id = ?`, traceID).Scan(
		&r.TraceID, &r.RootSessionID, &r.SessionID, &r.AgentName,
		&r.Depth, &r.Path, &r.FeatureID, &r.StartedAt, &r.Status,
	)
	if err != nil {
		t.Fatalf("read lineage trace %s: %v", traceID, err)
	}
	return r
}

// readParentSession reads parent_session_id for a session row.
func readParentSession(t *testing.T, database *sql.DB, sessionID string) string {
	t.Helper()
	var p sql.NullString
	if err := database.QueryRow(
		`SELECT parent_session_id FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&p); err != nil {
		t.Fatalf("read parent_session_id for %s: %v", sessionID, err)
	}
	return p.String
}
