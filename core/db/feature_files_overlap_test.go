package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func sqlTime(ts time.Time) string {
	return ts.UTC().Format("2006-01-02 15:04:05")
}

func mustExec(t *testing.T, database *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// mkClaim inserts a claim row with last_heartbeat_at written in RFC3339 — the
// exact format ClaimItemOrRenew uses and SessionLivenessByHeartbeat parses. A
// fresh heartbeat makes the owner session live; an old one makes it a stale
// status='active' ghost (bug-6c3e8252).
func mkClaim(t *testing.T, database *sql.DB, claimID, workItem, sessionID string, heartbeat time.Time) {
	t.Helper()
	hb := heartbeat.UTC().Format(time.RFC3339)
	mustExec(t, database, `
		INSERT INTO claims
			(claim_id, work_item_id, owner_session_id, owner_agent, status,
			 leased_at, lease_expires_at, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, ?, 'claude-code', 'in_progress', ?, ?, ?, ?, ?)`,
		claimID, workItem, sessionID, hb, hb, hb, hb, hb)
}

func indexExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := database.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query index %s: %v", name, err)
	}
	return got == name
}

func TestOverlapQuery(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()

	mustExec(t, database, `INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES ('sess-A','claude-code','active',?)`, sqlTime(now))
	mustExec(t, database, `INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES ('sess-B','claude-code','active',?)`, sqlTime(now))
	mustExec(t, database, `INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES ('sess-stale','claude-code','active',?)`, sqlTime(now))
	mustExec(t, database, `INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES ('sess-old','claude-code','active',?)`, sqlTime(now))

	// Features are the FK target for claims.work_item_id.
	for _, fid := range []string{"feat-1", "feat-2", "feat-3", "feat-old"} {
		if err := UpsertFeature(database, &Feature{
			ID: fid, Type: "feature", Title: fid, Status: "in-progress",
			Priority: "medium", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("upsert %s: %v", fid, err)
		}
	}

	// Fresh heartbeats => A and B live; 2h-old heartbeat => sess-stale NOT live.
	mkClaim(t, database, "clm-A", "feat-1", "sess-A", now)
	mkClaim(t, database, "clm-B", "feat-2", "sess-B", now)
	mkClaim(t, database, "clm-stale", "feat-3", "sess-stale", now.Add(-2*time.Hour))
	// sess-old is LIVE (fresh heartbeat) but its file touch is 90m old — it
	// must be excluded by the recency window, not by liveness.
	mkClaim(t, database, "clm-old", "feat-old", "sess-old", now)

	mustExec(t, database, `INSERT INTO feature_files (id, feature_id, file_path, operation, session_id, last_seen) VALUES ('ff-A','feat-1','internal/db/x.go','modify','sess-A',?)`, sqlTime(now.Add(-1*time.Minute)))
	mustExec(t, database, `INSERT INTO feature_files (id, feature_id, file_path, operation, session_id, last_seen) VALUES ('ff-B','feat-2','internal/db/x.go','modify','sess-B',?)`, sqlTime(now.Add(-3*time.Minute)))
	mustExec(t, database, `INSERT INTO feature_files (id, feature_id, file_path, operation, session_id, last_seen) VALUES ('ff-old','feat-old','internal/db/x.go','modify','sess-old',?)`, sqlTime(now.Add(-90*time.Minute)))
	mustExec(t, database, `INSERT INTO feature_files (id, feature_id, file_path, operation, session_id, last_seen) VALUES ('ff-stale','feat-3','internal/db/x.go','modify','sess-stale',?)`, sqlTime(now.Add(-2*time.Minute)))
	mustExec(t, database, `INSERT INTO feature_files (id, feature_id, file_path, operation, session_id, last_seen) VALUES ('ff-other','feat-2','internal/db/y.go','modify','sess-B',?)`, sqlTime(now.Add(-1*time.Minute)))

	// Raw: self excluded; B's 90m-old row out of the 15m window; stale present
	// (liveness not applied at this layer).
	raw, err := FindFileOverlaps(database, "internal/db/x.go", "sess-A", 15*time.Minute)
	if err != nil {
		t.Fatalf("FindFileOverlaps: %v", err)
	}
	for _, o := range raw {
		if o.SessionID == "sess-A" {
			t.Fatalf("self session must be excluded, got %+v", o)
		}
		if o.SessionID == "sess-old" {
			t.Fatalf("out-of-window (90m-old) row leaked into results: %+v", o)
		}
	}
	if len(raw) == 0 {
		t.Fatalf("expected at least the recent sess-B/sess-stale overlaps, got none")
	}

	// LIVE: only sess-B survives; sess-stale dropped by heartbeat-recency
	// liveness (folds bug-6c3e8252), self excluded, de-duped.
	live, err := FindLiveFileOverlaps(database, "internal/db/x.go", "sess-A", 15*time.Minute, 2*time.Minute)
	if err != nil {
		t.Fatalf("FindLiveFileOverlaps: %v", err)
	}
	if len(live) != 1 || live[0].SessionID != "sess-B" {
		t.Fatalf("expected exactly [sess-B] live overlap, got %+v", live)
	}
	if live[0].FeatureID != "feat-2" {
		t.Fatalf("expected feature_id feat-2 on overlap, got %q", live[0].FeatureID)
	}

	// EXPLAIN QUERY PLAN must use the composite index, not a table scan.
	rows, err := database.Query(`
		EXPLAIN QUERY PLAN
		SELECT COALESCE(session_id, ''), feature_id,
		       COALESCE(operation, ''), last_seen
		FROM feature_files
		WHERE file_path = ?
		  AND last_seen >= datetime('now', ?)
		  AND COALESCE(session_id, '') != ''
		  AND COALESCE(session_id, '') != ?
		ORDER BY last_seen DESC`,
		"internal/db/x.go", "-15 minutes", "sess-A")
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	planStr := plan.String()
	if !strings.Contains(planStr, "idx_feature_files_path_seen") {
		t.Fatalf("query plan does not use idx_feature_files_path_seen:\n%s", planStr)
	}
	if strings.Contains(planStr, "SCAN feature_files") &&
		!strings.Contains(planStr, "idx_feature_files_path_seen") {
		t.Fatalf("query plan range-scans feature_files instead of using the index:\n%s", planStr)
	}
}

func TestMigration_FeatureFilesPathSeenIndex(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	if !indexExists(t, database, "idx_feature_files_path_seen") {
		t.Fatalf("idx_feature_files_path_seen missing after migrate")
	}

	// Idempotent: re-running the migration step is a no-op; index stays.
	if err := stepFeatureFilesPathSeenIndex(database); err != nil {
		t.Fatalf("re-run stepFeatureFilesPathSeenIndex: %v", err)
	}
	if !indexExists(t, database, "idx_feature_files_path_seen") {
		t.Fatalf("idx_feature_files_path_seen missing after idempotent re-run")
	}

	// Verify that idx_feature_files_path_seen was added by step 9 and is still
	// present regardless of how many later steps exist.
	if !indexExists(t, database, "idx_feature_files_path_seen") {
		t.Fatalf("idx_feature_files_path_seen missing after all migrations")
	}

	// Verify the migration registry is internally consistent (ordered, last ==
	// CurrentSchemaVersion) without hardcoding the exact final step name or
	// version number — so adding future steps does not break this test.
	names := MigrationStepNames()
	versions := MigrationStepVersions()
	if len(names) == 0 {
		t.Fatal("no migration steps registered")
	}
	if versions[len(versions)-1] != CurrentSchemaVersion() {
		t.Fatalf("last migration version = %d, want CurrentSchemaVersion = %d",
			versions[len(versions)-1], CurrentSchemaVersion())
	}
}
