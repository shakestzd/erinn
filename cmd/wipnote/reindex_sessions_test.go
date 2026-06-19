package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// writeSessionHTML writes a session HTML file with an activity log.
func writeSessionHTML(t *testing.T, dir, sessionID, agent, startedAt string, events []sessionEventSpec) string {
	t.Helper()

	liItems := ""
	for _, ev := range events {
		successAttr := fmt.Sprintf(`data-success="%s"`, ev.success)
		featureAttr := ""
		if ev.featureID != "" {
			featureAttr = fmt.Sprintf(` data-feature="%s"`, ev.featureID)
		}
		parentAttr := ""
		if ev.parentEventID != "" {
			parentAttr = fmt.Sprintf(` data-parent="%s"`, ev.parentEventID)
		}
		liItems += fmt.Sprintf(
			"<li data-ts=%q data-tool=%q %s data-event-id=%q%s%s>%s</li>\n",
			ev.ts, ev.tool, successAttr, ev.eventID, featureAttr, parentAttr, ev.text,
		)
	}

	content := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Session Test</title></head>
<body>
  <article id="%s"
           data-type="session"
           data-status="stale"
           data-agent="%s"
           data-started-at="%s"
           data-event-count="%d">
    <header><h1>Session Test</h1></header>
    <section data-activity-log>
      <h3>Activity Log (%d events)</h3>
      <ol reversed>
        %s
      </ol>
    </section>
  </article>
</body>
</html>`, sessionID, agent, startedAt, len(events), len(events), liItems)

	path := filepath.Join(dir, sessionID+".html")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write session HTML %s: %v", path, err)
	}
	return path
}

type sessionEventSpec struct {
	eventID       string
	ts            string
	tool          string
	success       string
	featureID     string
	parentEventID string
	text          string
}

// setupSessionTestDB creates an in-memory DB for session reindex tests.
func setupSessionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestReindexSessions_EventIDMapping(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	sessionID := "022101e1-test-4e8f-8f20-bb042ce50058"
	events := []sessionEventSpec{
		{
			eventID: "evt-abc12345",
			ts:      "2026-03-10T15:36:40.000000",
			tool:    "Bash",
			success: "true",
			text:    "Run tests",
		},
	}
	writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:32:28.000000", events)

	total, upserted, errCount := reindexSessions(database, dir, "/test/project")
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}
	if total != 1 {
		t.Errorf("total files: got %d, want 1", total)
	}
	if upserted != 1 {
		t.Errorf("upserted events: got %d, want 1", upserted)
	}

	got, err := dbpkg.GetEvent(database, "evt-abc12345")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.EventID != "evt-abc12345" {
		t.Errorf("EventID: got %q, want %q", got.EventID, "evt-abc12345")
	}
	if got.SessionID != sessionID {
		t.Errorf("SessionID: got %q, want %q", got.SessionID, sessionID)
	}
}

func TestReindexSessions_RestoresActiveFeatureFromHTML(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	sessionID := "sess-active-feature"
	path := writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:32:28Z", nil)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session html: %v", err)
	}
	updated := strings.Replace(string(data), `data-event-count="0"`, `data-event-count="0" data-active-feature-id="feat-durable"`, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatalf("write session html: %v", err)
	}

	total, _, errCount := reindexSessions(database, dir, "/test/project")
	if total != 1 || errCount != 0 {
		t.Fatalf("reindexSessions total=%d errCount=%d, want total=1 errCount=0", total, errCount)
	}
	if got := dbpkg.GetActiveFeatureIDForSession(database, sessionID); got != "feat-durable" {
		t.Fatalf("active_feature_id = %q, want feat-durable", got)
	}
}

func TestReindexSessions_SuccessStatusMapping(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	sessionID := "033202f2-test-4e8f-8f20-bb042ce50058"
	events := []sessionEventSpec{
		{
			eventID: "evt-success-1",
			ts:      "2026-03-10T15:00:00.000000",
			tool:    "Bash",
			success: "true",
			text:    "Successful command",
		},
		{
			eventID: "evt-failed-1",
			ts:      "2026-03-10T15:01:00.000000",
			tool:    "Bash",
			success: "false",
			text:    "Failed command",
		},
	}
	writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:00:00.000000", events)

	_, _, errCount := reindexSessions(database, dir, "/test/project")
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}

	successEvt, err := dbpkg.GetEvent(database, "evt-success-1")
	if err != nil {
		t.Fatalf("GetEvent success: %v", err)
	}
	if successEvt.Status != "completed" {
		t.Errorf("success status: got %q, want %q", successEvt.Status, "completed")
	}

	failedEvt, err := dbpkg.GetEvent(database, "evt-failed-1")
	if err != nil {
		t.Fatalf("GetEvent failed: %v", err)
	}
	if failedEvt.Status != "failed" {
		t.Errorf("failure status: got %q, want %q", failedEvt.Status, "failed")
	}
}

func TestReindexSessions_FeatureIDMapping(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	// Insert the feature row that the session HTML references (FK constraint).
	database.Exec(`INSERT INTO features (id, type, title, status) VALUES ('feat-5922d683', 'feature', 'Test Feature', 'done')`)

	sessionID := "044303a3-test-4e8f-8f20-bb042ce50058"
	events := []sessionEventSpec{
		{
			eventID:   "evt-feat-1",
			ts:        "2026-03-10T15:00:00.000000",
			tool:      "Read",
			success:   "true",
			featureID: "feat-5922d683",
			text:      "Read file for feature",
		},
		{
			eventID: "evt-nofeat-1",
			ts:      "2026-03-10T15:01:00.000000",
			tool:    "Stop",
			success: "true",
			text:    "Agent stopped",
		},
	}
	writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:00:00.000000", events)

	_, _, errCount := reindexSessions(database, dir, "/test/project")
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}

	withFeat, err := dbpkg.GetEvent(database, "evt-feat-1")
	if err != nil {
		t.Fatalf("GetEvent feat: %v", err)
	}
	if withFeat.FeatureID != "feat-5922d683" {
		t.Errorf("FeatureID: got %q, want %q", withFeat.FeatureID, "feat-5922d683")
	}

	noFeat, err := dbpkg.GetEvent(database, "evt-nofeat-1")
	if err != nil {
		t.Fatalf("GetEvent nofeat: %v", err)
	}
	if noFeat.FeatureID != "" {
		t.Errorf("FeatureID should be empty, got %q", noFeat.FeatureID)
	}
}

func TestReindexSessions_InputSummaryTruncation(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	sessionID := "055404b4-test-4e8f-8f20-bb042ce50058"
	longText := ""
	for i := 0; i < 250; i++ {
		longText += "a"
	}
	events := []sessionEventSpec{
		{
			eventID: "evt-long-1",
			ts:      "2026-03-10T15:00:00.000000",
			tool:    "Bash",
			success: "true",
			text:    longText,
		},
	}
	writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:00:00.000000", events)

	_, _, errCount := reindexSessions(database, dir, "/test/project")
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}

	got, err := dbpkg.GetEvent(database, "evt-long-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if len([]rune(got.InputSummary)) > 200 {
		t.Errorf("InputSummary length %d > 200", len([]rune(got.InputSummary)))
	}
}

func TestReindexSessions_ParentEventID(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	sessionID := "066505c5-test-4e8f-8f20-bb042ce50058"
	events := []sessionEventSpec{
		{
			eventID: "evt-parent-1",
			ts:      "2026-03-10T15:00:00.000000",
			tool:    "Bash",
			success: "true",
			text:    "Parent event",
		},
		{
			eventID:       "evt-child-1",
			ts:            "2026-03-10T15:01:00.000000",
			tool:          "Read",
			success:       "true",
			parentEventID: "evt-parent-1",
			text:          "Child event",
		},
	}
	writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:00:00.000000", events)

	_, _, errCount := reindexSessions(database, dir, "/test/project")
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}

	child, err := dbpkg.GetEvent(database, "evt-child-1")
	if err != nil {
		t.Fatalf("GetEvent child: %v", err)
	}
	if child.ParentEventID != "evt-parent-1" {
		t.Errorf("ParentEventID: got %q, want %q", child.ParentEventID, "evt-parent-1")
	}
}

func TestReindexSessions_IdempotentUpsert(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	sessionID := "077606d6-test-4e8f-8f20-bb042ce50058"
	events := []sessionEventSpec{
		{
			eventID: "evt-idem-1",
			ts:      "2026-03-10T15:00:00.000000",
			tool:    "Bash",
			success: "true",
			text:    "Idempotent event",
		},
	}
	writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:00:00.000000", events)

	// First run.
	_, u1, e1 := reindexSessions(database, dir, "/test/project")
	if e1 != 0 {
		t.Errorf("first run errCount: got %d, want 0", e1)
	}
	if u1 != 1 {
		t.Errorf("first run upserted: got %d, want 1", u1)
	}

	// Second run -- should not error (INSERT OR REPLACE is idempotent).
	_, _, e2 := reindexSessions(database, dir, "/test/project")
	if e2 != 0 {
		t.Errorf("second run errCount: got %d, want 0", e2)
	}
}

func TestReindexSessions_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	for i := 0; i < 3; i++ {
		sessionID := fmt.Sprintf("0888%02d00-test-4e8f-8f20-bb042ce50058", i)
		events := []sessionEventSpec{
			{
				eventID: fmt.Sprintf("evt-multi-%d", i),
				ts:      "2026-03-10T15:00:00.000000",
				tool:    "Bash",
				success: "true",
				text:    fmt.Sprintf("Event %d", i),
			},
		}
		writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:00:00.000000", events)
	}

	total, upserted, errCount := reindexSessions(database, dir, "/test/project")
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}
	if total != 3 {
		t.Errorf("total files: got %d, want 3", total)
	}
	if upserted != 3 {
		t.Errorf("upserted: got %d, want 3", upserted)
	}
}

func TestReindexSessions_SourceIsReindex(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	sessionID := "099707e7-test-4e8f-8f20-bb042ce50058"
	events := []sessionEventSpec{
		{
			eventID: "evt-source-1",
			ts:      "2026-03-10T15:00:00.000000",
			tool:    "Bash",
			success: "true",
			text:    "Check source",
		},
	}
	writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:00:00.000000", events)

	reindexSessions(database, dir, "/test/project")

	got, err := dbpkg.GetEvent(database, "evt-source-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Source != "reindex" {
		t.Errorf("Source: got %q, want %q", got.Source, "reindex")
	}
	if got.EventType != models.EventToolCall {
		t.Errorf("EventType: got %q, want %q", got.EventType, models.EventToolCall)
	}
	if got.AgentID != "claude-code" {
		t.Errorf("AgentID: got %q, want %q", got.AgentID, "claude-code")
	}
}

func TestReindexSessions_TimestampParsing(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	sessionID := "0aa808f8-test-4e8f-8f20-bb042ce50058"
	events := []sessionEventSpec{
		{
			eventID: "evt-ts-1",
			ts:      "2026-03-10T15:36:08.258945",
			tool:    "Bash",
			success: "true",
			text:    "Timestamp without tz",
		},
	}
	writeSessionHTML(t, dir, sessionID, "claude-code", "2026-03-10T14:00:00.000000", events)

	_, _, errCount := reindexSessions(database, dir, "/test/project")
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}

	got, err := dbpkg.GetEvent(database, "evt-ts-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	// The timestamp is stored via RFC3339 (second precision) in SQLite,
	// so we only check date + hour:minute:second.
	expected := time.Date(2026, 3, 10, 15, 36, 8, 0, time.UTC)
	if !got.Timestamp.Equal(expected) {
		t.Errorf("Timestamp: got %v, want %v", got.Timestamp, expected)
	}
}

func TestReindexSessions_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	total, upserted, errCount := reindexSessions(database, dir, "/test/project")
	if total != 0 {
		t.Errorf("total: got %d, want 0", total)
	}
	if upserted != 0 {
		t.Errorf("upserted: got %d, want 0", upserted)
	}
	if errCount != 0 {
		t.Errorf("errCount: got %d, want 0", errCount)
	}
}

func TestReindexSessions_MalformedHTML(t *testing.T) {
	dir := t.TempDir()
	database := setupSessionTestDB(t)

	// Write a file with no <article> tag.
	path := filepath.Join(dir, "bad-session.html")
	if err := os.WriteFile(path, []byte("<html><body>no article</body></html>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	total, _, errCount := reindexSessions(database, dir, "/test/project")
	if total != 1 {
		t.Errorf("total: got %d, want 1", total)
	}
	// Should count as an error (no article found).
	if errCount != 1 {
		t.Errorf("errCount: got %d, want 1", errCount)
	}
}
