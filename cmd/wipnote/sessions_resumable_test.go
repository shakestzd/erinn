package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

func TestListResumableSessions_RanksLatestPerWorkItemAndUsesHeartbeatLiveness(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "resumable.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	insertResumableSession(t, database, "sess-old", "claude-code", "active", now.Add(-2*time.Hour))
	insertResumableSession(t, database, "sess-new", "codex", "active", now.Add(-time.Hour))
	insertResumableSession(t, database, "sess-stale", "gemini", "active", now.Add(-30*time.Minute))

	if _, err := database.Exec(`INSERT INTO features (id, type, title, status) VALUES
		('feat-a', 'feature', 'Alpha', 'in-progress'),
		('feat-b', 'bug', 'Bravo', 'in-progress'),
		('feat-done', 'feature', 'Done', 'done')`); err != nil {
		t.Fatalf("insert features: %v", err)
	}
	if err := dbpkg.SetActiveWorkItem(database, "sess-old", dbpkg.AgentRootSentinel, "feat-a"); err != nil {
		t.Fatalf("SetActiveWorkItem old: %v", err)
	}
	if _, err := database.Exec(`UPDATE sessions SET active_feature_id = ? WHERE session_id = ?`, "feat-a", "sess-new"); err != nil {
		t.Fatalf("legacy active_feature_id: %v", err)
	}
	if _, err := database.Exec(`UPDATE sessions SET active_feature_id = ? WHERE session_id = ?`, "feat-b", "sess-stale"); err != nil {
		t.Fatalf("feature b link: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO sessions (session_id, agent_assigned, created_at, status, active_feature_id)
		VALUES (?, ?, ?, ?, ?)`, "sess-done", "claude-code", now.Format(time.RFC3339), "completed", "feat-done"); err != nil {
		t.Fatalf("insert done session: %v", err)
	}

	fresh := now.Add(-30 * time.Second).Format(time.RFC3339)
	stale := now.Add(-10 * time.Minute).Format(time.RFC3339)
	if _, err := database.Exec(`INSERT INTO claims
		(claim_id, work_item_id, owner_session_id, owner_agent, status, lease_expires_at, last_heartbeat_at)
		VALUES
		('claim-new', 'feat-a', 'sess-new', 'codex', 'in_progress', ?, ?),
		('claim-stale', 'feat-b', 'sess-stale', 'gemini', 'in_progress', ?, ?)`,
		now.Add(time.Hour).Format(time.RFC3339), fresh,
		now.Add(time.Hour).Format(time.RFC3339), stale,
	); err != nil {
		t.Fatalf("insert claims: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, session_id, status)
		VALUES
		('evt-old', 'claude-code', 'tool_call', ?, 'sess-old', 'completed'),
		('evt-new', 'codex', 'tool_call', ?, 'sess-new', 'completed'),
		('evt-stale', 'gemini', 'tool_call', ?, 'sess-stale', 'completed')`,
		now.Add(-90*time.Minute).Format(time.RFC3339),
		now.Add(-45*time.Minute).Format(time.RFC3339),
		now.Add(-20*time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert agent_events: %v", err)
	}

	rows, err := dbpkg.ListResumableSessions(database, 2*time.Minute)
	if err != nil {
		t.Fatalf("ListResumableSessions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].WorkItemID != "feat-a" {
		t.Fatalf("rows[0].work_item_id = %q, want feat-a", rows[0].WorkItemID)
	}
	if rows[0].LastSessionID != "sess-new" {
		t.Errorf("feat-a last_session_id = %q, want sess-new", rows[0].LastSessionID)
	}
	if !rows[0].Live {
		t.Error("feat-a live = false, want true from fresh heartbeat")
	}
	if rows[0].Harness != "codex" {
		t.Errorf("feat-a harness = %q, want codex", rows[0].Harness)
	}
	if rows[1].WorkItemID != "feat-b" {
		t.Errorf("rows[1].work_item_id = %q, want feat-b", rows[1].WorkItemID)
	}
	if rows[1].Live {
		t.Error("feat-b live = true, want false for stale heartbeat")
	}
}

func TestResumableSessionsHandler_JSONContract(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "resumable_api.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	insertResumableSession(t, database, "sess-api", "claude-code", "active", now.Add(-time.Minute))
	if _, err := database.Exec(`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', 'API Item', 'in-progress')`, "feat-api"); err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	if _, err := database.Exec(`UPDATE sessions SET active_feature_id = ?, exec_worktree_path = ?, branch = ?, harness = ? WHERE session_id = ?`,
		"feat-api", ".claude/worktrees/api", "feat-api", "claude", "sess-api"); err != nil {
		t.Fatalf("update session: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO claims
		(claim_id, work_item_id, owner_session_id, owner_agent, status, lease_expires_at, last_heartbeat_at)
		VALUES (?, ?, ?, ?, 'in_progress', ?, ?)`,
		"claim-api", "feat-api", "sess-api", "claude",
		now.Add(time.Hour).Format(time.RFC3339), now.Add(-time.Second).Format(time.RFC3339)); err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/resumable", nil)
	w := httptest.NewRecorder()
	resumableSessionsHandler(database, "/repo/.wipnote")(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}

	var resp struct {
		Sessions []dbpkg.ResumableSession `json:"sessions"`
		Count    int                      `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 || len(resp.Sessions) != 1 {
		t.Fatalf("count=%d len=%d, want 1", resp.Count, len(resp.Sessions))
	}
	got := resp.Sessions[0]
	if got.WorkItemID != "feat-api" || got.Branch != "feat-api" || got.ExecWorktreePath != ".claude/worktrees/api" {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestRenderResumableSessionsTable(t *testing.T) {
	var buf bytes.Buffer
	renderResumableSessionsTable(&buf, []dbpkg.ResumableSession{{
		WorkItemID:       "feat-a",
		Type:             "feature",
		Harness:          "codex",
		Live:             true,
		LastActivity:     "2026-06-16T12:00:00Z",
		LastSessionID:    "sess-1234567890abcdef",
		Branch:           "feat-a",
		ExecWorktreePath: ".claude/worktrees/feat-a",
		Title:            "Alpha",
	}})
	out := buf.String()
	for _, want := range []string{"WORK ITEM", "feat-a", "codex", "yes", "Alpha"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table output missing %q:\n%s", want, out)
		}
	}
}

func insertResumableSession(t *testing.T, database *sql.DB, sid, agent, status string, createdAt time.Time) {
	t.Helper()
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     sid,
		AgentAssigned: agent,
		CreatedAt:     createdAt,
		Status:        status,
	}); err != nil {
		t.Fatalf("InsertSession %s: %v", sid, err)
	}
}
