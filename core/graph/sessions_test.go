package graph_test

import (
	"database/sql"
	"testing"

	"github.com/shakestzd/wipnote/core/graph"
)

func seedSession(t *testing.T, database *sql.DB, sessionID, agent, status string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		sessionID, agent, status,
	)
	if err != nil {
		t.Fatalf("seed session %s: %v", sessionID, err)
	}
}

func seedAgentEvent(t *testing.T, database *sql.DB, eventID, sessionID, featureID string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO agent_events (event_id, agent_id, event_type, session_id, feature_id)
		 VALUES (?, 'claude', 'tool_call', ?, ?)`,
		eventID, sessionID, featureID,
	)
	if err != nil {
		t.Fatalf("seed agent_event %s: %v", eventID, err)
	}
}

func seedFeatureFile(t *testing.T, database *sql.DB, id, featureID, filePath, sessionID string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO feature_files (id, feature_id, file_path, session_id)
		 VALUES (?, ?, ?, ?)`,
		id, featureID, filePath, sessionID,
	)
	if err != nil {
		t.Fatalf("seed feature_file %s: %v", id, err)
	}
}

// --- SessionsForFeature ---

func TestSessionsForFeature_Empty(t *testing.T) {
	database := openTestDB(t)
	results, err := graph.SessionsForFeature(database, "feat-nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSessionsForFeature_ViaAgentEvents(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "Feature A", "todo")
	seedSession(t, database, "sess-1", "claude", "active")
	seedAgentEvent(t, database, "evt-1", "sess-1", "feat-a")

	results, err := graph.SessionsForFeature(database, "feat-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 session, got %d", len(results))
	}
	if results[0].SessionID != "sess-1" {
		t.Errorf("expected sess-1, got %s", results[0].SessionID)
	}
}

func TestSessionsForFeature_ViaFeatureFiles(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-b", "Feature B", "done")
	seedSession(t, database, "sess-2", "claude", "completed")
	seedFeatureFile(t, database, "ff-1", "feat-b", "internal/foo.go", "sess-2")

	results, err := graph.SessionsForFeature(database, "feat-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 session, got %d", len(results))
	}
	if results[0].SessionID != "sess-2" {
		t.Errorf("expected sess-2, got %s", results[0].SessionID)
	}
}

func TestSessionsForFeature_ViaActiveFeature(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-c", "Feature C", "in-progress")
	seedSession(t, database, "sess-3", "claude", "active")
	// Set active_feature_id on the session.
	database.Exec(`UPDATE sessions SET active_feature_id = ? WHERE session_id = ?`,
		"feat-c", "sess-3")

	results, err := graph.SessionsForFeature(database, "feat-c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 session, got %d", len(results))
	}
}

func TestSessionsForFeature_Deduped(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-d", "Feature D", "todo")
	seedSession(t, database, "sess-4", "claude", "active")
	// Same session linked via two sources.
	seedAgentEvent(t, database, "evt-2", "sess-4", "feat-d")
	seedFeatureFile(t, database, "ff-2", "feat-d", "internal/bar.go", "sess-4")

	results, err := graph.SessionsForFeature(database, "feat-d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 deduped session, got %d", len(results))
	}
}

// --- FeaturesForSession ---

func TestFeaturesForSession_Empty(t *testing.T) {
	database := openTestDB(t)
	results, err := graph.FeaturesForSession(database, "sess-nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestFeaturesForSession_MultipleFeatures(t *testing.T) {
	database := openTestDB(t)
	seedSession(t, database, "sess-5", "claude", "completed")
	seedFeature(t, database, "feat-e", "Feature E", "done")
	seedFeature(t, database, "feat-f", "Feature F", "todo")
	seedAgentEvent(t, database, "evt-3", "sess-5", "feat-e")
	seedAgentEvent(t, database, "evt-4", "sess-5", "feat-f")

	results, err := graph.FeaturesForSession(database, "sess-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 features, got %d", len(results))
	}
}
