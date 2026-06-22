package db_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// setupTestDB opens an in-memory database with schema and a test session.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Insert a session so FK constraints pass.
	now := time.Now().UTC()
	sess := &models.Session{
		SessionID:     "sess-test",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
	}
	if err := db.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	return database
}

func TestUpsertEvent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:   "evt-upsert-1",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}

	// First insert.
	if err := db.UpsertEvent(database, ev); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Verify it exists.
	got, err := db.GetEvent(database, "evt-upsert-1")
	if err != nil {
		t.Fatalf("GetEvent after first upsert: %v", err)
	}
	if got.Status != "started" {
		t.Errorf("status: got %q, want %q", got.Status, "started")
	}

	// Upsert with updated status (should replace).
	ev.Status = "completed"
	ev.OutputSummary = "done"
	if err := db.UpsertEvent(database, ev); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err = db.GetEvent(database, "evt-upsert-1")
	if err != nil {
		t.Fatalf("GetEvent after second upsert: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("status after upsert: got %q, want %q", got.Status, "completed")
	}
	if got.OutputSummary != "done" {
		t.Errorf("output_summary: got %q, want %q", got.OutputSummary, "done")
	}
}

func TestUpdateEventFields(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:   "evt-update-1",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Read",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	if err := db.UpdateEventFields(database, "evt-update-1", "completed", "read main.go"); err != nil {
		t.Fatalf("UpdateEventFields: %v", err)
	}

	got, err := db.GetEvent(database, "evt-update-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("status: got %q, want %q", got.Status, "completed")
	}
	if got.OutputSummary != "read main.go" {
		t.Errorf("output_summary: got %q, want %q", got.OutputSummary, "read main.go")
	}
}

func TestUpdateEventStatus(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:   "evt-status-1",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Grep",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	if err := db.UpdateEventStatus(database, "evt-status-1", "failed"); err != nil {
		t.Fatalf("UpdateEventStatus: %v", err)
	}

	got, err := db.GetEvent(database, "evt-status-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status: got %q, want %q", got.Status, "failed")
	}
}

func TestFindStartedEvent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()

	// Insert a started Bash event.
	ev1 := &models.AgentEvent{
		EventID:   "evt-find-1",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, ev1); err != nil {
		t.Fatalf("InsertEvent ev1: %v", err)
	}

	// Insert a completed Bash event (should not be found).
	ev2 := &models.AgentEvent{
		EventID:   "evt-find-2",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now.Add(time.Second),
		ToolName:  "Bash",
		SessionID: "sess-test",
		Status:    "completed",
		Source:    "hook",
		CreatedAt: now.Add(time.Second),
		UpdatedAt: now.Add(time.Second),
	}
	if err := db.InsertEvent(database, ev2); err != nil {
		t.Fatalf("InsertEvent ev2: %v", err)
	}

	id, err := db.FindStartedEvent(database, "sess-test", "Bash")
	if err != nil {
		t.Fatalf("FindStartedEvent: %v", err)
	}
	if id != "evt-find-1" {
		t.Errorf("got %q, want %q", id, "evt-find-1")
	}

	// No started Read events -> ErrNoRows.
	_, err = db.FindStartedEvent(database, "sess-test", "Read")
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for Read, got %v", err)
	}
}

