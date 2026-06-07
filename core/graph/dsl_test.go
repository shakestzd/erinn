package graph_test

import (
	"testing"

	"github.com/shakestzd/wipnote/core/graph"
)

// --- Tokenizer ---

func TestExecuteDSL_SimpleChain(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-x", "X", "todo")
	seedFeature(t, database, "feat-y", "Y", "done")
	seedEdge(t, database, "feat-x", "feature", "feat-y", "feature", "contains")

	results, err := graph.ExecuteDSL(database, "features -> contains -> features")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "feat-y" {
		t.Errorf("expected [feat-y], got %v", results)
	}
}

func TestTokenize_Empty(t *testing.T) {
	database := openTestDB(t)
	_, err := graph.ExecuteDSL(database, "")
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestTokenize_UnclosedBracket(t *testing.T) {
	database := openTestDB(t)
	_, err := graph.ExecuteDSL(database, "features[status=todo")
	if err == nil {
		t.Fatal("expected error for unclosed bracket")
	}
}

func TestTokenize_BadFilter(t *testing.T) {
	database := openTestDB(t)
	_, err := graph.ExecuteDSL(database, "features[badfield]")
	if err == nil {
		t.Fatal("expected error for filter without =")
	}
}

// --- ExecuteDSL ---

func TestExecuteDSL_SimpleType(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "A", "todo")
	seedFeature(t, database, "feat-b", "B", "done")

	results, err := graph.ExecuteDSL(database, "features")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 features, got %d", len(results))
	}
}

func TestExecuteDSL_TypeWithFilter(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "A", "todo")
	seedFeature(t, database, "feat-b", "B", "done")

	results, err := graph.ExecuteDSL(database, "features[status=todo]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "feat-a" {
		t.Errorf("expected [feat-a], got %v", results)
	}
}

func TestExecuteDSL_FollowRelation(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")
	seedFeature(t, database, "feat-a", "A", "todo")
	seedFeature(t, database, "feat-b", "B", "done")
	seedEdge(t, database, "trk-1", "track", "feat-a", "feature", "contains")
	seedEdge(t, database, "trk-1", "track", "feat-b", "feature", "contains")

	results, err := graph.ExecuteDSL(database, "tracks -> contains -> features")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 features, got %d", len(results))
	}
}

func TestExecuteDSL_FollowWithFilter(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")
	seedFeature(t, database, "feat-a", "A", "todo")
	seedFeature(t, database, "feat-b", "B", "done")
	seedEdge(t, database, "trk-1", "track", "feat-a", "feature", "contains")
	seedEdge(t, database, "trk-1", "track", "feat-b", "feature", "contains")

	results, err := graph.ExecuteDSL(database,
		"tracks -> contains -> features[status=todo]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "feat-a" {
		t.Errorf("expected [feat-a], got %v", results)
	}
}

func TestExecuteDSL_ChainedRelations(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")
	seedFeature(t, database, "feat-a", "A", "todo")
	seedFeature(t, database, "feat-b", "B", "done")
	seedEdge(t, database, "trk-1", "track", "feat-a", "feature", "contains")
	seedEdge(t, database, "feat-a", "feature", "feat-b", "feature", "blocked_by")

	results, err := graph.ExecuteDSL(database,
		"tracks -> contains -> blocked_by -> features")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "feat-b" {
		t.Errorf("expected [feat-b], got %v", results)
	}
}

func TestExecuteDSL_ChainedWithFilters(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "A", "todo")
	seedFeature(t, database, "feat-b", "B", "done")
	seedFeature(t, database, "feat-c", "C", "todo")
	seedEdge(t, database, "feat-a", "feature", "feat-b", "feature", "blocked_by")
	seedEdge(t, database, "feat-a", "feature", "feat-c", "feature", "blocked_by")

	results, err := graph.ExecuteDSL(database,
		"features[status=todo] -> blocked_by -> features[status=done]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "feat-b" {
		t.Errorf("expected [feat-b], got %v", results)
	}
}

func TestExecuteDSL_NoResults(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "A", "todo")

	results, err := graph.ExecuteDSL(database,
		"features -> blocked_by -> features")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestExecuteDSL_PluralAndSingular(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")

	// Both "tracks" and "track" should work.
	r1, err := graph.ExecuteDSL(database, "tracks")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r2, err := graph.ExecuteDSL(database, "track")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r1) != len(r2) {
		t.Errorf("singular and plural should return same count: %d vs %d", len(r1), len(r2))
	}
}

func TestExecuteDSL_InvalidField(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "A", "todo")

	_, err := graph.ExecuteDSL(database, "features[nonexistent=val]")
	if err == nil {
		t.Fatal("expected error for invalid filter field")
	}
}
