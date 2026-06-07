package graph_test

import (
	"database/sql"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
)

// openTestDB creates an in-memory SQLite database with the full schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// seedFeature inserts a feature row for testing.
func seedFeature(t *testing.T, database *sql.DB, id, title, status string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', ?, ?)`,
		id, title, status,
	)
	if err != nil {
		t.Fatalf("seed feature %s: %v", id, err)
	}
}

// seedTrack inserts a track row for testing.
func seedTrack(t *testing.T, database *sql.DB, id, title, status string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO tracks (id, title, status) VALUES (?, ?, ?)`,
		id, title, status,
	)
	if err != nil {
		t.Fatalf("seed track %s: %v", id, err)
	}
}

// seedEdge inserts a graph edge for testing.
func seedEdge(t *testing.T, database *sql.DB, fromID, fromType, toID, toType, relType string) {
	t.Helper()
	edgeID := fromID + "-" + relType + "-" + toID
	err := db.InsertEdge(database, edgeID, fromID, fromType, toID, toType, relType, nil)
	if err != nil {
		t.Fatalf("seed edge %s->%s: %v", fromID, toID, err)
	}
}

func TestQueryBuilder_NilDB(t *testing.T) {
	_, err := graph.NewQuery(nil).From("x").Execute()
	if err == nil {
		t.Fatal("expected error for nil DB")
	}
}

func TestQueryBuilder_NoFrom(t *testing.T) {
	database := openTestDB(t)
	_, err := graph.NewQuery(database).Follow("contains").Execute()
	if err == nil {
		t.Fatal("expected error without From()")
	}
}

func TestQueryBuilder_FromOnly(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "Feature A", "todo")

	results, err := graph.NewQuery(database).From("feat-a").Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "feat-a" {
		t.Errorf("expected feat-a, got %s", results[0].ID)
	}
	if results[0].Title != "Feature A" {
		t.Errorf("expected title 'Feature A', got %q", results[0].Title)
	}
}

func TestQueryBuilder_Follow(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")
	seedFeature(t, database, "feat-a", "Feature A", "todo")
	seedFeature(t, database, "feat-b", "Feature B", "done")
	seedEdge(t, database, "trk-1", "track", "feat-a", "feature", "contains")
	seedEdge(t, database, "trk-1", "track", "feat-b", "feature", "contains")

	results, err := graph.NewQuery(database).
		From("trk-1").
		Follow("contains").
		Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestQueryBuilder_FollowThenWhere(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")
	seedFeature(t, database, "feat-a", "Feature A", "todo")
	seedFeature(t, database, "feat-b", "Feature B", "done")
	seedEdge(t, database, "trk-1", "track", "feat-a", "feature", "contains")
	seedEdge(t, database, "trk-1", "track", "feat-b", "feature", "contains")

	results, err := graph.NewQuery(database).
		From("trk-1").
		Follow("contains").
		Where("status", "todo").
		Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "feat-a" {
		t.Errorf("expected feat-a, got %s", results[0].ID)
	}
}

func TestQueryBuilder_ChainedFollows(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")
	seedFeature(t, database, "feat-a", "Feature A", "todo")
	seedFeature(t, database, "feat-b", "Feature B", "done")
	seedEdge(t, database, "trk-1", "track", "feat-a", "feature", "contains")
	seedEdge(t, database, "feat-a", "feature", "feat-b", "feature", "blocked_by")

	results, err := graph.NewQuery(database).
		From("trk-1").
		Follow("contains").
		Follow("blocked_by").
		Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "feat-b" {
		t.Errorf("expected feat-b, got %s", results[0].ID)
	}
}

func TestQueryBuilder_NoResults(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "Feature A", "todo")

	results, err := graph.NewQuery(database).
		From("feat-a").
		Follow("contains").
		Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestQueryBuilder_InvalidFilterField(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "Feature A", "todo")

	_, err := graph.NewQuery(database).
		From("feat-a").
		Where("nonexistent", "val").
		Execute()
	if err == nil {
		t.Fatal("expected error for invalid filter field")
	}
}

func TestQueryBuilder_WhereFiltersTracks(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")
	seedTrack(t, database, "trk-2", "Track 2", "done")
	seedFeature(t, database, "feat-hub", "Hub", "todo")
	seedEdge(t, database, "feat-hub", "feature", "trk-1", "track", "part_of")
	seedEdge(t, database, "feat-hub", "feature", "trk-2", "track", "part_of")

	results, err := graph.NewQuery(database).
		From("feat-hub").
		Follow("part_of").
		Where("status", "active").
		Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "trk-1" {
		t.Errorf("expected trk-1, got %s", results[0].ID)
	}
}

func TestQueryBuilder_UnresolvedNode(t *testing.T) {
	database := openTestDB(t)
	// Edge exists but target has no features/tracks row.
	seedFeature(t, database, "feat-a", "A", "todo")
	seedEdge(t, database, "feat-a", "feature", "sess-orphan", "session", "implemented_in")

	results, err := graph.NewQuery(database).
		From("feat-a").
		Follow("implemented_in").
		Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "sess-orphan" {
		t.Errorf("expected sess-orphan, got %s", results[0].ID)
	}
	// Unresolved node should have empty metadata.
	if results[0].Title != "" {
		t.Errorf("expected empty title for unresolved node, got %q", results[0].Title)
	}
}