func TestFindStartedEventByAgent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()

	// A started Bash event from agent-aaa.
	ev1 := &models.AgentEvent{
		EventID:   "evt-fseba-1",
		AgentID:   "agent-aaa",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, ev1); err != nil {
		t.Fatalf("InsertEvent ev1: %v", err)
	}

	// A started Bash event from agent-bbb (different agent).
	ev2 := &models.AgentEvent{
		EventID:   "evt-fseba-2",
		AgentID:   "agent-bbb",
		EventType: models.EventToolCall,
		Timestamp: now.Add(time.Second),
		ToolName:  "Bash",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now.Add(time.Second),
		UpdatedAt: now.Add(time.Second),
	}
	if err := db.InsertEvent(database, ev2); err != nil {
		t.Fatalf("InsertEvent ev2: %v", err)
	}

	// A completed Bash event from agent-aaa (should not match).
	ev3 := &models.AgentEvent{
		EventID:   "evt-fseba-3",
		AgentID:   "agent-aaa",
		EventType: models.EventToolCall,
		Timestamp: now.Add(2 * time.Second),
		ToolName:  "Bash",
		SessionID: "sess-test",
		Status:    "completed",
		Source:    "hook",
		CreatedAt: now.Add(2 * time.Second),
		UpdatedAt: now.Add(2 * time.Second),
	}
	if err := db.InsertEvent(database, ev3); err != nil {
		t.Fatalf("InsertEvent ev3: %v", err)
	}

	// Should find agent-aaa's started event.
	id, err := db.FindStartedEventByAgent(database, "sess-test", "Bash", "agent-aaa")
	if err != nil {
		t.Fatalf("FindStartedEventByAgent agent-aaa: %v", err)
	}
	if id != "evt-fseba-1" {
		t.Errorf("agent-aaa: got %q, want %q", id, "evt-fseba-1")
	}

	// Should find agent-bbb's started event independently.
	id, err = db.FindStartedEventByAgent(database, "sess-test", "Bash", "agent-bbb")
	if err != nil {
		t.Fatalf("FindStartedEventByAgent agent-bbb: %v", err)
	}
	if id != "evt-fseba-2" {
		t.Errorf("agent-bbb: got %q, want %q", id, "evt-fseba-2")
	}

	// Unknown agent -> ErrNoRows.
	_, err = db.FindStartedEventByAgent(database, "sess-test", "Bash", "agent-unknown")
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for unknown agent, got %v", err)
	}

	// No started Read events for agent-aaa -> ErrNoRows.
	_, err = db.FindStartedEventByAgent(database, "sess-test", "Read", "agent-aaa")
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for Read/agent-aaa, got %v", err)
	}
}

func TestFindStartedDelegation(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:   "evt-deleg-1",
		AgentID:   "subagent-abc",
		EventType: models.EventTaskDelegation,
		Timestamp: now,
		ToolName:  "Task",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	id, err := db.FindStartedDelegation(database, "sess-test")
	if err != nil {
		t.Fatalf("FindStartedDelegation: %v", err)
	}
	if id != "evt-deleg-1" {
		t.Errorf("got %q, want %q", id, "evt-deleg-1")
	}
}

func TestFindStartedDelegationByAgent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()

	// A started delegation for agent-abc.
	ev1 := &models.AgentEvent{
		EventID:   "evt-sdba-1",
		AgentID:   "agent-abc",
		EventType: models.EventTaskDelegation,
		Timestamp: now,
		ToolName:  "Task",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, ev1); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	// A completed delegation for agent-abc (should not match).
	ev2 := &models.AgentEvent{
		EventID:   "evt-sdba-2",
		AgentID:   "agent-abc",
		EventType: models.EventTaskDelegation,
		Timestamp: now.Add(time.Second),
		ToolName:  "Task",
		SessionID: "sess-test",
		Status:    "completed",
		Source:    "hook",
		CreatedAt: now.Add(time.Second),
		UpdatedAt: now.Add(time.Second),
	}
	if err := db.InsertEvent(database, ev2); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	id, err := db.FindStartedDelegationByAgent(database, "sess-test", "agent-abc")
	if err != nil {
		t.Fatalf("FindStartedDelegationByAgent: %v", err)
	}
	if id != "evt-sdba-1" {
		t.Errorf("got %q, want %q", id, "evt-sdba-1")
	}

	// Different agent -> ErrNoRows.
	_, err = db.FindStartedDelegationByAgent(database, "sess-test", "other-agent")
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for other-agent, got %v", err)
	}
}

func TestFindDelegationByAgent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:   "evt-deleg-agent-1",
		AgentID:   "agent-xyz",
		EventType: models.EventTaskDelegation,
		Timestamp: now,
		ToolName:  "Task",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	id, err := db.FindDelegationByAgent(database, "sess-test", "agent-xyz")
	if err != nil {
		t.Fatalf("FindDelegationByAgent: %v", err)
	}
	if id != "evt-deleg-agent-1" {
		t.Errorf("got %q, want %q", id, "evt-deleg-agent-1")
	}

	// Wrong agent -> ErrNoRows.
	_, err = db.FindDelegationByAgent(database, "sess-test", "other-agent")
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for other-agent, got %v", err)
	}
}

