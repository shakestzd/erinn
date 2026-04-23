package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/ingest"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// setupMigrateEnv creates a temp project with a db file at the expected
// location and seeds the given orphan session_ids (rows in sessions but
// no HTML file, plus one tool call per session so the renderer has data).
func setupMigrateEnv(t *testing.T, orphanIDs ...string) string {
	t.Helper()
	projectDir := t.TempDir()
	htmlgraphDir := filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(filepath.Join(htmlgraphDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	database, err := dbpkg.Open(filepath.Join(htmlgraphDir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	for _, id := range orphanIDs {
		sess := &models.Session{
			SessionID:     id,
			AgentAssigned: "claude-code",
			CreatedAt:     now,
			Status:        "completed",
		}
		if err := dbpkg.InsertSession(database, sess); err != nil {
			t.Fatalf("InsertSession %s: %v", id, err)
		}
		// Seed a message so the tool_call FK constraint is satisfied.
		msg := &models.Message{
			SessionID: id,
			Ordinal:   0,
			Role:      "assistant",
			Content:   "test",
			Timestamp: now,
		}
		msgID, err := dbpkg.InsertMessage(database, msg)
		if err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
		tc := &models.ToolCall{
			MessageID:      int(msgID),
			SessionID:      id,
			ToolName:       "Read",
			ToolUseID:      "tu-" + id,
			InputJSON:      `{"file_path":"/mock/a.go"}`,
			Category:       "Read",
			MessageOrdinal: 0,
		}
		if err := dbpkg.InsertToolCall(database, tc); err != nil {
			t.Fatalf("InsertToolCall: %v", err)
		}
	}
	return htmlgraphDir
}

func TestExistingSessionHTMLSet(t *testing.T) {
	tmpdir := t.TempDir()
	sessDir := filepath.Join(tmpdir, ".htmlgraph", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, id := range []string{"sess-a", "sess-b"} {
		if err := os.WriteFile(filepath.Join(sessDir, id+".html"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	set, err := existingSessionHTMLSet(filepath.Join(tmpdir, ".htmlgraph"))
	if err != nil {
		t.Fatalf("existingSessionHTMLSet: %v", err)
	}
	if _, ok := set["sess-a"]; !ok {
		t.Error("expected sess-a in set")
	}
	if _, ok := set["sess-b"]; !ok {
		t.Error("expected sess-b in set")
	}
	if _, ok := set["sess-missing"]; ok {
		t.Error("sess-missing should not be in set")
	}
}

func TestBuildParseResultFromSQLite(t *testing.T) {
	htmlgraphDir := setupMigrateEnv(t, "sess-build-001")

	database, err := dbpkg.Open(filepath.Join(htmlgraphDir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	result, err := buildParseResultFromSQLite(database, "sess-build-001")
	if err != nil {
		t.Fatalf("buildParseResultFromSQLite: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Errorf("ToolCalls: got %d, want 1", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ToolName != "Read" {
		t.Errorf("ToolName: got %q, want Read", result.ToolCalls[0].ToolName)
	}
}

func TestMigrateOneSession_RendersFromSQLite(t *testing.T) {
	htmlgraphDir := setupMigrateEnv(t, "sess-migrate-001")

	database, err := dbpkg.Open(filepath.Join(htmlgraphDir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	emptyIdx := map[string]ingest.SessionFile{}
	if err := migrateOneSession(database, htmlgraphDir, "sess-migrate-001", "sqlite", emptyIdx); err != nil {
		t.Fatalf("migrateOneSession: %v", err)
	}

	htmlPath := filepath.Join(htmlgraphDir, "sessions", "sess-migrate-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read rendered: %v", err)
	}
	if !strings.Contains(string(data), `id="sess-migrate-001"`) {
		t.Error("rendered HTML missing article id")
	}
	if !strings.Contains(string(data), `data-tool="Read"`) {
		t.Error("rendered HTML missing tool entry")
	}
}

func TestMigrateSessions_Idempotent(t *testing.T) {
	htmlgraphDir := setupMigrateEnv(t, "sess-idem-001")

	database, err := dbpkg.Open(filepath.Join(htmlgraphDir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	emptyIdx := map[string]ingest.SessionFile{}
	if err := migrateOneSession(database, htmlgraphDir, "sess-idem-001", "sqlite", emptyIdx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(htmlgraphDir, "sessions", "sess-idem-001.html"))
	if err != nil {
		t.Fatalf("read first: %v", err)
	}

	// Second migrate is a no-op because the skip-if-exists guard in
	// RenderIngestedSessionHTML short-circuits.
	if err := migrateOneSession(database, htmlgraphDir, "sess-idem-001", "sqlite", emptyIdx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(htmlgraphDir, "sessions", "sess-idem-001.html"))
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(first) != string(second) {
		t.Error("second migrate must be a no-op when HTML already exists")
	}
}

func TestSelectOrphanSessions(t *testing.T) {
	htmlgraphDir := setupMigrateEnv(t, "sess-orphan-1", "sess-orphan-2", "sess-already-has-html")

	// Mark one session as already having HTML.
	if err := os.WriteFile(filepath.Join(htmlgraphDir, "sessions", "sess-already-has-html.html"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	database, err := dbpkg.Open(filepath.Join(htmlgraphDir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	present, err := existingSessionHTMLSet(htmlgraphDir)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	orphans, err := selectOrphanSessions(database, present)
	if err != nil {
		t.Fatalf("selectOrphanSessions: %v", err)
	}

	got := map[string]bool{}
	for _, id := range orphans {
		got[id] = true
	}
	if !got["sess-orphan-1"] || !got["sess-orphan-2"] {
		t.Errorf("expected both orphans in result, got %v", orphans)
	}
	if got["sess-already-has-html"] {
		t.Errorf("sess-already-has-html should not be in orphans")
	}
}
