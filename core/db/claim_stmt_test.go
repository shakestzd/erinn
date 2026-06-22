package db_test

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// bug-d792aee6 finding 2: PreToolUse's claim writes are routed through the
// daemon's enqueue-only seam. The seam consumes the parameterized statement the
// builders return, so these tests pin TWO contracts:
//
//   1. EFFECT EQUIVALENCE — the (sql, args) the builders return, when Exec'd
//      directly, produce the SAME database effect the original wrapper functions
//      (HeartbeatClaimByWorkItem / ReapExpiredClaims) produce. If they ever
//      diverge, a routed PreToolUse would silently write a different row than the
//      legacy direct path.
//
//   2. JSON-TRANSPORT SAFETY — every arg round-trips through encoding/json as a
//      plain primitive (string / number / nil). The daemon JSON-encodes args
//      over the wire (core/daemon/apply.DerivedOp); a sql.NullString or time.Time
//      would marshal to a shape the SQLite driver cannot re-bind.

// assertJSONTransportSafe round-trips args through encoding/json and asserts
// every element decodes back to a primitive the SQLite driver can bind: a
// string, a JSON number, or nil. Anything else (object/array) would be a
// transport hazard for the daemon's wire encoding.
func assertJSONTransportSafe(t *testing.T, args []any) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("args not JSON-encodable: %v", err)
	}
	var decoded []any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("args not JSON-decodable: %v", err)
	}
	if len(decoded) != len(args) {
		t.Fatalf("arg count changed across JSON round-trip: got %d want %d", len(decoded), len(args))
	}
	for i, v := range decoded {
		switch v.(type) {
		case string, float64, nil, bool:
			// primitives the modernc.org/sqlite driver binds directly
		default:
			t.Fatalf("arg %d is not a JSON-transport-safe primitive: %T (%v) — "+
				"the daemon cannot re-bind a sql.NullString / time.Time / struct", i, v, v)
		}
	}
}

// seedActiveClaimForStmt opens an ISOLATED on-disk DB (NOT the package-shared
// `file::memory:?cache=shared` setupTestDB uses — these tests open TWO DBs
// concurrently to compare effects, which would collide on the shared in-memory
// session row), seeds a feature + session, and inserts one active claim for
// (feat-test, sess-test) with the given claimID and lease window.
func seedActiveClaimForStmt(t *testing.T, claimID string, lease time.Duration) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "claim-stmt.db"))
	if err != nil {
		t.Fatalf("open isolated db: %v", err)
	}
	now := time.Now().UTC()
	if err := db.InsertSession(database, &models.Session{
		SessionID: "sess-test", AgentAssigned: "claude-code", CreatedAt: now, Status: "active",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-test', 'feature', 'Test Feature', 'in-progress')`,
	); err != nil {
		t.Fatalf("seed feature: %v", err)
	}
	c := makeClaim(claimID, "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, lease); err != nil {
		t.Fatalf("seed ClaimItem: %v", err)
	}
	return database
}