func TestLatestEventByTool(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()

	// Insert two UserQuery events.
	for i, id := range []string{"evt-uq-1", "evt-uq-2"} {
		ev := &models.AgentEvent{
			EventID:      id,
			AgentID:      "claude-code",
			EventType:    models.EventToolCall,
			Timestamp:    now.Add(time.Duration(i) * time.Second),
			ToolName:     "UserQuery",
			InputSummary: "prompt " + id,
			SessionID:    "sess-test",
			Status:       "recorded",
			Source:       "hook",
			CreatedAt:    now.Add(time.Duration(i) * time.Second),
			UpdatedAt:    now.Add(time.Duration(i) * time.Second),
		}
		if err := db.InsertEvent(database, ev); err != nil {
			t.Fatalf("InsertEvent %s: %v", id, err)
		}
	}

	// Should return the latest one.
	id, err := db.LatestEventByTool(database, "sess-test", "UserQuery")
	if err != nil {
		t.Fatalf("LatestEventByTool: %v", err)
	}
	if id != "evt-uq-2" {
		t.Errorf("got %q, want %q", id, "evt-uq-2")
	}

	// No Bash events -> ErrNoRows.
	_, err = db.LatestEventByTool(database, "sess-test", "Bash")
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for Bash, got %v", err)
	}
}

func TestAgentEvent_PopulatesParentAgentID(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()

	// Insert parent event (agent a1).
	evA := &models.AgentEvent{
		EventID:   "evt-parent-a1",
		AgentID:   "agent-a1",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Task",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, evA); err != nil {
		t.Fatalf("InsertEvent evA: %v", err)
	}

	// Insert child event referencing parent; ParentAgentID intentionally empty.
	evB := &models.AgentEvent{
		EventID:       "evt-child-a2",
		AgentID:       "agent-a2",
		EventType:     models.EventToolCall,
		Timestamp:     now.Add(time.Second),
		ToolName:      "Bash",
		SessionID:     "sess-test",
		ParentEventID: "evt-parent-a1",
		Status:        "started",
		Source:        "hook",
		CreatedAt:     now.Add(time.Second),
		UpdatedAt:     now.Add(time.Second),
	}
	if err := db.InsertEvent(database, evB); err != nil {
		t.Fatalf("InsertEvent evB: %v", err)
	}

	got, err := db.GetEvent(database, "evt-child-a2")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.ParentAgentID != "agent-a1" {
		t.Errorf("parent_agent_id: got %q, want %q", got.ParentAgentID, "agent-a1")
	}
}

func TestAgentEvent_NilParentAgentID_NoParent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()

	ev := &models.AgentEvent{
		EventID:   "evt-no-parent",
		AgentID:   "agent-standalone",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Read",
		SessionID: "sess-test",
		// ParentEventID intentionally empty
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	got, err := db.GetEvent(database, "evt-no-parent")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.ParentAgentID != "" {
		t.Errorf("parent_agent_id: got %q, want empty", got.ParentAgentID)
	}
}

func TestAgentEvent_UnknownParent_NoError(t *testing.T) {
	// The FK constraint prevents inserting a completely unknown parent_event_id.
	// This test verifies the safe-lookup path: when ParentAgentID is already set
	// by the caller, InsertEvent must not overwrite it with a lookup result.
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()

	// Insert a real parent event so FK is satisfied.
	evParent := &models.AgentEvent{
		EventID:   "evt-uknp-parent",
		AgentID:   "agent-parent",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Task",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.InsertEvent(database, evParent); err != nil {
		t.Fatalf("InsertEvent parent: %v", err)
	}

	// Child has ParentAgentID pre-set by caller; lookup must not overwrite it.
	ev := &models.AgentEvent{
		EventID:       "evt-uknp-child",
		AgentID:       "agent-child",
		EventType:     models.EventToolCall,
		Timestamp:     now.Add(time.Second),
		ToolName:      "Bash",
		SessionID:     "sess-test",
		ParentEventID: "evt-uknp-parent",
		ParentAgentID: "caller-supplied-value", // caller pre-set, must be preserved
		Status:        "started",
		Source:        "hook",
		CreatedAt:     now.Add(time.Second),
		UpdatedAt:     now.Add(time.Second),
	}
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent child: %v", err)
	}

	got, err := db.GetEvent(database, "evt-uknp-child")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	// Must preserve the caller-supplied value, not overwrite with lookup result.
	if got.ParentAgentID != "caller-supplied-value" {
		t.Errorf("parent_agent_id: got %q, want %q", got.ParentAgentID, "caller-supplied-value")
	}
}

