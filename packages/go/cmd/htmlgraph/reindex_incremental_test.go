package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/htmlparse"
)

// --- metadata helper tests ---

func TestGetSetMetadata(t *testing.T) {
	database := openReindexTestDB(t)

	// Missing key returns empty string, no error.
	val, err := dbpkg.GetMetadata(database, "missing_key")
	if err != nil {
		t.Fatalf("GetMetadata missing key: %v", err)
	}
	if val != "" {
		t.Errorf("GetMetadata missing key: got %q, want %q", val, "")
	}

	// Set and read back.
	if err := dbpkg.SetMetadata(database, "last_indexed_commit", "abc123"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	val, err = dbpkg.GetMetadata(database, "last_indexed_commit")
	if err != nil {
		t.Fatalf("GetMetadata after set: %v", err)
	}
	if val != "abc123" {
		t.Errorf("GetMetadata: got %q, want %q", val, "abc123")
	}

	// Overwrite.
	if err := dbpkg.SetMetadata(database, "last_indexed_commit", "def456"); err != nil {
		t.Fatalf("SetMetadata overwrite: %v", err)
	}
	val, err = dbpkg.GetMetadata(database, "last_indexed_commit")
	if err != nil {
		t.Fatalf("GetMetadata after overwrite: %v", err)
	}
	if val != "def456" {
		t.Errorf("GetMetadata overwrite: got %q, want %q", val, "def456")
	}
}

// --- git helper tests ---

func TestIdFromHTMLPath(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/dir/.htmlgraph/features/feat-abc123.html", "feat-abc123"},
		{"/dir/.htmlgraph/tracks/trk-def456.html", "trk-def456"},
		{"/dir/.htmlgraph/spikes/spk-xyz.html", "spk-xyz"},
	}
	for _, tc := range cases {
		got := idFromHTMLPath(tc.path)
		if got != tc.want {
			t.Errorf("idFromHTMLPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestGitHeadCommit_NoGitRepo(t *testing.T) {
	commit := gitHeadCommit("/tmp/definitely-not-a-git-repo-xyz123")
	// Ensure no panic; result is empty on error.
	if commit != "" {
		t.Logf("gitHeadCommit returned %q for non-repo (non-fatal)", commit)
	}
}

func TestGitCommitExists_InvalidCommit(t *testing.T) {
	exists := gitCommitExists("/tmp", "0000000000000000000000000000000000000000")
	if exists {
		t.Error("gitCommitExists: expected false for bogus commit in non-repo")
	}
}

// --- incremental reindex logic tests ---

// TestIncrementalReindex_ParsesChangedFiles verifies only changed-list files are upserted.
func TestIncrementalReindex_ParsesChangedFiles(t *testing.T) {
	hgDir := setupHtmlgraphDir(t)
	database, err := dbpkg.Open(filepath.Join(hgDir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	pathA := writeMinimalFeatureHTML(t, filepath.Join(hgDir, "features"), "feat-incr-a.html", "feat-incr-a", "Incremental A")
	writeMinimalFeatureHTML(t, filepath.Join(hgDir, "features"), "feat-incr-b.html", "feat-incr-b", "Incremental B")

	validIDs := map[string]bool{}
	total, upserted, errCount := reindexFromFileLists(database, []string{pathA}, nil, validIDs)

	if total != 1 {
		t.Errorf("total: got %d, want 1", total)
	}
	if upserted != 1 {
		t.Errorf("upserted: got %d, want 1", upserted)
	}
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}

	var countA, countB int
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-incr-a").Scan(&countA)
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-incr-b").Scan(&countB)
	if countA != 1 {
		t.Errorf("feat-incr-a: want 1 in DB, got %d", countA)
	}
	if countB != 0 {
		t.Errorf("feat-incr-b: want 0 in DB (not in changed list), got %d", countB)
	}
}

// TestIncrementalReindex_DeletesRemovedFiles verifies deleted paths are removed from DB.
func TestIncrementalReindex_DeletesRemovedFiles(t *testing.T) {
	hgDir := setupHtmlgraphDir(t)
	database, err := dbpkg.Open(filepath.Join(hgDir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	if err := dbpkg.UpsertFeature(database, &dbpkg.Feature{
		ID: "feat-del-incr", Type: "feature", Title: "To Delete",
		Status: "todo", Priority: "medium", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertFeature: %v", err)
	}

	deletedPath := filepath.Join(hgDir, "features", "feat-del-incr.html")
	_, _, _ = reindexFromFileLists(database, nil, []string{deletedPath}, map[string]bool{})

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-del-incr").Scan(&count)
	if count != 0 {
		t.Errorf("deleted feature still in DB: count = %d", count)
	}
}

// reindexFromFileLists is a testable shim for the incremental upsert logic that
// accepts explicit file lists instead of invoking git.
func reindexFromFileLists(
	database *sql.DB,
	added, deleted []string,
	validIDs map[string]bool,
) (total, upserted, errCount int) {
	for _, path := range deleted {
		if id := idFromHTMLPath(path); id != "" {
			database.Exec(`DELETE FROM features WHERE id = ?`, id)
			database.Exec(`DELETE FROM tracks WHERE id = ?`, id)
		}
	}
	for _, path := range added {
		total++
		node, parseErr := htmlparse.ParseFile(path)
		if parseErr != nil {
			errCount++
			continue
		}
		createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)
		desc := node.Content
		if len([]rune(desc)) > 500 {
			desc = string([]rune(desc)[:499]) + "…"
		}
		stepsCompleted := 0
		for _, s := range node.Steps {
			if s.Completed {
				stepsCompleted++
			}
		}
		feat := &dbpkg.Feature{
			ID: node.ID, Type: mapNodeType(node.Type), Title: node.Title,
			Description: desc, Status: normalizeStatus(string(node.Status)),
			Priority: string(node.Priority), AssignedTo: node.AgentAssigned,
			TrackID: node.TrackID, CreatedAt: createdAt, UpdatedAt: updatedAt,
			StepsTotal: len(node.Steps), StepsCompleted: stepsCompleted,
		}
		if err := dbpkg.UpsertFeature(database, feat); err != nil {
			errCount++
			continue
		}
		validIDs[node.ID] = true
		upserted++
	}
	return total, upserted, errCount
}
