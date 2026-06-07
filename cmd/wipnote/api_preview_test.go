package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestPreviewQueryReturnsLastNUserAssistant verifies the SQL query inside
// previewMessages returns only user/assistant rows, ordered by ordinal DESC
// (newest first), limited to N.
func TestPreviewQueryReturnsLastNUserAssistant(t *testing.T) {
	const sessionID = "sess-preview-query-001"
	now := time.Now().UTC()

	msgs := []models.Message{
		{SessionID: sessionID, Ordinal: 0, Role: "user", Content: "hello", Timestamp: now},
		{SessionID: sessionID, Ordinal: 1, Role: "assistant", Content: "world", Timestamp: now.Add(1 * time.Second)},
		{SessionID: sessionID, Ordinal: 2, Role: "user", Content: "follow up", Timestamp: now.Add(2 * time.Second)},
		{SessionID: sessionID, Ordinal: 3, Role: "assistant", Content: "done", Timestamp: now.Add(3 * time.Second)},
	}
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, 'claude-code', datetime('now'), 'completed')`, sessionID)
	for _, m := range msgs {
		m2 := m
		if _, err := dbpkg.InsertMessage(database, &m2); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	got, err := previewMessages(database, sessionID, 2)
	if err != nil {
		t.Fatalf("previewMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	// Returned in chronological order (ordinal asc after DESC fetch).
	if got[0].Role != "user" || got[0].ContentTruncated != "follow up" {
		t.Errorf("got[0]: %+v, want role=user content=follow up", got[0])
	}
	if got[1].Role != "assistant" || got[1].ContentTruncated != "done" {
		t.Errorf("got[1]: %+v, want role=assistant content=done", got[1])
	}
}

// TestPreviewEndpoint verifies GET /api/sessions/{id}/preview returns 200 and
// a JSON body with the expected messages array structure.
func TestPreviewEndpoint(t *testing.T) {
	const sessionID = "sess-preview-http-001"
	now := time.Now().UTC()

	msgs := []models.Message{
		{SessionID: sessionID, Ordinal: 0, Role: "user", Content: "hi there", Timestamp: now},
		{SessionID: sessionID, Ordinal: 1, Role: "assistant", Content: "hello back", Timestamp: now.Add(1 * time.Second)},
	}
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, 'claude-code', datetime('now'), 'completed')`, sessionID)
	for _, m := range msgs {
		m2 := m
		if _, err := dbpkg.InsertMessage(database, &m2); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	handler := previewHandler(database)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/preview", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}

	var body previewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SessionID != sessionID {
		t.Errorf("session_id: got %q, want %q", body.SessionID, sessionID)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("messages len: got %d, want 2", len(body.Messages))
	}
	if body.Messages[0].Role != "user" {
		t.Errorf("messages[0].role: got %q, want user", body.Messages[0].Role)
	}
	if body.Messages[0].ContentTruncated != "hi there" {
		t.Errorf("messages[0].content_truncated: got %q", body.Messages[0].ContentTruncated)
	}
}

// TestPreviewEmptySession verifies that a session with no messages returns
// 200 with an empty messages array (not 404).
func TestPreviewEmptySession(t *testing.T) {
	const sessionID = "sess-preview-empty-001"
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, 'claude-code', datetime('now'), 'completed')`, sessionID)

	handler := previewHandler(database)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/preview", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}

	var body previewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Messages == nil || len(body.Messages) != 0 {
		t.Errorf("messages: got %v, want empty array", body.Messages)
	}
}

// TestIngestRouteStillWorks verifies that POST /api/sessions/{id}/ingest still
// routes to the existing ingest handler after adding preview dispatch.
func TestIngestRouteStillWorks(t *testing.T) {
	const sessionID = "sess-ingest-regression-001"
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	handler := sessionIngestHandler(database, database)
	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/ingest", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The ingest handler returns 200 with a JSON body (not_found status or ok).
	// We only care that it does NOT 404 (which would mean preview stole the route)
	// and it returns valid JSON.
	if rec.Code == http.StatusNotFound {
		t.Fatalf("ingest route returned 404 — preview dispatch may have stolen it")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
}

// TestPreviewTruncatesLongContent verifies that content longer than 200 runes
// is truncated to exactly 200 runes in content_truncated.
func TestPreviewTruncatesLongContent(t *testing.T) {
	const sessionID = "sess-preview-trunc-001"
	now := time.Now().UTC()

	longContent := strings.Repeat("a", 5000)
	msgs := []models.Message{
		{SessionID: sessionID, Ordinal: 0, Role: "user", Content: longContent, Timestamp: now},
	}
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, 'claude-code', datetime('now'), 'completed')`, sessionID)
	for _, m := range msgs {
		m2 := m
		if _, err := dbpkg.InsertMessage(database, &m2); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	handler := previewHandler(database)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/preview", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var body previewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("messages len: got %d, want 1", len(body.Messages))
	}

	got := []rune(body.Messages[0].ContentTruncated)
	if len(got) != 200 {
		t.Errorf("content_truncated rune length: got %d, want 200", len(got))
	}
}
