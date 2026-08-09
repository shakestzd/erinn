package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// openReindexTestDB creates an in-memory SQLite database with the full schema.
func openReindexTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// writeMinimalFeatureHTML writes a minimal valid feature HTML file to dir/filename.
func writeMinimalFeatureHTML(t *testing.T, dir, filename, id, title string) string {
	t.Helper()
	content := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>%s</title></head>
<body>
  <article id="%s"
           data-type="feature"
           data-status="todo"
           data-priority="medium"
           data-created="%s"
           data-updated="%s">
    <header><h1>%s</h1></header>
  </article>
</body>
</html>`, title, id, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339), title)

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write feature HTML %s: %v", path, err)
	}
	return path
}

// writeMinimalTrackHTML writes a minimal valid track HTML file to dir/filename.
func writeMinimalTrackHTML(t *testing.T, dir, filename, id, title string) string {
	t.Helper()
	content := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>%s</title></head>
<body>
  <article id="%s"
           data-type="track"
           data-status="todo"
           data-priority="medium"
           data-created="%s"
           data-updated="%s">
    <header><h1>%s</h1></header>
  </article>
</body>
</html>`, title, id, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339), title)

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write track HTML %s: %v", path, err)
	}
	return path
}

// setupWipnoteDir creates a minimal .wipnote directory structure in a temp dir.
func setupWipnoteDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return hgDir
}

func TestPurgeStaleEntries_StaleFeatureRemoved(t *testing.T) {
	database := openReindexTestDB(t)
	now := time.Now().UTC()

	// Pre-populate DB with a feature that has no backing HTML file.
	stale := &dbpkg.Feature{
		ID:        "feat-stale-001",
		Type:      "feature",
		Title:     "Stale Feature",
		Status:    "todo",
		Priority:  "medium",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := dbpkg.UpsertFeature(database, stale); err != nil {
		t.Fatalf("UpsertFeature: %v", err)
	}

	// validIDs is empty — no HTML files exist for this feature.
	validIDs := map[string]bool{}
	purged, edgesPurged := purgeStaleEntries(database, validIDs)

	if purged != 1 {
		t.Errorf("purged features: got %d, want 1", purged)
	}
	if edgesPurged != 0 {
		t.Errorf("purged edges: got %d, want 0", edgesPurged)
	}

	// Confirm the row is gone.
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-stale-001").Scan(&count)
	if count != 0 {
		t.Errorf("stale feature still in DB: count = %d", count)
	}
}

func TestPurgeStaleEntries_StaleTrackRemoved(t *testing.T) {
	database := openReindexTestDB(t)
	now := time.Now().UTC()

	// Pre-populate DB with a track that has no backing HTML file.
	stale := &dbpkg.Track{
		ID:        "trk-stale-001",
		Type:      "track",
		Title:     "Stale Track",
		Status:    "todo",
		Priority:  "medium",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := dbpkg.UpsertTrack(database, stale); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}

	// validIDs is empty — no HTML files exist for this track.
	validIDs := map[string]bool{}
	purged, edgesPurged := purgeStaleEntries(database, validIDs)

	if purged != 1 {
		t.Errorf("purged items: got %d, want 1 (stale track)", purged)
	}
	if edgesPurged != 0 {
		t.Errorf("purged edges: got %d, want 0", edgesPurged)
	}

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM tracks WHERE id = ?`, "trk-stale-001").Scan(&count)
	if count != 0 {
		t.Errorf("stale track still in DB: count = %d", count)
	}
}

func TestPurgeStaleEntries_ValidEntriesKept(t *testing.T) {
	database := openReindexTestDB(t)
	now := time.Now().UTC()

	track := &dbpkg.Track{
		ID:        "trk-valid-001",
		Type:      "track",
		Title:     "Valid Track",
		Status:    "todo",
		Priority:  "medium",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := dbpkg.UpsertTrack(database, track); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}

	feat := &dbpkg.Feature{
		ID:        "feat-valid-001",
		Type:      "feature",
		Title:     "Valid Feature",
		Status:    "todo",
		Priority:  "medium",
		TrackID:   "trk-valid-001",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := dbpkg.UpsertFeature(database, feat); err != nil {
		t.Fatalf("UpsertFeature: %v", err)
	}

	// Both IDs are in validIDs — their HTML files still exist.
	validIDs := map[string]bool{
		"trk-valid-001":  true,
		"feat-valid-001": true,
	}
	purged, edgesPurged := purgeStaleEntries(database, validIDs)

	if purged != 0 {
		t.Errorf("purged: got %d, want 0 (nothing should be purged)", purged)
	}
	if edgesPurged != 0 {
		t.Errorf("edges purged: got %d, want 0", edgesPurged)
	}

	var trackCount, featCount int
	database.QueryRow(`SELECT COUNT(*) FROM tracks WHERE id = ?`, "trk-valid-001").Scan(&trackCount)
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-valid-001").Scan(&featCount)
	if trackCount != 1 {
		t.Errorf("valid track was incorrectly purged")
	}
	if featCount != 1 {
		t.Errorf("valid feature was incorrectly purged")
	}
}

func TestReindex_DeletedHTMLPurgesDBEntry(t *testing.T) {
	hgDir := setupWipnoteDir(t)

	// Write a track and feature HTML file.
	writeMinimalTrackHTML(t, filepath.Join(hgDir, "tracks"), "trk-del-001.html", "trk-del-001", "Track To Delete")
	writeMinimalFeatureHTML(t, filepath.Join(hgDir, "features"), "feat-del-001.html", "feat-del-001", "Feature To Delete")

	// Open DB and do an initial reindex (both files exist).
	database, err := dbpkg.Open(filepath.Join(hgDir, "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	validIDs := map[string]bool{}
	reindexTracks(database, hgDir, "", validIDs, false)
	reindexFeatureDir(database, hgDir, "", "features", validIDs, false)

	// Confirm both rows exist.
	var tc, fc int
	database.QueryRow(`SELECT COUNT(*) FROM tracks WHERE id = ?`, "trk-del-001").Scan(&tc)
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-del-001").Scan(&fc)
	if tc != 1 || fc != 1 {
		t.Fatalf("initial index: track=%d feature=%d, both should be 1", tc, fc)
	}

	// Delete the HTML files — simulating the user removing work items.
	os.Remove(filepath.Join(hgDir, "tracks", "trk-del-001.html"))
	os.Remove(filepath.Join(hgDir, "features", "feat-del-001.html"))

	// Reindex again with fresh validIDs — deleted files produce empty set.
	validIDs2 := map[string]bool{}
	reindexTracks(database, hgDir, "", validIDs2, false)
	reindexFeatureDir(database, hgDir, "", "features", validIDs2, false)
	purged, _ := purgeStaleEntries(database, validIDs2)

	if purged != 2 {
		t.Errorf("purged: got %d, want 2 (1 track + 1 feature)", purged)
	}

	database.QueryRow(`SELECT COUNT(*) FROM tracks WHERE id = ?`, "trk-del-001").Scan(&tc)
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-del-001").Scan(&fc)
	if tc != 0 {
		t.Errorf("deleted track still in DB")
	}
	if fc != 0 {
		t.Errorf("deleted feature still in DB")
	}
}

func TestPurgeStaleEntries_StaleEdgesRemoved(t *testing.T) {
	database := openReindexTestDB(t)

	// Insert an edge between two node IDs that have no backing HTML files.
	err := dbpkg.InsertEdge(
		database,
		"edge-stale-001", "feat-gone-a", "feature", "feat-gone-b", "feature",
		"blocks", nil,
	)
	if err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}

	validIDs := map[string]bool{} // neither endpoint exists on disk
	purged, edgesPurged := purgeStaleEntries(database, validIDs)

	if edgesPurged != 1 {
		t.Errorf("edges purged: got %d, want 1", edgesPurged)
	}
	_ = purged // may be 0 — no feature/track rows were inserted

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE edge_id = ?`, "edge-stale-001").Scan(&count)
	if count != 0 {
		t.Errorf("stale edge still in DB")
	}
}

