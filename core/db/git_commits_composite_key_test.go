package db_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestInsertGitCommit_MultipleFeatureIDsSameCommitSession is the direct
// regression test for bug-3bf05d49: git_commits' primary key used to be
// (commit_hash, session_id), so a single commit referencing two or more work
// items under the same session_id (the trailer-ingest path writes every row
// under the constant session_id "trailer-ingest") collided on the PK and
// INSERT OR IGNORE silently dropped every feature_id after the first.
func TestInsertGitCommit_MultipleFeatureIDsSameCommitSession(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	commits := []models.GitCommit{
		{
			CommitHash: "multi0001",
			SessionID:  "trailer-ingest",
			FeatureID:  "feat-aaaa1111",
			Message:    "feat: touches two work items",
			Timestamp:  now,
		},
		{
			CommitHash: "multi0001",
			SessionID:  "trailer-ingest",
			FeatureID:  "bug-bbbb2222",
			Message:    "feat: touches two work items",
			Timestamp:  now,
		},
	}
	for i := range commits {
		if err := db.InsertGitCommit(database, &commits[i]); err != nil {
			t.Fatalf("InsertGitCommit %d: %v", i, err)
		}
	}

	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM git_commits WHERE commit_hash = 'multi0001' AND session_id = 'trailer-ingest'`,
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows (one per feature_id), got %d — the PK collision regressed", count)
	}

	rows, err := database.Query(
		`SELECT feature_id FROM git_commits WHERE commit_hash = 'multi0001' ORDER BY feature_id`,
	)
	if err != nil {
		t.Fatalf("query feature_ids: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var fid string
		if err := rows.Scan(&fid); err != nil {
			t.Fatalf("scan feature_id: %v", err)
		}
		got = append(got, fid)
	}
	want := []string{"bug-bbbb2222", "feat-aaaa1111"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("feature_ids = %v, want %v", got, want)
	}
}

// TestInsertGitCommit_UnattributedDuplicateStillDeduped guards the flip side
// of the fix: feature_id is stored as the empty string rather than NULL specifically so
// that re-inserting the SAME unattributed commit (e.g. a re-run of
// `wipnote ingest commits`, or the hook firing twice) still de-dupes via
// INSERT OR IGNORE. Had feature_id remained nullable, two NULLs are never
// equal under SQLite's UNIQUE index and the second insert would have
// silently duplicated the row instead of being ignored.
func TestInsertGitCommit_UnattributedDuplicateStillDeduped(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		commit := &models.GitCommit{
			CommitHash: "unattributed01",
			SessionID:  "backfill",
			FeatureID:  "", // no attribution
			Message:    "chore: no work item",
			Timestamp:  now,
		}
		if err := db.InsertGitCommit(database, commit); err != nil {
			t.Fatalf("InsertGitCommit run %d: %v", i, err)
		}
	}

	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM git_commits WHERE commit_hash = 'unattributed01'`,
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row (re-insert should be ignored), got %d", count)
	}
}

// TestGitCommitsCompositeKeyMigration_UpgradesLegacyTwoColumnKey simulates a
// pre-bug-3bf05d49 database: a git_commits table created with the old
// two-column PRIMARY KEY (commit_hash, session_id), already holding data.
// Opening it must run migration 018, widen the key to include feature_id,
// preserve every existing row untouched, and accept new inserts that were
// previously impossible (a second feature_id for the same commit+session).
func TestGitCommitsCompositeKeyMigration_UpgradesLegacyTwoColumnKey(t *testing.T) {
	path := fileDBPath(t, "git_commits_legacy_key.db")

	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold Open: %v", err)
	}

	// Recreate the pre-migration (two-column PK) shape and seed one row,
	// mirroring a database that predates migration 018.
	for _, stmt := range []string{
		`DROP TABLE git_commits`,
		`CREATE TABLE git_commits (
			commit_hash TEXT NOT NULL,
			session_id TEXT NOT NULL,
			feature_id TEXT,
			tool_event_id TEXT,
			message TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (commit_hash, session_id)
		)`,
	} {
		if _, err := cold.Exec(stmt); err != nil {
			cold.Close()
			t.Fatalf("seed legacy schema (%s): %v", stmt, err)
		}
	}
	if _, err := cold.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp)
		 VALUES ('legacyhash01', 'sess-legacy', 'feat-legacy1', 'legacy commit', '2026-01-01T00:00:00Z')`,
	); err != nil {
		cold.Close()
		t.Fatalf("seed legacy row: %v", err)
	}
	// Rewind so the warm open below re-runs migration 018 (and any steps
	// after it) against this legacy-shaped table.
	if _, err := cold.Exec("PRAGMA user_version = 17"); err != nil {
		cold.Close()
		t.Fatalf("rewind user_version: %v", err)
	}
	cold.Close()

	warm, err := db.Open(path)
	if err != nil {
		t.Fatalf("warm Open (migration): %v", err)
	}
	defer warm.Close()

	if v := queryUserVersion(t, warm); v != db.CurrentSchemaVersion() {
		t.Fatalf("user_version after migration = %d, want %d", v, db.CurrentSchemaVersion())
	}

	// Pre-existing row must survive the copy-swap unchanged.
	var featureID, message string
	if err := warm.QueryRow(
		`SELECT feature_id, message FROM git_commits WHERE commit_hash = 'legacyhash01'`,
	).Scan(&featureID, &message); err != nil {
		t.Fatalf("query migrated row: %v", err)
	}
	if featureID != "feat-legacy1" || message != "legacy commit" {
		t.Fatalf("migrated row = (%q, %q), want (feat-legacy1, legacy commit)", featureID, message)
	}

	// The new PK must now accept a second feature_id for the same
	// (commit_hash, session_id) pair -- impossible under the old schema.
	if err := db.InsertGitCommit(warm, &models.GitCommit{
		CommitHash: "legacyhash01",
		SessionID:  "sess-legacy",
		FeatureID:  "feat-legacy2",
		Message:    "legacy commit",
		Timestamp:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert second feature_id after migration: %v", err)
	}

	var count int
	if err := warm.QueryRow(
		`SELECT COUNT(*) FROM git_commits WHERE commit_hash = 'legacyhash01'`,
	).Scan(&count); err != nil {
		t.Fatalf("count rows after second insert: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows after widened-key insert, got %d", count)
	}

	// idx_git_commits_feature must have been reinstalled after the
	// DROP-TABLE-based copy-swap.
	var idxName string
	if err := warm.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_git_commits_feature'`,
	).Scan(&idxName); err != nil {
		t.Fatalf("idx_git_commits_feature missing after migration: %v", err)
	}
}
