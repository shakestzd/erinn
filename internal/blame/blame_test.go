package blame_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/internal/blame"
)

// openTestDB creates an in-memory SQLite database with the full wipnote schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// insertTrack is a test helper that inserts a track with minimal required fields.
func insertTrack(t *testing.T, database *sql.DB, id, title string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.UpsertTrack(database, &db.Track{
		ID:        id,
		Type:      "track",
		Title:     title,
		Status:    "active",
		Priority:  "medium",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("insert track %s: %v", id, err)
	}
}

// insertFeature is a test helper that inserts a feature with minimal required fields.
func insertFeature(t *testing.T, database *sql.DB, id, title, trackID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.UpsertFeature(database, &db.Feature{
		ID:        id,
		Type:      "feature",
		Title:     title,
		Status:    "active",
		Priority:  "medium",
		TrackID:   trackID,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("insert feature %s: %v", id, err)
	}
}

// insertFeatureFile inserts a feature_file row via db.UpsertFeatureFile.
func insertFeatureFile(t *testing.T, database *sql.DB, id, featureID, filePath string) {
	t.Helper()
	if err := db.UpsertFeatureFile(database, &models.FeatureFile{
		ID:        id,
		FeatureID: featureID,
		FilePath:  filePath,
		Operation: "edit",
	}); err != nil {
		t.Fatalf("insert feature_file %s: %v", id, err)
	}
}

func TestQuery_SingleFeature(t *testing.T) {
	database := openTestDB(t)
	insertTrack(t, database, "trk-aaa", "Track A")
	insertFeature(t, database, "feat-aaa", "Feature Alpha", "trk-aaa")
	insertFeatureFile(t, database, "ff-1", "feat-aaa", "cmd/main.go")

	res, err := blame.Query(context.Background(), database, "cmd/main.go", blame.QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Path != "cmd/main.go" {
		t.Errorf("Path: got %q, want %q", res.Path, "cmd/main.go")
	}
	if len(res.Features) != 1 {
		t.Fatalf("Features: got %d, want 1", len(res.Features))
	}
	if res.Features[0].ID != "feat-aaa" {
		t.Errorf("Feature ID: got %q, want feat-aaa", res.Features[0].ID)
	}
	if res.Features[0].TrackID != "trk-aaa" {
		t.Errorf("TrackID: got %q, want trk-aaa", res.Features[0].TrackID)
	}
	if res.Features[0].TrackTitle != "Track A" {
		t.Errorf("TrackTitle: got %q, want Track A", res.Features[0].TrackTitle)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("Tracks: got %d, want 1", len(res.Tracks))
	}
	if res.Tracks[0].ID != "trk-aaa" {
		t.Errorf("Track rollup ID: got %q, want trk-aaa", res.Tracks[0].ID)
	}
	if res.TotalTouchCount != 1 {
		t.Errorf("TotalTouchCount: got %d, want 1", res.TotalTouchCount)
	}
}

func TestQuery_MultiFeatureSameTrack_Rollup(t *testing.T) {
	database := openTestDB(t)
	insertTrack(t, database, "trk-bbb", "Track B")
	insertFeature(t, database, "feat-bbb1", "Feature B1", "trk-bbb")
	insertFeature(t, database, "feat-bbb2", "Feature B2", "trk-bbb")

	insertFeatureFile(t, database, "ff-b1", "feat-bbb1", "pkg/server.go")
	insertFeatureFile(t, database, "ff-b2", "feat-bbb2", "pkg/server.go")

	res, err := blame.Query(context.Background(), database, "pkg/server.go", blame.QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Features) != 2 {
		t.Fatalf("Features: got %d, want 2", len(res.Features))
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("Tracks: got %d, want 1 (same track)", len(res.Tracks))
	}
	if res.Tracks[0].ID != "trk-bbb" {
		t.Errorf("Track ID: got %q", res.Tracks[0].ID)
	}
	if res.Tracks[0].FeatureCount != 2 {
		t.Errorf("FeatureCount: got %d, want 2", res.Tracks[0].FeatureCount)
	}
	if res.TotalTouchCount != 2 {
		t.Errorf("TotalTouchCount: got %d, want 2", res.TotalTouchCount)
	}
}

func TestQuery_MultiTrack_DominantTrack(t *testing.T) {
	database := openTestDB(t)
	insertTrack(t, database, "trk-x", "Track X")
	insertTrack(t, database, "trk-y", "Track Y")
	insertFeature(t, database, "feat-x1", "Feature X1", "trk-x")
	insertFeature(t, database, "feat-x2", "Feature X2", "trk-x")
	insertFeature(t, database, "feat-y1", "Feature Y1", "trk-y")

	insertFeatureFile(t, database, "ff-x1", "feat-x1", "lib/shared.go")
	insertFeatureFile(t, database, "ff-x2", "feat-x2", "lib/shared.go")
	insertFeatureFile(t, database, "ff-y1", "feat-y1", "lib/shared.go")

	res, err := blame.Query(context.Background(), database, "lib/shared.go", blame.QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Features) != 3 {
		t.Fatalf("Features: got %d, want 3", len(res.Features))
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("Tracks: got %d, want 2", len(res.Tracks))
	}
	// trk-x has 2 features so should sort first
	if res.Tracks[0].ID != "trk-x" {
		t.Errorf("dominant track: got %q, want trk-x", res.Tracks[0].ID)
	}
}

func TestQuery_UnknownPath_EmptyNotError(t *testing.T) {
	database := openTestDB(t)

	res, err := blame.Query(context.Background(), database, "nonexistent/file.go", blame.QueryOptions{})
	if err != nil {
		t.Fatalf("unknown path should not error: %v", err)
	}
	if len(res.Features) != 0 {
		t.Errorf("expected no features, got %d", len(res.Features))
	}
	if res.TotalTouchCount != 0 {
		t.Errorf("expected TotalTouchCount=0, got %d", res.TotalTouchCount)
	}
}

func TestQuery_WipnotePath_Error(t *testing.T) {
	database := openTestDB(t)

	_, err := blame.Query(context.Background(), database, ".wipnote/features/feat-abc.html", blame.QueryOptions{})
	if err == nil {
		t.Error("expected error for .wipnote/ path, got nil")
	}
}

func TestQuery_TopN_Limits(t *testing.T) {
	database := openTestDB(t)
	insertTrack(t, database, "trk-top", "Track Top")
	for i := 0; i < 5; i++ {
		fid := fmt.Sprintf("feat-top%d", i)
		insertFeature(t, database, fid, "Feature "+fid, "trk-top")
		insertFeatureFile(t, database, "ff-top-"+fid, fid, "shared/top.go")
	}

	res, err := blame.Query(context.Background(), database, "shared/top.go", blame.QueryOptions{Top: 3})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Features) != 3 {
		t.Errorf("expected 3 features with Top=3, got %d", len(res.Features))
	}
}

func TestQuery_SinceFilter(t *testing.T) {
	database := openTestDB(t)
	insertTrack(t, database, "trk-since", "Track Since")
	insertFeature(t, database, "feat-since1", "Feature Since1", "trk-since")

	insertFeatureFile(t, database, "ff-since", "feat-since1", "filtered/file.go")

	// Filter to a far-future date — nothing should match
	future := time.Now().Add(24 * time.Hour)
	res, err := blame.Query(context.Background(), database, "filtered/file.go", blame.QueryOptions{Since: &future})
	if err != nil {
		t.Fatalf("Query with Since: %v", err)
	}
	if len(res.Features) != 0 {
		t.Errorf("expected 0 features with future Since, got %d", len(res.Features))
	}
}

func TestFormatJSON_RoundTrip(t *testing.T) {
	database := openTestDB(t)
	insertTrack(t, database, "trk-json", "Track JSON")
	insertFeature(t, database, "feat-json", "Feature JSON", "trk-json")
	insertFeatureFile(t, database, "ff-json", "feat-json", "cmd/json.go")

	res, err := blame.Query(context.Background(), database, "cmd/json.go", blame.QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	data, err := blame.FormatJSON(res)
	if err != nil {
		t.Fatalf("FormatJSON: %v", err)
	}

	var decoded blame.Result
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Path != "cmd/json.go" {
		t.Errorf("decoded Path: got %q, want cmd/json.go", decoded.Path)
	}
	if len(decoded.Features) != 1 {
		t.Fatalf("decoded Features: got %d, want 1", len(decoded.Features))
	}
	if decoded.Features[0].ID != "feat-json" {
		t.Errorf("decoded Feature ID: got %q, want feat-json", decoded.Features[0].ID)
	}
}

func TestFormatMarkdown_ContainsHeadings(t *testing.T) {
	database := openTestDB(t)
	insertTrack(t, database, "trk-md", "Track Markdown")
	insertFeature(t, database, "feat-md", "Feature MD", "trk-md")
	insertFeatureFile(t, database, "ff-md", "feat-md", "docs/readme.go")

	res, err := blame.Query(context.Background(), database, "docs/readme.go", blame.QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	md := blame.FormatMarkdown(res)
	if !strings.Contains(md, "## File: docs/readme.go") {
		t.Errorf("markdown missing file heading, got:\n%s", md)
	}
	if !strings.Contains(md, "### Track:") {
		t.Errorf("markdown missing track heading, got:\n%s", md)
	}
	if !strings.Contains(md, "Track Markdown") {
		t.Errorf("markdown missing track title, got:\n%s", md)
	}
}

func TestFormatText_EmptyResult(t *testing.T) {
	res := &blame.Result{Path: "unknown/file.go"}
	text := blame.FormatText(res)
	if !strings.Contains(text, "No features") {
		t.Errorf("text for empty result should mention 'No features', got:\n%s", text)
	}
}
