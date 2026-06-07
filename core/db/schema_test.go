package db_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

func TestSchemaCreationAndCRUD(t *testing.T) {
	// Use an in-memory database for testing.
	dbPath := "file::memory:?cache=shared"
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Verify tables exist by querying sqlite_master.
	rows, err := database.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()

	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables[name] = true
	}

	expected := []string{
		"agent_events", "features", "sessions", "tracks",
		"claims", "graph_edges",
	}
	for _, tbl := range expected {
		if !tables[tbl] {
			t.Errorf("missing table: %s (got %v)", tbl, tables)
		}
	}

	// Test Session CRUD.
	now := time.Now().UTC()
	sess := &models.Session{
		SessionID:     "test-sess-001",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
		Model:         "opus-4.6",
	}
	if err := db.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	got, err := db.GetSession(database, "test-sess-001")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.AgentAssigned != "claude-code" {
		t.Errorf("agent: got %q, want %q", got.AgentAssigned, "claude-code")
	}
	if got.Model != "opus-4.6" {
		t.Errorf("model: got %q, want %q", got.Model, "opus-4.6")
	}

	// Insert track first (FK requirement for features).
	_, err = database.Exec(
		`INSERT INTO tracks (id, title, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"trk-001", "Test Track", "todo", now.Format("2006-01-02T15:04:05Z07:00"), now.Format("2006-01-02T15:04:05Z07:00"),
	)
	if err != nil {
		t.Fatalf("Insert track: %v", err)
	}

	// Test Feature CRUD.
	feat := &db.Feature{
		ID:        "feat-test-001",
		Type:      "feature",
		Title:     "Test Feature",
		Status:    "todo",
		Priority:  "high",
		TrackID:   "trk-001",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertFeature(database, feat); err != nil {
		t.Fatalf("InsertFeature: %v", err)
	}
	gotFeat, err := db.GetFeature(database, "feat-test-001")
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if gotFeat.Title != "Test Feature" {
		t.Errorf("title: got %q, want %q", gotFeat.Title, "Test Feature")
	}
	if gotFeat.Priority != "high" {
		t.Errorf("priority: got %q, want %q", gotFeat.Priority, "high")
	}

	// Test status update.
	if err := db.UpdateFeatureStatus(database, "feat-test-001", "in-progress"); err != nil {
		t.Fatalf("UpdateFeatureStatus: %v", err)
	}
	gotFeat2, _ := db.GetFeature(database, "feat-test-001")
	if gotFeat2.Status != "in-progress" {
		t.Errorf("status after update: got %q, want %q", gotFeat2.Status, "in-progress")
	}

	// Test Event CRUD.
	evt := &models.AgentEvent{
		EventID:   "evt-test-001",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: "test-sess-001",
		Status:    "recorded",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, evt); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	gotEvt, err := db.GetEvent(database, "evt-test-001")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if gotEvt.ToolName != "Bash" {
		t.Errorf("tool_name: got %q, want %q", gotEvt.ToolName, "Bash")
	}
	if gotEvt.AgentID != "claude-code" {
		t.Errorf("agent_id: got %q, want %q", gotEvt.AgentID, "claude-code")
	}

	// Test ListEventsBySession.
	events, err := db.ListEventsBySession(database, "test-sess-001", 10)
	if err != nil {
		t.Fatalf("ListEventsBySession: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("events count: got %d, want 1", len(events))
	}

	// Test ListFeaturesByStatus.
	features, err := db.ListFeaturesByStatus(database, "in-progress", 10)
	if err != nil {
		t.Fatalf("ListFeaturesByStatus: %v", err)
	}
	if len(features) != 1 {
		t.Errorf("features count: got %d, want 1", len(features))
	}
}

func TestPragmasApplied(t *testing.T) {
	database, err := db.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Verify WAL mode is set.
	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	// In-memory databases may report "memory" instead of "wal".
	if journalMode != "wal" && journalMode != "memory" {
		t.Errorf("journal_mode: got %q, want wal or memory", journalMode)
	}

	// Verify foreign_keys is on.
	var fk int
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys: got %d, want 1", fk)
	}
}

func TestIntegrityCheck(t *testing.T) {
	database, err := db.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	ok, err := db.CheckIntegrity(database)
	if err != nil {
		t.Fatalf("CheckIntegrity: %v", err)
	}
	if !ok {
		t.Error("integrity check failed on fresh database")
	}
}

func TestTotalEventsIncrementTrigger(t *testing.T) {
	database, err := db.Open("file::memory:?cache=shared&mode=memory")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	sess := &models.Session{
		SessionID:     "sess-trigger-001",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
	}
	if err := db.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Verify total_events starts at 0.
	got, err := db.GetSession(database, "sess-trigger-001")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.TotalEvents != 0 {
		t.Errorf("initial total_events: got %d, want 0", got.TotalEvents)
	}

	// Insert 3 events — trigger should increment total_events each time.
	for i := 0; i < 3; i++ {
		e := &models.AgentEvent{
			EventID:   fmt.Sprintf("evt-trigger-%d", i),
			AgentID:   "claude-code",
			EventType: models.EventToolCall,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			ToolName:  "Bash",
			SessionID: "sess-trigger-001",
			Status:    "recorded",
			Source:    "hook",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := db.InsertEvent(database, e); err != nil {
			t.Fatalf("InsertEvent[%d]: %v", i, err)
		}
	}

	got, err = db.GetSession(database, "sess-trigger-001")
	if err != nil {
		t.Fatalf("GetSession after events: %v", err)
	}
	if got.TotalEvents != 3 {
		t.Errorf("total_events after 3 inserts: got %d, want 3", got.TotalEvents)
	}
}

func TestGetSessionProjectDir(t *testing.T) {
	database, err := db.Open("file::memory:?cache=shared&mode=memory")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	sess := &models.Session{
		SessionID:     "sess-proj-dir-getter-001",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
		ProjectDir:    "/home/user/myproject",
	}
	if err := db.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	got := db.GetSessionProjectDir(database, sess.SessionID)
	if got != sess.ProjectDir {
		t.Errorf("GetSessionProjectDir: got %q, want %q", got, sess.ProjectDir)
	}

	// Non-existent session returns empty string.
	empty := db.GetSessionProjectDir(database, "does-not-exist")
	if empty != "" {
		t.Errorf("GetSessionProjectDir non-existent: got %q, want empty", empty)
	}
}
