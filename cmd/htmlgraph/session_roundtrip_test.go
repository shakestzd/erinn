//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/hooks"
	"github.com/shakestzd/htmlgraph/internal/ingest"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// setupRoundTripEnv prepares a temp project with an on-disk db at the
// expected `.htmlgraph/htmlgraph.db` location and returns the paths.
// The db is left closed — callers re-open as needed so sweep and reindex
// both hit a real file that survives between goroutines.
func setupRoundTripEnv(t *testing.T) (projectDir, htmlgraphDir, dbPath string) {
	t.Helper()
	projectDir = t.TempDir()
	htmlgraphDir = filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(filepath.Join(htmlgraphDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	dbPath = filepath.Join(htmlgraphDir, "htmlgraph.db")

	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.Close()
	return
}

// TestSessionRoundTrip_IngestThenReindex verifies that an ingested session
// renders HTML, the HTML parses via goquery, and the rebuilt SQLite row
// after a reindex matches the expected event count.
func TestSessionRoundTrip_IngestThenReindex(t *testing.T) {
	_, htmlgraphDir, dbPath := setupRoundTripEnv(t)

	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	sessionID := "sess-roundtrip-ingest-001"
	msgTs := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	result := &ingest.ParseResult{
		SessionID: sessionID,
		Messages: []models.Message{
			{Ordinal: 0, Role: "user", Timestamp: msgTs},
			{Ordinal: 1, Role: "assistant", Timestamp: msgTs.Add(2 * time.Second)},
		},
		ToolCalls: []models.ToolCall{
			{MessageOrdinal: 1, ToolName: "Read", ToolUseID: "tu-a", InputJSON: `{"file_path":"/mock/a.go"}`},
			{MessageOrdinal: 1, ToolName: "Edit", ToolUseID: "tu-b", InputJSON: `{"file_path":"/mock/a.go"}`},
			{MessageOrdinal: 1, ToolName: "Bash", ToolUseID: "tu-c", InputJSON: `{"command":"go test"}`},
		},
	}

	sess := &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		CreatedAt:     msgTs,
		Status:        "completed",
	}
	if err := dbpkg.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if err := hooks.RenderIngestedSessionHTML(htmlgraphDir, sessionID, "/src/project", result, false); err != nil {
		t.Fatalf("render: %v", err)
	}

	// Blow away every agent_events row so reindex starts from a clean slate.
	if _, err := database.Exec(`DELETE FROM agent_events`); err != nil {
		t.Fatalf("delete events: %v", err)
	}

	total, upserted, errFiles := reindexSessions(database, filepath.Join(htmlgraphDir, "sessions"), filepath.Dir(htmlgraphDir))
	if errFiles != 0 {
		t.Fatalf("reindex errors: %d", errFiles)
	}
	if total == 0 {
		t.Fatal("reindex found no session files")
	}
	if upserted != len(result.ToolCalls) {
		t.Errorf("upserted: got %d, want %d", upserted, len(result.ToolCalls))
	}

	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ?`, sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != len(result.ToolCalls) {
		t.Errorf("agent_events count: got %d, want %d", count, len(result.ToolCalls))
	}
}

// TestSessionRoundTrip_OrphanSweepReindexesAsAborted proves that a swept
// orphan comes back as status=aborted after a reindex.
func TestSessionRoundTrip_OrphanSweepReindexesAsAborted(t *testing.T) {
	projectDir, htmlgraphDir, dbPath := setupRoundTripEnv(t)

	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	sessionID := "sess-roundtrip-sweep-001"
	sess := &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		CreatedAt:     time.Now().UTC(),
		Status:        "active",
	}
	if err := dbpkg.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	hooks.CreateSessionHTML(projectDir, sess)

	old := time.Now().UTC().Add(-10 * time.Minute)
	orphan := &models.AgentEvent{
		EventID:   "evt-sweep-roundtrip-1",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: old,
		ToolName:  "Bash",
		SessionID: sessionID,
		Status:    "started",
		Source:    "hook",
		CreatedAt: old,
		UpdatedAt: old,
	}
	if err := dbpkg.UpsertEvent(database, orphan); err != nil {
		t.Fatalf("UpsertEvent: %v", err)
	}

	if hooks.SweepOrphanedEventsForSession(database, projectDir, sessionID) != 1 {
		t.Fatal("sweep should have appended one synthetic entry")
	}

	// Drop the row and reindex — the rebuilt row must come back as aborted.
	if _, err := database.Exec(`DELETE FROM agent_events`); err != nil {
		t.Fatalf("delete events: %v", err)
	}
	_, upserted, errFiles := reindexSessions(database, filepath.Join(htmlgraphDir, "sessions"), filepath.Dir(htmlgraphDir))
	if errFiles != 0 {
		t.Fatalf("reindex errors: %d", errFiles)
	}
	if upserted != 1 {
		t.Errorf("upserted: got %d, want 1", upserted)
	}

	evt, err := dbpkg.GetEvent(database, "evt-sweep-roundtrip-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if evt.Status != "aborted" {
		t.Errorf("reindexed status: got %q, want aborted", evt.Status)
	}
}

// TestSessionRoundTrip_MigrationBackfill verifies slice 3: SQLite-only
// sessions get rendered and round-trip through reindex.
func TestSessionRoundTrip_MigrationBackfill(t *testing.T) {
	_, htmlgraphDir, dbPath := setupRoundTripEnv(t)

	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	for _, id := range []string{"sess-mig-a", "sess-mig-b"} {
		sess := &models.Session{
			SessionID:     id,
			AgentAssigned: "claude-code",
			CreatedAt:     now,
			Status:        "completed",
		}
		if err := dbpkg.InsertSession(database, sess); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		msg := &models.Message{SessionID: id, Ordinal: 0, Role: "assistant", Content: "x", Timestamp: now}
		msgID, err := dbpkg.InsertMessage(database, msg)
		if err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
		tc := &models.ToolCall{
			MessageID: int(msgID), SessionID: id, ToolName: "Read",
			ToolUseID: "tu-" + id, InputJSON: `{"file_path":"/mock/a.go"}`,
			Category: "Read", MessageOrdinal: 0,
		}
		if err := dbpkg.InsertToolCall(database, tc); err != nil {
			t.Fatalf("InsertToolCall: %v", err)
		}
	}

	// Migrate directly via the internal helpers (no CLI plumbing needed).
	emptyIdx := map[string]ingest.SessionFile{}
	for _, id := range []string{"sess-mig-a", "sess-mig-b"} {
		if err := migrateOneSession(database, htmlgraphDir, id, "sqlite", emptyIdx); err != nil {
			t.Fatalf("migrateOneSession %s: %v", id, err)
		}
	}

	// Reindex round-trip.
	if _, err := database.Exec(`DELETE FROM agent_events`); err != nil {
		t.Fatalf("delete events: %v", err)
	}
	_, upserted, errFiles := reindexSessions(database, filepath.Join(htmlgraphDir, "sessions"), filepath.Dir(htmlgraphDir))
	if errFiles != 0 {
		t.Fatalf("reindex errors: %d", errFiles)
	}
	if upserted != 2 {
		t.Errorf("upserted: got %d, want 2", upserted)
	}
}

// TestSessionRoundTrip_ConcurrentWriters is the load-bearing acceptance
// criterion for the plan. Spawns 20 goroutines across three logical writer
// types — ingest render, live PostToolUse append, and orphan sweep append —
// all targeting the SAME session HTML file, then parses the result via
// goquery and verifies every single event lands in the final file matched
// by data-event-id. Anything less than "all 20 present" is a failure.
func TestSessionRoundTrip_ConcurrentWriters(t *testing.T) {
	projectDir, htmlgraphDir, dbPath := setupRoundTripEnv(t)

	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	sessionID := "sess-concurrent-001"
	sess := &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		CreatedAt:     time.Now().UTC(),
		Status:        "active",
	}
	if err := dbpkg.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	hooks.CreateSessionHTML(projectDir, sess)

	const (
		liveWriters   = 8
		ingestWriters = 4
		sweepWriters  = 8
	)
	total := liveWriters + ingestWriters + sweepWriters

	// Pre-seed one started agent_event per sweep writer so the sweep path
	// has something to turn into a synthetic aborted entry.
	old := time.Now().UTC().Add(-10 * time.Minute)
	sweepIDs := make([]string, sweepWriters)
	for i := 0; i < sweepWriters; i++ {
		sweepIDs[i] = fmt.Sprintf("evt-sweep-%02d", i)
		ev := &models.AgentEvent{
			EventID:   sweepIDs[i],
			AgentID:   "claude-code",
			EventType: models.EventToolCall,
			Timestamp: old,
			ToolName:  "Bash",
			SessionID: sessionID,
			Status:    "started",
			Source:    "hook",
			CreatedAt: old,
			UpdatedAt: old,
		}
		if err := dbpkg.UpsertEvent(database, ev); err != nil {
			t.Fatalf("seed orphan %s: %v", sweepIDs[i], err)
		}
	}

	// Expected event IDs must include every writer's event id so the
	// final assertion covers all 20 targets.
	expected := make(map[string]struct{}, total)

	var wg sync.WaitGroup
	start := make(chan struct{})

	// Live PostToolUse-style appends.
	for i := 0; i < liveWriters; i++ {
		id := fmt.Sprintf("evt-live-%02d", i)
		expected[id] = struct{}{}
		wg.Add(1)
		go func(eventID string) {
			defer wg.Done()
			<-start
			hooks.AppendEventToSessionHTML(projectDir, sessionID, hooks.SessionEvent{
				Timestamp: time.Now().UTC(),
				ToolName:  "Read",
				Success:   true,
				EventID:   eventID,
				Summary:   "live append",
			})
		}(id)
	}

	// Ingest render-style appends (same mechanism, different label).
	for i := 0; i < ingestWriters; i++ {
		id := fmt.Sprintf("evt-ingest-%02d", i)
		expected[id] = struct{}{}
		wg.Add(1)
		go func(eventID string) {
			defer wg.Done()
			<-start
			hooks.AppendEventToSessionHTML(projectDir, sessionID, hooks.SessionEvent{
				Timestamp: time.Now().UTC(),
				ToolName:  "Edit",
				Success:   true,
				EventID:   eventID,
				Summary:   "ingest render",
			})
		}(id)
	}

	// Sweep-style writers — each goroutine runs the sweep scoped to the
	// session, which under the hood calls AppendEventToSessionHTML with
	// data-status="aborted" for every pre-seeded orphan row it still sees.
	// Only the first goroutine will append all N entries; the rest are
	// no-ops because of the dedup check — that's OK. The expected set
	// already includes the sweep IDs from the pre-seed loop above.
	for _, id := range sweepIDs {
		expected[id] = struct{}{}
	}
	for i := 0; i < sweepWriters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			hooks.SweepOrphanedEventsForSession(database, projectDir, sessionID)
		}()
	}

	// Release the herd.
	close(start)
	wg.Wait()

	// Parse via goquery and collect every data-event-id.
	htmlPath := filepath.Join(htmlgraphDir, "sessions", sessionID+".html")
	f, err := os.Open(htmlPath)
	if err != nil {
		t.Fatalf("open final html: %v", err)
	}
	defer f.Close()
	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		t.Fatalf("parse final html: %v", err)
	}

	present := map[string]struct{}{}
	doc.Find("li[data-event-id]").Each(func(_ int, s *goquery.Selection) {
		if v, ok := s.Attr("data-event-id"); ok {
			present[v] = struct{}{}
		}
	})

	var missing []string
	for id := range expected {
		if _, ok := present[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		data, _ := os.ReadFile(htmlPath)
		t.Errorf("concurrent writers lost %d of %d events: %s\nfinal html:\n%s",
			len(missing), len(expected), strings.Join(missing, ", "), string(data))
	}

	// Round-trip through reindex and confirm every expected event comes back.
	if _, err := database.Exec(`DELETE FROM agent_events`); err != nil {
		t.Fatalf("delete events: %v", err)
	}
	_, upserted, errFiles := reindexSessions(database, filepath.Join(htmlgraphDir, "sessions"), filepath.Dir(htmlgraphDir))
	if errFiles != 0 {
		t.Fatalf("reindex errors: %d", errFiles)
	}
	if upserted != len(expected) {
		t.Errorf("reindex upserted: got %d, want %d", upserted, len(expected))
	}
}
