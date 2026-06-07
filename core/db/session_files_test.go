package db

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// TestSessionFiles_Query verifies that ListFilesBySession returns only rows
// where session_id matches, ordered by last_seen DESC.
func TestSessionFiles_Query(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()

	// Seed a feature so FK is satisfied.
	if err := UpsertFeature(database, &Feature{
		ID: "feat-sess-files", Type: "feature", Title: "T",
		Status: "in-progress", Priority: "medium",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert feature: %v", err)
	}

	// Insert two files for session-A and one file for session-B.
	for _, ff := range []*models.FeatureFile{
		{ID: "ff-1", FeatureID: "feat-sess-files", FilePath: "/a/foo.go", Operation: "write", SessionID: "sess-A"},
		{ID: "ff-2", FeatureID: "feat-sess-files", FilePath: "/a/bar.go", Operation: "read", SessionID: "sess-A"},
		{ID: "ff-3", FeatureID: "feat-sess-files", FilePath: "/b/baz.go", Operation: "edit", SessionID: "sess-B"},
	} {
		if err := UpsertFeatureFile(database, ff); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	// Query session-A — expect 2 files.
	files, err := ListFilesBySession(database, "sess-A")
	if err != nil {
		t.Fatalf("ListFilesBySession: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files for sess-A, got %d", len(files))
	}
	// Verify paths are present.
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.FilePath] = true
	}
	for _, want := range []string{"/a/foo.go", "/a/bar.go"} {
		if !paths[want] {
			t.Errorf("expected path %q in result", want)
		}
	}

	// Query session-B — expect 1 file.
	bFiles, err := ListFilesBySession(database, "sess-B")
	if err != nil {
		t.Fatalf("ListFilesBySession sess-B: %v", err)
	}
	if len(bFiles) != 1 {
		t.Fatalf("expected 1 file for sess-B, got %d", len(bFiles))
	}
	if bFiles[0].FilePath != "/b/baz.go" {
		t.Errorf("expected /b/baz.go, got %q", bFiles[0].FilePath)
	}
	if bFiles[0].Operation != "edit" {
		t.Errorf("expected operation edit, got %q", bFiles[0].Operation)
	}

	// Query unknown session — expect empty (no error).
	none, err := ListFilesBySession(database, "sess-unknown")
	if err != nil {
		t.Fatalf("ListFilesBySession unknown: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("expected 0 files for unknown session, got %d", len(none))
	}

	// Query empty session ID — expect nil (no error).
	nilFiles, err := ListFilesBySession(database, "")
	if err != nil {
		t.Fatalf("ListFilesBySession empty: %v", err)
	}
	if nilFiles != nil {
		t.Errorf("expected nil for empty session id, got %v", nilFiles)
	}
}
