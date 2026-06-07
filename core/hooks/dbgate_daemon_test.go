package hooks

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

func newTestEvent(id string) *models.AgentEvent {
	now := time.Now().UTC().Truncate(time.Second)
	return &models.AgentEvent{
		EventID:   id,
		AgentID:   "agent-1",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: "sess-xyz",
		Status:    "completed",
		Source:    "claude",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// TestSubmitDerivedEvent_FallbackWritesViaDirectOpen verifies that when no
// writer daemon is reachable (projectRoot has no socket), SubmitDerivedEvent
// still writes the row via the direct-open fallback and surfaces no error.
// This is today's behaviour preserved (MVP-3 is purely additive when the
// daemon is down).
func TestSubmitDerivedEvent_FallbackWritesViaDirectOpen(t *testing.T) {
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1") // deterministic: no spawn, straight to fallback
	projectRoot := t.TempDir()              // no .wipnote/writer.sock here
	database, err := db.Open(filepath.Join(projectRoot, "fallback.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := db.InsertSession(database, &models.Session{
		SessionID: "sess-xyz", AgentAssigned: "agent-1", CreatedAt: now, Status: "active",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	ev := newTestEvent("evt-fallback")

	// Daemon is unavailable and spawn will fail (no real serve-child wired in
	// a unit test); SubmitDerivedEvent must fall back to direct-open and write.
	start := time.Now()
	SubmitDerivedEvent("posttooluse", projectRoot, "sess-xyz", 1, ev, database)
	elapsed := time.Since(start)

	// Must not hang: the whole attempt (daemon budget + fallback) is bounded.
	if elapsed > daemonSubmitBudget+spawnSlack() {
		t.Fatalf("SubmitDerivedEvent took %v, exceeds bounded budget (must not hang)", elapsed)
	}

	var count int
	if err := database.QueryRow(
		`SELECT COUNT(1) FROM agent_events WHERE event_id = ?`, "evt-fallback",
	).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("fallback did not write the row: count=%d want 1", count)
	}
}

// TestSubmitDerivedEvent_NilDBNoPanic verifies the no-daemon + nil-db path
// records a fallback and returns cleanly (canonical NDJSON is authoritative).
func TestSubmitDerivedEvent_NilDBNoPanic(t *testing.T) {
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1")
	projectRoot := t.TempDir()
	// Should not panic; nothing to assert beyond a clean return.
	SubmitDerivedEvent("posttooluse", projectRoot, "sess-xyz", 2, newTestEvent("evt-nildb"), nil)
}

// spawnSlack is a generous ceiling above the daemon budget to absorb the
// fallback's own bounded BUSY-retry without flaking on slow CI.
func spawnSlack() time.Duration { return 2 * time.Second }