func TestCountRecentDuplicates(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:      "evt-dup-1",
		AgentID:      "claude-code",
		EventType:    models.EventToolCall,
		Timestamp:    now,
		ToolName:     "UserQuery",
		InputSummary: "hello world",
		SessionID:    "sess-test",
		Status:       "recorded",
		Source:       "hook",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	// Count within 5 seconds should find 1.
	count, err := db.CountRecentDuplicates(database, "sess-test", "UserQuery", "hello world", 5)
	if err != nil {
		t.Fatalf("CountRecentDuplicates: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}

	// Different summary -> 0.
	count, err = db.CountRecentDuplicates(database, "sess-test", "UserQuery", "different", 5)
	if err != nil {
		t.Fatalf("CountRecentDuplicates (different): %v", err)
	}
	if count != 0 {
		t.Errorf("count for different: got %d, want 0", count)
	}

	// Different session -> 0.
	count, err = db.CountRecentDuplicates(database, "other-session", "UserQuery", "hello world", 5)
	if err != nil {
		t.Fatalf("CountRecentDuplicates (other session): %v", err)
	}
	if count != 0 {
		t.Errorf("count for other session: got %d, want 0", count)
	}
}

func TestFindOrphanedEvents(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	insert := func(id string, minutesAgo int, status string) {
		t.Helper()
		created := now.Add(-time.Duration(minutesAgo) * time.Minute)
		ev := &models.AgentEvent{
			EventID:   id,
			AgentID:   "claude-code",
			EventType: models.EventToolCall,
			Timestamp: created,
			ToolName:  "Bash",
			SessionID: "sess-test",
			Status:    status,
			Source:    "hook",
			CreatedAt: created,
			UpdatedAt: created,
		}
		if err := db.UpsertEvent(database, ev); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	insert("evt-recent-started", 1, "started")      // too new
	insert("evt-old-started", 10, "started")        // orphan
	insert("evt-old-completed", 10, "completed")    // not started
	insert("evt-ancient-started", 60*30, "started") // orphan, also past 24h

	orphans, err := db.FindOrphanedEvents(database, "", 5*time.Minute)
	if err != nil {
		t.Fatalf("FindOrphanedEvents: %v", err)
	}
	ids := map[string]bool{}
	for _, o := range orphans {
		ids[o.EventID] = true
	}
	if !ids["evt-old-started"] {
		t.Error("expected evt-old-started in orphans")
	}
	if !ids["evt-ancient-started"] {
		t.Error("expected evt-ancient-started in orphans")
	}
	if ids["evt-recent-started"] {
		t.Error("evt-recent-started should not be an orphan (too new)")
	}
	if ids["evt-old-completed"] {
		t.Error("completed events should not be orphans")
	}

	// Session-scoped sweep still finds the old started event.
	scoped, err := db.FindOrphanedEvents(database, "sess-test", 5*time.Minute)
	if err != nil {
		t.Fatalf("FindOrphanedEvents scoped: %v", err)
	}
	if len(scoped) < 2 {
		t.Errorf("expected >=2 scoped orphans, got %d", len(scoped))
	}

	// Session filter excludes other sessions.
	other, err := db.FindOrphanedEvents(database, "other-session", 5*time.Minute)
	if err != nil {
		t.Fatalf("FindOrphanedEvents other: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("expected 0 orphans for other-session, got %d", len(other))
	}
}

// TestFindOrphanedEventsLimited verifies the LIMIT variant returns at most N
// orphans, oldest-first, so the session-start hot path drains a bounded batch
// (bug-504095f2) while leaving the rest for the serve-side drain.
func TestFindOrphanedEventsLimited(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	insert := func(id string, minutesAgo int) {
		t.Helper()
		created := now.Add(-time.Duration(minutesAgo) * time.Minute)
		ev := &models.AgentEvent{
			EventID:   id,
			AgentID:   "claude-code",
			EventType: models.EventToolCall,
			Timestamp: created,
			ToolName:  "Bash",
			SessionID: "sess-test",
			Status:    "started",
			Source:    "hook",
			CreatedAt: created,
			UpdatedAt: created,
		}
		if err := db.UpsertEvent(database, ev); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	// Five orphans, all past the 5m threshold, distinct ages.
	insert("evt-o-10", 10)
	insert("evt-o-40", 40)
	insert("evt-o-20", 20)
	insert("evt-o-50", 50) // oldest
	insert("evt-o-30", 30)

	// Cap at 2 → must return exactly 2, the oldest two (50m, 40m).
	limited, err := db.FindOrphanedEventsLimited(database, "", 5*time.Minute, 2)
	if err != nil {
		t.Fatalf("FindOrphanedEventsLimited: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected exactly 2 capped orphans, got %d", len(limited))
	}
	if limited[0].EventID != "evt-o-50" || limited[1].EventID != "evt-o-40" {
		t.Errorf("expected oldest-first [evt-o-50, evt-o-40], got [%s, %s]",
			limited[0].EventID, limited[1].EventID)
	}

	// limit <= 0 means unlimited — returns all 5.
	all, err := db.FindOrphanedEventsLimited(database, "", 5*time.Minute, 0)
	if err != nil {
		t.Fatalf("FindOrphanedEventsLimited unlimited: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("expected 5 unlimited orphans, got %d", len(all))
	}

	// A cap larger than the population returns everything.
	big, err := db.FindOrphanedEventsLimited(database, "", 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("FindOrphanedEventsLimited big cap: %v", err)
	}
	if len(big) != 5 {
		t.Errorf("expected 5 orphans under a big cap, got %d", len(big))
	}
}

// TestFindStaleProjectOrphans verifies the live-session protection (roborev 448):
// a started event in a NON-terminal (possibly live) session is not returned until
// it crosses the hard cutoff, while a terminal session's event is returned at the
// normal threshold.
func TestFindStaleProjectOrphans(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	mkSession := func(id, status string) {
		t.Helper()
		s := &models.Session{SessionID: id, Status: status, CreatedAt: now.Add(-26 * time.Hour)}
		if status == "completed" {
			c := now.Add(-1 * time.Hour)
			s.CompletedAt = &c
		}
		if err := db.UpsertSession(database, s); err != nil {
			t.Fatalf("seed session %s: %v", id, err)
		}
	}
	mkEvent := func(id, sid string, age time.Duration) {
		t.Helper()
		created := now.Add(-age)
		ev := &models.AgentEvent{
			EventID: id, AgentID: "claude-code", EventType: models.EventToolCall,
			Timestamp: created, ToolName: "Bash", SessionID: sid, Status: "started",
			Source: "hook", CreatedAt: created, UpdatedAt: created,
		}
		if err := db.UpsertEvent(database, ev); err != nil {
			t.Fatalf("seed event %s: %v", id, err)
		}
	}

	mkSession("sess-live", "active")    // non-terminal: protected until hard cutoff
	mkSession("sess-done", "completed") // terminal: swept at normal threshold
	mkEvent("evt-live-recent", "sess-live", 10*time.Minute) // protected (no heartbeat, but < hard cutoff)
	mkEvent("evt-live-ancient", "sess-live", 25*time.Hour)  // past hard cutoff → swept anyway
	mkEvent("evt-done-recent", "sess-done", 10*time.Minute) // terminal → swept

	got, err := db.FindStaleProjectOrphans(database, 5*time.Minute, 24*time.Hour, 0)
	if err != nil {
		t.Fatalf("FindStaleProjectOrphans: %v", err)
	}
	found := map[string]bool{}
	for _, o := range got {
		found[o.EventID] = true
	}
	if found["evt-live-recent"] {
		t.Errorf("evt-live-recent should be PROTECTED (live session, < hard cutoff) but was returned")
	}
	if !found["evt-done-recent"] {
		t.Errorf("evt-done-recent (terminal session) should be swept at the normal threshold")
	}
	if !found["evt-live-ancient"] {
		t.Errorf("evt-live-ancient (past 24h hard cutoff) should be swept even for a live session")
	}
}

func TestMarkEventAborted(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:   "evt-abort-1",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.UpsertEvent(database, ev); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := db.MarkEventAborted(database, "evt-abort-1", "swept")
	if err != nil {
		t.Fatalf("MarkEventAborted: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows affected: got %d, want 1", rows)
	}

	// Second call must report 0 rows (row is already aborted).
	rows, err = db.MarkEventAborted(database, "evt-abort-1", "swept")
	if err != nil {
		t.Fatalf("MarkEventAborted (second): %v", err)
	}
	if rows != 0 {
		t.Errorf("second call rows affected: got %d, want 0", rows)
	}

	got, err := db.GetEvent(database, "evt-abort-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Status != "aborted" {
		t.Errorf("status: got %q, want %q", got.Status, "aborted")
	}

	// Verify reason column populated directly (GetEvent may not surface it).
	var reason sql.NullString
	if err := database.QueryRow(`SELECT reason FROM agent_events WHERE event_id = ?`, "evt-abort-1").Scan(&reason); err != nil {
		t.Fatalf("query reason: %v", err)
	}
	if reason.String != "swept" {
		t.Errorf("reason: got %q, want %q", reason.String, "swept")
	}
}

// TestInsertEventOrphanParent verifies that InsertEvent persists the row even
// when parent_event_id points to a non-existent event (bug-89990f33: dropping
// the self-referential FK on parent_event_id prevents silent insert failures).
func TestInsertEventOrphanParent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:       "evt-orphan-1",
		AgentID:       "claude-code",
		EventType:     models.EventToolCall,
		Timestamp:     now,
		ToolName:      "Read",
		SessionID:     "sess-test",
		ParentEventID: "nonexistent-parent-id",
		Status:        "started",
		Source:        "hook",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent with orphan parent_event_id: %v (FK constraint must not block insert)", err)
	}

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM agent_events WHERE event_id = ?`, "evt-orphan-1").Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Errorf("event not persisted: got count %d, want 1", count)
	}

	var parentID sql.NullString
	if err := database.QueryRow(`SELECT parent_event_id FROM agent_events WHERE event_id = ?`, "evt-orphan-1").Scan(&parentID); err != nil {
		t.Fatalf("query parent_event_id: %v", err)
	}
	if parentID.String != "nonexistent-parent-id" {
		t.Errorf("parent_event_id: got %q, want %q", parentID.String, "nonexistent-parent-id")
	}
}

// TestInsertEventWithAddedColumns verifies that InsertEvent works correctly
// with columns added by later migrations (reason, teammate_name, team_name, prompt_id).
// This indirectly tests that the migration in migrateAgentEventsAddCheckConstraint
// preserves all columns added by post-initial-schema ALTER TABLE statements.
func TestInsertEventWithAddedColumns(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:   "evt-added-cols-1",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: "sess-test",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Insert event (which will run through db.Open and all migrations).
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	// Verify the event exists.
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM agent_events WHERE event_id = ?`, "evt-added-cols-1").Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Errorf("event not persisted: got count %d, want 1", count)
	}

	// Verify that columns added by later migrations exist and can be queried.
	// The migration should have preserved these columns: reason, teammate_name, team_name, prompt_id.
	var (
		reason   sql.NullString
		teammate sql.NullString
		teamName sql.NullString
		promptID sql.NullString
	)
	if err := database.QueryRow(`
		SELECT reason, teammate_name, team_name, prompt_id FROM agent_events WHERE event_id = ?`,
		"evt-added-cols-1").Scan(&reason, &teammate, &teamName, &promptID); err != nil {
		t.Fatalf("query added columns: %v (columns may not exist after migration)", err)
	}
	// All should be NULL since we didn't set them, but they must exist.
	if !reason.Valid || reason.String != "" {
		// NULL is OK, we just need to confirm the column exists
	}
}