// TestHeartbeatClaimByWorkItemStmt_EffectMatchesDirect asserts the builder's
// (sql, args), Exec'd directly, extends the lease and appends the write path
// exactly as the wrapper HeartbeatClaimByWorkItem does, and that the args are
// JSON-transport-safe.
func TestHeartbeatClaimByWorkItemStmt_EffectMatchesDirect(t *testing.T) {
	const writePath = "internal/foo/bar.go"

	// --- Reference DB: the legacy direct wrapper ---
	refDB := seedActiveClaimForStmt(t, "claim-hb-ref", 5*time.Minute)
	defer refDB.Close()
	refBefore, err := db.GetClaim(refDB, "claim-hb-ref")
	if err != nil {
		t.Fatalf("ref GetClaim before: %v", err)
	}
	if err := db.HeartbeatClaimByWorkItem(refDB, "feat-test", "sess-test", writePath, 30*time.Minute); err != nil {
		t.Fatalf("ref HeartbeatClaimByWorkItem: %v", err)
	}
	refAfter, err := db.GetClaim(refDB, "claim-hb-ref")
	if err != nil {
		t.Fatalf("ref GetClaim after: %v", err)
	}

	// --- Routed DB: the builder + Exec (what the daemon seam applies) ---
	stmtDB := seedActiveClaimForStmt(t, "claim-hb-stmt", 5*time.Minute)
	defer stmtDB.Close()
	stmtBefore, err := db.GetClaim(stmtDB, "claim-hb-stmt")
	if err != nil {
		t.Fatalf("stmt GetClaim before: %v", err)
	}
	hbSQL, hbArgs := db.HeartbeatClaimByWorkItemStmt("feat-test", "sess-test", writePath, 30*time.Minute)
	assertJSONTransportSafe(t, hbArgs)
	res, err := stmtDB.Exec(hbSQL, hbArgs...)
	if err != nil {
		t.Fatalf("stmt Exec: %v", err)
	}
	if rows, _ := res.RowsAffected(); rows != 1 {
		t.Fatalf("stmt Exec affected %d rows, want 1", rows)
	}
	stmtAfter, err := db.GetClaim(stmtDB, "claim-hb-stmt")
	if err != nil {
		t.Fatalf("stmt GetClaim after: %v", err)
	}

	// Effect 1: both extended the lease.
	if !refAfter.LeaseExpiresAt.After(refBefore.LeaseExpiresAt) {
		t.Fatal("ref did not extend lease — fixture invalid")
	}
	if !stmtAfter.LeaseExpiresAt.After(stmtBefore.LeaseExpiresAt) {
		t.Error("stmt path did not extend lease (effect diverged from direct)")
	}

	// Effect 2: both appended the same write-scope path.
	refPaths := extractWriteScopePaths(t, refAfter.WriteScope)
	stmtPaths := extractWriteScopePaths(t, stmtAfter.WriteScope)
	if len(refPaths) != 1 || refPaths[0] != writePath {
		t.Fatalf("ref write_scope paths = %v, want [%s] — fixture invalid", refPaths, writePath)
	}
	if len(stmtPaths) != len(refPaths) || stmtPaths[0] != refPaths[0] {
		t.Errorf("stmt write_scope paths = %v, want %v (effect diverged from direct)", stmtPaths, refPaths)
	}
}

// TestReapExpiredClaimsStmt_EffectMatchesDirect asserts the builder's (sql,
// args), Exec'd directly, expires the same lease-expired claims the wrapper
// ReapExpiredClaims does, and that the args are JSON-transport-safe.
func TestReapExpiredClaimsStmt_EffectMatchesDirect(t *testing.T) {
	// Seed a claim with an ALREADY-expired lease so reap will transition it.
	mkExpired := func(t *testing.T, claimID string) *sql.DB {
		t.Helper()
		database := seedActiveClaimForStmt(t, claimID, 30*time.Minute)
		// Force the lease into the past.
		past := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
		if _, err := database.Exec(
			`UPDATE claims SET lease_expires_at = ? WHERE claim_id = ?`, past, claimID,
		); err != nil {
			t.Fatalf("force-expire %s: %v", claimID, err)
		}
		return database
	}

	// --- Reference DB: the legacy direct wrapper ---
	refDB := mkExpired(t, "claim-reap-ref")
	defer refDB.Close()
	refReaped, err := db.ReapExpiredClaims(refDB)
	if err != nil {
		t.Fatalf("ref ReapExpiredClaims: %v", err)
	}

	// --- Routed DB: the builder + Exec ---
	stmtDB := mkExpired(t, "claim-reap-stmt")
	defer stmtDB.Close()
	reapSQL, reapArgs := db.ReapExpiredClaimsStmt()
	assertJSONTransportSafe(t, reapArgs)
	res, err := stmtDB.Exec(reapSQL, reapArgs...)
	if err != nil {
		t.Fatalf("stmt Exec: %v", err)
	}
	stmtReaped, _ := res.RowsAffected()

	if refReaped != 1 {
		t.Fatalf("ref reaped %d, want 1 — fixture invalid", refReaped)
	}
	if int(stmtReaped) != refReaped {
		t.Errorf("stmt reaped %d, want %d (effect diverged from direct)", stmtReaped, refReaped)
	}

	// The reaped claim must now be status='expired' on the routed DB too.
	var status string
	if err := stmtDB.QueryRow(
		`SELECT status FROM claims WHERE claim_id = ?`, "claim-reap-stmt",
	).Scan(&status); err != nil {
		t.Fatalf("read status after stmt reap: %v", err)
	}
	if status != "expired" {
		t.Errorf("stmt-reaped claim status = %q, want %q", status, "expired")
	}
}

// extractWriteScopePaths decodes the $.paths array out of a write_scope JSON blob.
func extractWriteScopePaths(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var ws struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(raw, &ws); err != nil {
		t.Fatalf("decode write_scope %q: %v", string(raw), err)
	}
	return ws.Paths
}
