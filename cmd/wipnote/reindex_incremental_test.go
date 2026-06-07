package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
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
		{"/dir/.wipnote/features/feat-abc123.html", "feat-abc123"},
		{"/dir/.wipnote/tracks/trk-def456.html", "trk-def456"},
		{"/dir/.wipnote/spikes/spk-xyz.html", "spk-xyz"},
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
	hgDir := setupWipnoteDir(t)
	database, err := dbpkg.Open(filepath.Join(hgDir, "wipnote.db"))
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
	hgDir := setupWipnoteDir(t)
	database, err := dbpkg.Open(filepath.Join(hgDir, "wipnote.db"))
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

// TestIncrementalReindex_PropagatesTrackIDAfterMove verifies that when a feature
// HTML file is updated with a new data-track-id (simulating `bug/feature move`)
// and the file appears in the incremental changed-file list, the SQLite row is
// updated with the correct track_id — not left with the stale value.
func TestIncrementalReindex_PropagatesTrackIDAfterMove(t *testing.T) {
	hgDir := setupWipnoteDir(t)
	database, err := dbpkg.Open(filepath.Join(hgDir, "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()

	// Seed both tracks in the DB so trackExists() returns true for both.
	for _, trk := range []struct{ id, title string }{
		{"trk-old-001", "Old Track"},
		{"trk-new-001", "New Track"},
	} {
		if err := dbpkg.UpsertTrack(database, &dbpkg.Track{
			ID: trk.id, Type: "track", Title: trk.title,
			Status: "todo", Priority: "medium", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("UpsertTrack %s: %v", trk.id, err)
		}
	}

	// Seed the feature with the old track.
	if err := dbpkg.UpsertFeature(database, &dbpkg.Feature{
		ID: "bug-move-001", Type: "bug", Title: "Move Me",
		Status: "todo", Priority: "medium",
		TrackID: "trk-old-001", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertFeature (initial): %v", err)
	}

	// Write HTML file with the new track ID — simulating what `bug move` writes.
	path := writeFeatureHTMLWithTrack(t, filepath.Join(hgDir, "bugs"), "bug-move-001.html",
		"bug-move-001", "bug", "Move Me", "trk-new-001")

	// Run incremental reindex as if the changed file was detected.
	validIDs := map[string]bool{}
	_, upserted, errCount := reindexFromFileLists(database, []string{path}, nil, validIDs)

	if upserted != 1 {
		t.Errorf("upserted: got %d, want 1", upserted)
	}
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}

	var gotTrackID string
	database.QueryRow(`SELECT COALESCE(track_id,'') FROM features WHERE id = ?`, "bug-move-001").Scan(&gotTrackID)
	if gotTrackID != "trk-new-001" {
		t.Errorf("track_id after incremental reindex: got %q, want %q", gotTrackID, "trk-new-001")
	}
}

// TestIncrementalReindex_DirtyDeletion verifies that when a feature HTML file
// is deleted in the working tree (dirty deletion, before commit) and detected
// via dirty-files scan, the SQLite row for that feature is removed (not left stale).
func TestIncrementalReindex_DirtyDeletion(t *testing.T) {
	hgDir := setupWipnoteDir(t)
	database, err := dbpkg.Open(filepath.Join(hgDir, "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Create and index a feature HTML file.
	path := writeMinimalFeatureHTML(t, filepath.Join(hgDir, "features"), "feat-dirty-del.html", "feat-dirty-del", "Dirty Delete Me")

	// Initial reindex to populate DB.
	validIDs := map[string]bool{}
	_, upserted, errCount := reindexFromFileLists(database, []string{path}, nil, validIDs)
	if upserted != 1 {
		t.Fatalf("initial upserted: got %d, want 1", upserted)
	}
	if errCount != 0 {
		t.Fatalf("initial errCount: got %d, want 0", errCount)
	}

	var countBefore int
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-dirty-del").Scan(&countBefore)
	if countBefore != 1 {
		t.Fatalf("feature in DB before deletion: want 1, got %d", countBefore)
	}

	// Simulate dirty deletion: remove the file from disk (but don't commit).
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove file: %v", err)
	}

	// Run incremental reindex with the deleted path in the deletion list.
	// This simulates what would happen if git diff --name-status detected "D" status.
	_, _, _ = reindexFromFileLists(database, nil, []string{path}, map[string]bool{})

	// Verify the feature is removed from the DB.
	var countAfter int
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-dirty-del").Scan(&countAfter)
	if countAfter != 0 {
		t.Errorf("dirty-deleted feature still in DB: count = %d (want 0)", countAfter)
	}
}

// TestDeduplicatePaths verifies that deduplicatePaths removes duplicates while
// preserving order and not modifying the original slice.
func TestDeduplicatePaths(t *testing.T) {
	input := []string{"/a/b.html", "/c/d.html", "/a/b.html", "/e/f.html", "/c/d.html"}
	got := deduplicatePaths(input)
	want := []string{"/a/b.html", "/c/d.html", "/e/f.html"}
	if len(got) != len(want) {
		t.Fatalf("deduplicatePaths: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("deduplicatePaths[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// writeFeatureHTMLWithTrack writes a minimal HTML file for any work-item type
// (feature, bug, spike) with a specific data-track-id attribute.
func writeFeatureHTMLWithTrack(t *testing.T, dir, filename, id, itemType, title, trackID string) string {
	t.Helper()
	content := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>%s</title></head>
<body>
  <article id="%s"
           data-type="%s"
           data-status="todo"
           data-priority="medium"
           data-track-id="%s"
           data-created="%s"
           data-updated="%s">
    <header><h1>%s</h1></header>
  </article>
</body>
</html>`, title, id, itemType, trackID,
		time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339), title)

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write HTML %s: %v", path, err)
	}
	return path
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
