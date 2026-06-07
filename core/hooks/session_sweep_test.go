package hooks

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// setupSweepEnv creates a temp project directory with a pre-populated
// session HTML file and an in-memory db with a single started agent_event
// that is old enough to be considered an orphan.
func setupSweepEnv(t *testing.T, sessionID string, eventID string, age time.Duration) (string, *sql.DB) {
	t.Helper()

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote", "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	s := &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
	}
	CreateSessionHTML(projectDir, s)

	database, err := db.Open("file::memory:?cache=shared&_uniq=" + t.Name())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := db.InsertSession(database, s); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	created := time.Now().UTC().Add(-age)
	ev := &models.AgentEvent{
		EventID:   eventID,
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: created,
		ToolName:  "Bash",
		SessionID: sessionID,
		Status:    "started",
		Source:    "hook",
		CreatedAt: created,
		UpdatedAt: created,
	}
	if err := db.UpsertEvent(database, ev); err != nil {
		t.Fatalf("UpsertEvent: %v", err)
	}

	return projectDir, database
}

func TestSweepOrphanedEventsForSession_AppendsAbortedLi(t *testing.T) {
	projectDir, database := setupSweepEnv(t, "sess-sweep-001", "evt-orphan-1", 10*time.Minute)

	appended := SweepOrphanedEventsForSession(database, projectDir, "sess-sweep-001")
	if appended != 1 {
		t.Errorf("appended: got %d, want 1", appended)
	}

	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "sessions", "sess-sweep-001.html"))
	if err != nil {
		t.Fatalf("read session html: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `data-event-id="evt-orphan-1"`) {
		t.Error("synthetic entry missing event id")
	}
	if !strings.Contains(content, `data-status="aborted"`) {
		t.Error("synthetic entry missing data-status=aborted")
	}
	if !strings.Contains(content, `data-reason="no-post-hook"`) {
		t.Error("synthetic entry missing data-reason=no-post-hook")
	}

	// SQLite row transitioned.
	evt, err := db.GetEvent(database, "evt-orphan-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if evt.Status != "aborted" {
		t.Errorf("status: got %q, want aborted", evt.Status)
	}
}

func TestSweepOrphanedEventsForSession_Idempotent(t *testing.T) {
	projectDir, database := setupSweepEnv(t, "sess-sweep-idem-001", "evt-orphan-idem-1", 10*time.Minute)

	first := SweepOrphanedEventsForSession(database, projectDir, "sess-sweep-idem-001")
	second := SweepOrphanedEventsForSession(database, projectDir, "sess-sweep-idem-001")
	if first != 1 {
		t.Errorf("first sweep: got %d, want 1", first)
	}
	if second != 0 {
		t.Errorf("second sweep should be no-op, got %d", second)
	}

	data, _ := os.ReadFile(filepath.Join(projectDir, ".wipnote", "sessions", "sess-sweep-idem-001.html"))
	if strings.Count(string(data), `data-event-id="evt-orphan-idem-1"`) != 1 {
		t.Errorf("expected exactly 1 synthetic entry after double sweep")
	}
}

func TestSweepOrphanedEventsForSession_IgnoresRecent(t *testing.T) {
	projectDir, database := setupSweepEnv(t, "sess-sweep-recent-001", "evt-recent-1", 30*time.Second)

	appended := SweepOrphanedEventsForSession(database, projectDir, "sess-sweep-recent-001")
	if appended != 0 {
		t.Errorf("appended: got %d, want 0 (too recent to sweep)", appended)
	}
}