// TestPurgeStaleEntries_TombstonedEdgeSurvives pins the purge half of the
// tombstone policy (feat-d1439606) at the function boundary, because the full
// reindex hides it: purgeStaleEntries runs immediately before reindexEdges,
// which re-inserts anything the purge just deleted, so an end-state census
// cannot tell a preserved tombstone from a deleted-and-restored one.
//
// It is observable on the purge-only path — runFullReindex in purge_spikes.go
// calls purgeStaleEntries with no edge pass after it — and it is the invariant
// that keeps the two halves of the gate from contradicting each other if the
// reindex passes are ever reordered.
func TestPurgeStaleEntries_TombstonedEdgeSurvives(t *testing.T) {
	database := openReindexTestDB(t)

	const prunedSession = "77776666-5555-4444-3333-222211110000"
	// Declared by a canonical work item; the session it names has aged out.
	if err := dbpkg.InsertEdge(database,
		"edge-tombstone-001", "feat-live-001", "feature", prunedSession, "unknown",
		"implemented_in", map[string]string{"tombstoned": "session"},
	); err != nil {
		t.Fatalf("InsertEdge tombstone: %v", err)
	}
	// Same source, but the target is a work item that does not exist. This one
	// is a genuine dangling reference and must still go.
	if err := dbpkg.InsertEdge(database,
		"edge-dangling-001", "feat-live-001", "feature", "feat-gone-999", "feature",
		"relates_to", nil,
	); err != nil {
		t.Fatalf("InsertEdge dangling: %v", err)
	}
	// A session-shaped TARGET does not license an unknown SOURCE: an edge from
	// a node nothing on disk backs is not a canonical declaration.
	if err := dbpkg.InsertEdge(database,
		"edge-orphan-source-001", prunedSession, "session", "feat-live-001", "feature",
		"implements", nil,
	); err != nil {
		t.Fatalf("InsertEdge orphan source: %v", err)
	}

	validIDs := map[string]bool{"feat-live-001": true}
	if _, edgesPurged := purgeStaleEntries(database, validIDs); edgesPurged != 2 {
		t.Errorf("edges purged: got %d, want 2 (the dangling reference and the orphan-sourced row)", edgesPurged)
	}

	survived := func(edgeID string) bool {
		var n int
		database.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE edge_id = ?`, edgeID).Scan(&n) //nolint:errcheck
		return n > 0
	}
	if !survived("edge-tombstone-001") {
		t.Errorf("purge deleted the tombstoned edge feat-live-001 -implemented_in-> %s;\n"+
			"the declaration is canonical and git-tracked — only the session is ephemeral", prunedSession)
	}
	if survived("edge-dangling-001") {
		t.Errorf("purge kept the dangling reference feat-live-001 -relates_to-> feat-gone-999; " +
			"only session-shaped targets may tombstone")
	}
	if survived("edge-orphan-source-001") {
		t.Errorf("purge kept an edge whose SOURCE (%s) resolves to nothing", prunedSession)
	}
}
