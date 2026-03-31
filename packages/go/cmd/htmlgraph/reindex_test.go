package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
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

// setupHtmlgraphDir creates a minimal .htmlgraph directory structure in a temp dir.
func setupHtmlgraphDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
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
	hgDir := setupHtmlgraphDir(t)

	// Write a track and feature HTML file.
	writeMinimalTrackHTML(t, filepath.Join(hgDir, "tracks"), "trk-del-001.html", "trk-del-001", "Track To Delete")
	writeMinimalFeatureHTML(t, filepath.Join(hgDir, "features"), "feat-del-001.html", "feat-del-001", "Feature To Delete")

	// Open DB and do an initial reindex (both files exist).
	database, err := dbpkg.Open(filepath.Join(hgDir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	validIDs := map[string]bool{}
	reindexTracks(database, hgDir, "", validIDs)
	reindexFeatureDir(database, hgDir, "", "features", validIDs)

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
	reindexTracks(database, hgDir, "", validIDs2)
	reindexFeatureDir(database, hgDir, "", "features", validIDs2)
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
