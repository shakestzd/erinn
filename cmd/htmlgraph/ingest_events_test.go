package main

import (
	"database/sql"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/ingest"
	"github.com/shakestzd/htmlgraph/internal/models"
)

func setupIngestEventsDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestStoreParseResult_CreatesAgentEvents(t *testing.T) {
	database := setupIngestEventsDB(t)

	sessionID := "sess-ingest-evt-001"
	database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, 'claude-code', datetime('now'), 'completed')`, sessionID)

	result := &ingest.ParseResult{
		Messages: []models.Message{
			{Ordinal: 0, Role: "human", Timestamp: time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)},
			{Ordinal: 1, Role: "assistant", Timestamp: time.Date(2026, 4, 8, 12, 0, 5, 0, time.UTC)},
		},
		ToolCalls: []models.ToolCall{
			{
				MessageOrdinal: 1,
				ToolName:       "Read",
				ToolUseID:      "tu-abc123",
				InputJSON:      `{"file_path":"/mock/test.go"}`,
			},
			{
				MessageOrdinal: 1,
				ToolName:       "Edit",
				ToolUseID:      "tu-def456",
				InputJSON:      `{"file_path":"/mock/test.go","old_string":"foo","new_string":"bar"}`,
			},
		},
	}

	msgCount, toolCount := storeParseResult(database, sessionID, "", result)
	if msgCount != 2 {
		t.Errorf("msgCount: got %d, want 2", msgCount)
	}
	if toolCount != 2 {
		t.Errorf("toolCount: got %d, want 2", toolCount)
	}

	// Verify agent_events were created.
	evtID1 := ingest.EventID(sessionID, "tu-abc123", "Read", 0)
	evt1, err := dbpkg.GetEvent(database, evtID1)
	if err != nil {
		t.Fatalf("GetEvent for Read: %v", err)
	}
	if evt1.ToolName != "Read" {
		t.Errorf("ToolName: got %q, want %q", evt1.ToolName, "Read")
	}
	if evt1.Source != "ingest" {
		t.Errorf("Source: got %q, want %q", evt1.Source, "ingest")
	}
	if evt1.Status != "completed" {
		t.Errorf("Status: got %q, want %q", evt1.Status, "completed")
	}
	if evt1.AgentID != "claude-code" {
		t.Errorf("AgentID: got %q, want %q", evt1.AgentID, "claude-code")
	}
	if evt1.EventType != models.EventToolCall {
		t.Errorf("EventType: got %q, want %q", evt1.EventType, models.EventToolCall)
	}
	if evt1.SessionID != sessionID {
		t.Errorf("SessionID: got %q, want %q", evt1.SessionID, sessionID)
	}

	evtID2 := ingest.EventID(sessionID, "tu-def456", "Edit", 1)
	evt2, err := dbpkg.GetEvent(database, evtID2)
	if err != nil {
		t.Fatalf("GetEvent for Edit: %v", err)
	}
	if evt2.ToolName != "Edit" {
		t.Errorf("ToolName: got %q, want %q", evt2.ToolName, "Edit")
	}
}

func TestStoreParseResult_EventTimestampFromMessage(t *testing.T) {
	database := setupIngestEventsDB(t)

	sessionID := "sess-ingest-ts-001"
	database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, 'claude-code', datetime('now'), 'completed')`, sessionID)

	msgTime := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	result := &ingest.ParseResult{
		Messages: []models.Message{
			{Ordinal: 0, Role: "assistant", Timestamp: msgTime},
		},
		ToolCalls: []models.ToolCall{
			{
				MessageOrdinal: 0,
				ToolName:       "Bash",
				ToolUseID:      "tu-ts-001",
				InputJSON:      `{"command":"ls"}`,
			},
		},
	}

	storeParseResult(database, sessionID, "", result)

	evtID := ingest.EventID(sessionID, "tu-ts-001", "Bash", 0)
	evt, err := dbpkg.GetEvent(database, evtID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if !evt.Timestamp.Equal(msgTime) {
		t.Errorf("Timestamp: got %v, want %v", evt.Timestamp, msgTime)
	}
}

func TestStoreParseResult_IdempotentReingestion(t *testing.T) {
	database := setupIngestEventsDB(t)

	sessionID := "sess-ingest-idem-001"
	database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, 'claude-code', datetime('now'), 'completed')`, sessionID)

	result := &ingest.ParseResult{
		Messages: []models.Message{
			{Ordinal: 0, Role: "assistant", Timestamp: time.Now().UTC()},
		},
		ToolCalls: []models.ToolCall{
			{
				MessageOrdinal: 0,
				ToolName:       "Read",
				ToolUseID:      "tu-idem-001",
				InputJSON:      `{"file_path":"/mock/test.go"}`,
			},
		},
	}

	// First ingestion.
	storeParseResult(database, sessionID, "", result)

	// Second ingestion — should not error (UpsertEvent uses INSERT OR REPLACE).
	storeParseResult(database, sessionID, "", result)

	// Should still have exactly one event with this ID.
	evtID := ingest.EventID(sessionID, "tu-idem-001", "Read", 0)
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM agent_events WHERE event_id = ?`, evtID).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 event after re-ingestion, got %d", count)
	}
}

func TestIngestEventID_Deterministic(t *testing.T) {
	id1 := ingest.EventID("sess-001", "tu-abc", "Read", 0)
	id2 := ingest.EventID("sess-001", "tu-abc", "Read", 0)
	if id1 != id2 {
		t.Errorf("same inputs should produce same ID: %q != %q", id1, id2)
	}

	id3 := ingest.EventID("sess-001", "tu-def", "Read", 0)
	if id1 == id3 {
		t.Errorf("different toolUseID should produce different ID")
	}
}

func TestIngestEventID_FallbackWithoutToolUseID(t *testing.T) {
	id1 := ingest.EventID("sess-001", "", "Read", 0)
	id2 := ingest.EventID("sess-001", "", "Read", 1)
	if id1 == id2 {
		t.Errorf("different indices without toolUseID should produce different IDs")
	}
}

func TestStoreParseResult_InputSummaryTruncated(t *testing.T) {
	database := setupIngestEventsDB(t)

	sessionID := "sess-ingest-trunc-001"
	database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, 'claude-code', datetime('now'), 'completed')`, sessionID)

	longJSON := `{"command":"` + string(make([]byte, 300)) + `"}`
	result := &ingest.ParseResult{
		Messages: []models.Message{
			{Ordinal: 0, Role: "assistant", Timestamp: time.Now().UTC()},
		},
		ToolCalls: []models.ToolCall{
			{
				MessageOrdinal: 0,
				ToolName:       "Bash",
				ToolUseID:      "tu-trunc-001",
				InputJSON:      longJSON,
			},
		},
	}

	storeParseResult(database, sessionID, "", result)

	evtID := ingest.EventID(sessionID, "tu-trunc-001", "Bash", 0)
	evt, err := dbpkg.GetEvent(database, evtID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if len([]rune(evt.InputSummary)) > 200 {
		t.Errorf("InputSummary length %d > 200", len([]rune(evt.InputSummary)))
	}
}
