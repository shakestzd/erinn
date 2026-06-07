package db_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

func insertTestFeatures(t *testing.T, database *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		database.Exec(`INSERT OR IGNORE INTO features (id, type, title, status) VALUES (?, 'feature', 'test', 'todo')`, id)
	}
}

func TestCountEventsByTool(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	insertTestFeatures(t, database, "feat-aaa", "feat-bbb")

	now := time.Now().UTC()
	insertEvents := []struct {
		id   string
		tool string
		feat string
	}{
		{"evt-cet-1", "Read", "feat-aaa"},
		{"evt-cet-2", "Read", "feat-aaa"},
		{"evt-cet-3", "Read", "feat-bbb"},
		{"evt-cet-4", "Bash", "feat-aaa"},
		{"evt-cet-5", "", ""}, // empty tool_name — should be excluded
		{"evt-cet-6", "Grep", ""},
	}

	for i, ev := range insertEvents {
		e := &models.AgentEvent{
			EventID:   ev.id,
			AgentID:   "claude-code",
			EventType: models.EventToolCall,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			ToolName:  ev.tool,
			FeatureID: ev.feat,
			SessionID: "sess-test",
			Status:    "completed",
			Source:    "hook",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := db.InsertEvent(database, e); err != nil {
			t.Fatalf("InsertEvent %s: %v", ev.id, err)
		}
	}

	counts, err := db.CountEventsByTool(database, "sess-test")
	if err != nil {
		t.Fatalf("CountEventsByTool: %v", err)
	}

	if counts["Read"] != 3 {
		t.Errorf("Read count: got %d, want 3", counts["Read"])
	}
	if counts["Bash"] != 1 {
		t.Errorf("Bash count: got %d, want 1", counts["Bash"])
	}
	if counts["Grep"] != 1 {
		t.Errorf("Grep count: got %d, want 1", counts["Grep"])
	}
	// Empty tool_name should not appear.
	if _, ok := counts[""]; ok {
		t.Errorf("empty tool_name should be excluded from counts")
	}

	// Different session returns empty map.
	counts2, err := db.CountEventsByTool(database, "sess-other")
	if err != nil {
		t.Fatalf("CountEventsByTool (other session): %v", err)
	}
	if len(counts2) != 0 {
		t.Errorf("expected empty map for unknown session, got %v", counts2)
	}
}

func TestDistinctFeatureIDs(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	insertTestFeatures(t, database, "feat-aaa", "feat-bbb", "feat-ccc")

	now := time.Now().UTC()
	insertEvents := []struct {
		id   string
		feat string
	}{
		{"evt-dfi-1", "feat-aaa"},
		{"evt-dfi-2", "feat-aaa"}, // duplicate — should deduplicate
		{"evt-dfi-3", "feat-bbb"},
		{"evt-dfi-4", ""}, // empty feature_id — should be excluded
		{"evt-dfi-5", "feat-ccc"},
	}

	for i, ev := range insertEvents {
		e := &models.AgentEvent{
			EventID:   ev.id,
			AgentID:   "claude-code",
			EventType: models.EventToolCall,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			ToolName:  "Read",
			FeatureID: ev.feat,
			SessionID: "sess-test",
			Status:    "completed",
			Source:    "hook",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := db.InsertEvent(database, e); err != nil {
			t.Fatalf("InsertEvent %s: %v", ev.id, err)
		}
	}

	feats, err := db.DistinctFeatureIDs(database, "sess-test")
	if err != nil {
		t.Fatalf("DistinctFeatureIDs: %v", err)
	}

	if len(feats) != 3 {
		t.Errorf("expected 3 distinct feature IDs, got %d: %v", len(feats), feats)
	}

	featSet := make(map[string]bool, len(feats))
	for _, f := range feats {
		featSet[f] = true
	}
	for _, want := range []string{"feat-aaa", "feat-bbb", "feat-ccc"} {
		if !featSet[want] {
			t.Errorf("expected feature %q in result, got %v", want, feats)
		}
	}
	if featSet[""] {
		t.Errorf("empty feature_id should be excluded")
	}

	// Different session returns empty slice.
	feats2, err := db.DistinctFeatureIDs(database, "sess-other")
	if err != nil {
		t.Fatalf("DistinctFeatureIDs (other session): %v", err)
	}
	if len(feats2) != 0 {
		t.Errorf("expected empty slice for unknown session, got %v", feats2)
	}
}

func TestGetCommitsBySession(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	commits := []models.GitCommit{
		{
			CommitHash: "abc1234",
			SessionID:  "sess-test",
			FeatureID:  "feat-aaa",
			Message:    "feat: first commit",
			Timestamp:  now,
		},
		{
			CommitHash: "def5678",
			SessionID:  "sess-test",
			FeatureID:  "feat-bbb",
			Message:    "feat: second commit",
			Timestamp:  now.Add(time.Second),
		},
		{
			CommitHash: "ghi9012",
			SessionID:  "sess-other",
			FeatureID:  "feat-ccc",
			Message:    "feat: other session commit",
			Timestamp:  now.Add(2 * time.Second),
		},
	}

	for i := range commits {
		if err := db.InsertGitCommit(database, &commits[i]); err != nil {
			t.Fatalf("InsertGitCommit %s: %v", commits[i].CommitHash, err)
		}
	}

	results, err := db.GetCommitsBySession(database, "sess-test")
	if err != nil {
		t.Fatalf("GetCommitsBySession: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 commits for sess-test, got %d", len(results))
	}

	// Should be ordered DESC by timestamp.
	if results[0].CommitHash != "def5678" {
		t.Errorf("first result: got %q, want %q", results[0].CommitHash, "def5678")
	}
	if results[1].CommitHash != "abc1234" {
		t.Errorf("second result: got %q, want %q", results[1].CommitHash, "abc1234")
	}
	if results[0].Message != "feat: second commit" {
		t.Errorf("message: got %q, want %q", results[0].Message, "feat: second commit")
	}

	// Unknown session returns empty slice (not error).
	results2, err := db.GetCommitsBySession(database, "sess-unknown")
	if err != nil {
		t.Fatalf("GetCommitsBySession (unknown): %v", err)
	}
	if len(results2) != 0 {
		t.Errorf("expected empty slice for unknown session, got %d commits", len(results2))
	}
}

func TestTraceCommit(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	// Insert a feature with a track.
	database.Exec(`INSERT INTO tracks (id, title, status, created_at, updated_at) VALUES ('trk-trace', 'Trace Track', 'todo', datetime('now'), datetime('now'))`)
	database.Exec(`INSERT INTO features (id, type, title, status, priority, track_id, created_at, updated_at) VALUES ('feat-trace', 'feature', 'Trace Feature', 'done', 'medium', 'trk-trace', datetime('now'), datetime('now'))`)

	// Insert a commit linked to that feature.
	commit := models.GitCommit{
		CommitHash: "abc1234567890",
		SessionID:  "sess-test",
		FeatureID:  "feat-trace",
		Message:    "feat: traced commit",
		Timestamp:  time.Now().UTC(),
	}
	if err := db.InsertGitCommit(database, &commit); err != nil {
		t.Fatalf("InsertGitCommit: %v", err)
	}

	// Full SHA match.
	results, err := db.TraceCommit(database, "abc1234567890")
	if err != nil {
		t.Fatalf("TraceCommit full: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].FeatureID != "feat-trace" {
		t.Errorf("feature_id: got %q, want feat-trace", results[0].FeatureID)
	}
	if results[0].TrackID != "trk-trace" {
		t.Errorf("track_id: got %q, want trk-trace", results[0].TrackID)
	}
	if results[0].SessionID != "sess-test" {
		t.Errorf("session_id: got %q, want sess-test", results[0].SessionID)
	}

	// Prefix match.
	results2, err := db.TraceCommit(database, "abc1234")
	if err != nil {
		t.Fatalf("TraceCommit prefix: %v", err)
	}
	if len(results2) != 1 {
		t.Errorf("prefix match: expected 1 result, got %d", len(results2))
	}

	// No match.
	results3, err := db.TraceCommit(database, "zzz0000")
	if err != nil {
		t.Fatalf("TraceCommit no match: %v", err)
	}
	if len(results3) != 0 {
		t.Errorf("expected 0 results for unknown SHA, got %d", len(results3))
	}
}
