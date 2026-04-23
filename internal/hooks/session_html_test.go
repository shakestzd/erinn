package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/htmlgraph/internal/ingest"
	"github.com/shakestzd/htmlgraph/internal/models"
)

func TestCreateSessionHTML(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}

	now := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	s := &models.Session{
		SessionID:     "sess-html-test-001",
		AgentAssigned: "opus-coder",
		Status:        "active",
		CreatedAt:     now,
		StartCommit:   "abc1234",
		IsSubagent:    false,
	}

	CreateSessionHTML(projectDir, s)

	htmlPath := filepath.Join(projectDir, ".htmlgraph", "sessions", "sess-html-test-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("session HTML file not created: %v", err)
	}
	content := string(data)

	// Verify DOCTYPE and structure.
	if !strings.Contains(content, "<!DOCTYPE html>") {
		t.Error("missing DOCTYPE")
	}
	if !strings.Contains(content, `<html lang="en">`) {
		t.Error("missing <html lang>")
	}

	// Verify article attributes.
	if !strings.Contains(content, `id="sess-html-test-001"`) {
		t.Error("missing article id")
	}
	if !strings.Contains(content, `data-type="session"`) {
		t.Error("missing data-type")
	}
	if !strings.Contains(content, `data-status="active"`) {
		t.Error("missing data-status")
	}
	if !strings.Contains(content, `data-agent="opus-coder"`) {
		t.Error("missing data-agent")
	}
	if !strings.Contains(content, `data-started-at="2026-04-08T12:00:00Z"`) {
		t.Error("missing data-started-at")
	}
	if !strings.Contains(content, `data-event-count="0"`) {
		t.Error("missing data-event-count")
	}
	if !strings.Contains(content, `data-is-subagent="false"`) {
		t.Error("missing data-is-subagent")
	}
	if !strings.Contains(content, `data-start-commit="abc1234"`) {
		t.Error("missing data-start-commit")
	}

	// Verify empty activity log structure.
	if !strings.Contains(content, `<section data-activity-log>`) {
		t.Error("missing activity log section")
	}
	if !strings.Contains(content, `<ol reversed>`) {
		t.Error("missing ordered list")
	}
	if !strings.Contains(content, `</ol>`) {
		t.Error("missing </ol> close tag")
	}

	// Verify nav section for edges.
	if !strings.Contains(content, `<nav data-graph-edges>`) {
		t.Error("missing nav data-graph-edges")
	}
}

func TestCreateSessionHTMLSubagent(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}

	s := &models.Session{
		SessionID:     "sess-html-sub-001",
		AgentAssigned: "sonnet-coder",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		StartCommit:   "def5678",
		IsSubagent:    true,
	}

	CreateSessionHTML(projectDir, s)

	htmlPath := filepath.Join(projectDir, ".htmlgraph", "sessions", "sess-html-sub-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("session HTML file not created: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, `data-is-subagent="true"`) {
		t.Error("subagent should have data-is-subagent=true")
	}
}

func TestAppendEventToSessionHTML(t *testing.T) {
	projectDir := t.TempDir()
	sessDir := filepath.Join(projectDir, ".htmlgraph", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	// Create the initial session HTML file.
	s := &models.Session{
		SessionID:     "sess-append-test-001",
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		StartCommit:   "aaa1111",
		IsSubagent:    false,
	}
	CreateSessionHTML(projectDir, s)

	// Append an event.
	ts := time.Date(2026, 4, 8, 13, 30, 0, 0, time.UTC)
	ev := SessionEvent{
		Timestamp: ts,
		ToolName:  "Edit",
		Success:   true,
		EventID:   "evt-test-001",
		FeatureID: "feat-aabbccdd",
		Summary:   "/path/to/file.go (edited)",
	}
	AppendEventToSessionHTML(projectDir, "sess-append-test-001", ev)

	htmlPath := filepath.Join(sessDir, "sess-append-test-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read session HTML: %v", err)
	}
	content := string(data)

	// Verify the <li> was inserted.
	if !strings.Contains(content, `data-tool="Edit"`) {
		t.Error("missing data-tool attribute")
	}
	if !strings.Contains(content, `data-success="true"`) {
		t.Error("missing data-success attribute")
	}
	if !strings.Contains(content, `data-event-id="evt-test-001"`) {
		t.Error("missing data-event-id attribute")
	}
	if !strings.Contains(content, `data-feature="feat-aabbccdd"`) {
		t.Error("missing data-feature attribute")
	}
	if !strings.Contains(content, `/path/to/file.go (edited)`) {
		t.Error("missing summary text")
	}
	if !strings.Contains(content, `data-ts="2026-04-08T13:30:00Z"`) {
		t.Error("missing data-ts attribute")
	}

	// Verify the <li> is inside <ol reversed> ... </ol>.
	olIdx := strings.Index(content, "<ol reversed>")
	liIdx := strings.Index(content, "<li ")
	closOlIdx := strings.Index(content, "</ol>")
	if olIdx == -1 || liIdx == -1 || closOlIdx == -1 {
		t.Fatal("missing ol/li/close-ol structure")
	}
	if liIdx < olIdx || liIdx > closOlIdx {
		t.Error("<li> should be between <ol reversed> and </ol>")
	}
}

func TestAppendMultipleEventsToSessionHTML(t *testing.T) {
	projectDir := t.TempDir()
	sessDir := filepath.Join(projectDir, ".htmlgraph", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	s := &models.Session{
		SessionID:     "sess-multi-append-001",
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		IsSubagent:    false,
	}
	CreateSessionHTML(projectDir, s)

	// Append two events.
	AppendEventToSessionHTML(projectDir, "sess-multi-append-001", SessionEvent{
		Timestamp: time.Now().UTC(),
		ToolName:  "Read",
		Success:   true,
		EventID:   "evt-multi-001",
		Summary:   "first event",
	})
	AppendEventToSessionHTML(projectDir, "sess-multi-append-001", SessionEvent{
		Timestamp: time.Now().UTC(),
		ToolName:  "Write",
		Success:   true,
		EventID:   "evt-multi-002",
		Summary:   "second event",
	})

	htmlPath := filepath.Join(sessDir, "sess-multi-append-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read session HTML: %v", err)
	}
	content := string(data)

	// Both events should be present.
	if !strings.Contains(content, `data-event-id="evt-multi-001"`) {
		t.Error("first event missing")
	}
	if !strings.Contains(content, `data-event-id="evt-multi-002"`) {
		t.Error("second event missing")
	}

	// Count <li> elements.
	if c := strings.Count(content, "<li "); c != 2 {
		t.Errorf("expected 2 <li> elements, got %d", c)
	}
}

func TestFinalizeSessionHTML(t *testing.T) {
	projectDir := t.TempDir()
	sessDir := filepath.Join(projectDir, ".htmlgraph", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	s := &models.Session{
		SessionID:     "sess-finalize-001",
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		IsSubagent:    false,
	}
	CreateSessionHTML(projectDir, s)

	// Append 3 events so event count is meaningful.
	for i := 0; i < 3; i++ {
		AppendEventToSessionHTML(projectDir, "sess-finalize-001", SessionEvent{
			Timestamp: time.Now().UTC(),
			ToolName:  "Bash",
			Success:   true,
			EventID:   "evt-fin-" + string(rune('a'+i)),
			Summary:   "event",
		})
	}

	endedAt := "2026-04-08T15:00:00Z"
	FinalizeSessionHTML(projectDir, "sess-finalize-001", endedAt, "completed", 3)

	htmlPath := filepath.Join(sessDir, "sess-finalize-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read session HTML: %v", err)
	}
	content := string(data)

	// Verify status changed.
	if !strings.Contains(content, `data-status="completed"`) {
		t.Error("data-status should be completed")
	}
	if strings.Contains(content, `data-status="active"`) {
		t.Error("data-status should NOT still be active")
	}

	// Verify ended-at was added.
	if !strings.Contains(content, `data-ended-at="2026-04-08T15:00:00Z"`) {
		t.Error("missing data-ended-at attribute")
	}

	// Verify event count updated.
	if !strings.Contains(content, `data-event-count="3"`) {
		t.Error("data-event-count should be 3")
	}

	// Verify badge text updated.
	if !strings.Contains(content, `status-completed`) {
		t.Error("badge class should show completed")
	}
	if !strings.Contains(content, `3 events`) {
		t.Error("badge text should show 3 events")
	}
}

func TestMissingSessionHTMLDoesNotError(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}

	// Appending to a non-existent session file should not panic or error.
	AppendEventToSessionHTML(projectDir, "nonexistent-session", SessionEvent{
		Timestamp: time.Now().UTC(),
		ToolName:  "Read",
		Success:   true,
		EventID:   "evt-missing-001",
		Summary:   "should be silently ignored",
	})

	// Finalizing a non-existent session file should not panic or error.
	FinalizeSessionHTML(projectDir, "nonexistent-session", "2026-04-08T16:00:00Z", "completed", 5)

	// If we reach here without panicking, the test passes.
}

func TestCreateSessionHTMLCreatesDirectory(t *testing.T) {
	projectDir := t.TempDir()
	// Only create .htmlgraph, NOT .htmlgraph/sessions — CreateSessionHTML should handle it.
	if err := os.MkdirAll(filepath.Join(projectDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}

	s := &models.Session{
		SessionID:     "sess-mkdir-test-001",
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		IsSubagent:    false,
	}
	CreateSessionHTML(projectDir, s)

	htmlPath := filepath.Join(projectDir, ".htmlgraph", "sessions", "sess-mkdir-test-001.html")
	if _, err := os.Stat(htmlPath); os.IsNotExist(err) {
		t.Error("CreateSessionHTML should create the sessions directory automatically")
	}
}

func TestRenderIngestedSessionHTML_Shape(t *testing.T) {
	projectDir := t.TempDir()
	htmlgraphDir := filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(htmlgraphDir, 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}

	msgTs := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	result := &ingest.ParseResult{
		SessionID: "sess-render-001",
		Messages: []models.Message{
			{Ordinal: 0, Role: "user", Timestamp: msgTs},
			{Ordinal: 1, Role: "assistant", Timestamp: msgTs.Add(5 * time.Second)},
		},
		ToolCalls: []models.ToolCall{
			{MessageOrdinal: 1, ToolName: "Read", ToolUseID: "tu-1", InputJSON: `{"file_path":"/mock/a.go"}`},
			{MessageOrdinal: 1, ToolName: "Edit", ToolUseID: "tu-2", InputJSON: `{"file_path":"/mock/a.go"}`},
			{MessageOrdinal: 1, ToolName: "Bash", ToolUseID: "tu-3", InputJSON: `{"command":"echo hi"}`},
		},
		Model: "claude-sonnet-4-5",
	}

	if err := RenderIngestedSessionHTML(htmlgraphDir, "sess-render-001", "/src/project", result, false); err != nil {
		t.Fatalf("RenderIngestedSessionHTML: %v", err)
	}

	htmlPath := filepath.Join(htmlgraphDir, "sessions", "sess-render-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read rendered HTML: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, `id="sess-render-001"`) {
		t.Error("missing article id")
	}
	if !strings.Contains(content, `data-event-count="3"`) {
		t.Errorf("data-event-count should be 3 after finalize, got:\n%s", content)
	}
	if !strings.Contains(content, `data-status="completed"`) {
		t.Error("status should be completed after finalize")
	}
	for _, tool := range []string{"Read", "Edit", "Bash"} {
		if !strings.Contains(content, `data-tool="`+tool+`"`) {
			t.Errorf("missing data-tool=%q", tool)
		}
	}
	// Event IDs must match ingest.EventID so reindex dedup works.
	for i, tc := range result.ToolCalls {
		want := ingest.EventID("sess-render-001", tc.ToolUseID, tc.ToolName, i)
		if !strings.Contains(content, `data-event-id="`+want+`"`) {
			t.Errorf("missing data-event-id=%q for tool %q", want, tc.ToolName)
		}
	}
}

func TestRenderIngestedSessionHTML_SkipExisting(t *testing.T) {
	projectDir := t.TempDir()
	htmlgraphDir := filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(filepath.Join(htmlgraphDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	// Pre-seed the target file with sentinel content representing a live-hook write.
	htmlPath := filepath.Join(htmlgraphDir, "sessions", "sess-exists-001.html")
	sentinel := []byte("<!DOCTYPE html><html><body>LIVE-HOOK-CONTENT</body></html>\n")
	if err := os.WriteFile(htmlPath, sentinel, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	result := &ingest.ParseResult{
		Messages:  []models.Message{{Ordinal: 0, Timestamp: time.Now().UTC()}},
		ToolCalls: []models.ToolCall{{MessageOrdinal: 0, ToolName: "Read", ToolUseID: "tu-x"}},
	}
	if err := RenderIngestedSessionHTML(htmlgraphDir, "sess-exists-001", "/src/project", result, false); err != nil {
		t.Fatalf("RenderIngestedSessionHTML: %v", err)
	}

	got, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read after render: %v", err)
	}
	if string(got) != string(sentinel) {
		t.Errorf("skip-if-exists failed: file was overwritten.\nwant: %q\n got: %q", sentinel, got)
	}
}

func TestRenderIngestedSessionHTML_ForceOverwrite(t *testing.T) {
	projectDir := t.TempDir()
	htmlgraphDir := filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(filepath.Join(htmlgraphDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	htmlPath := filepath.Join(htmlgraphDir, "sessions", "sess-force-001.html")
	if err := os.WriteFile(htmlPath, []byte("STALE"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	result := &ingest.ParseResult{
		Messages:  []models.Message{{Ordinal: 0, Timestamp: time.Now().UTC()}},
		ToolCalls: []models.ToolCall{{MessageOrdinal: 0, ToolName: "Read", ToolUseID: "tu-force"}},
	}
	if err := RenderIngestedSessionHTML(htmlgraphDir, "sess-force-001", "/src/project", result, true); err != nil {
		t.Fatalf("RenderIngestedSessionHTML: %v", err)
	}

	got, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read after render: %v", err)
	}
	if string(got) == "STALE" {
		t.Error("force=true should have overwritten the stale file")
	}
	if !strings.Contains(string(got), `id="sess-force-001"`) {
		t.Error("force-overwritten file should contain the rendered article")
	}
}

func TestRenderIngestedSessionHTML_Idempotent(t *testing.T) {
	projectDir := t.TempDir()
	htmlgraphDir := filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(htmlgraphDir, 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}

	result := &ingest.ParseResult{
		Messages:  []models.Message{{Ordinal: 0, Timestamp: time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)}},
		ToolCalls: []models.ToolCall{{MessageOrdinal: 0, ToolName: "Read", ToolUseID: "tu-idem"}},
	}
	if err := RenderIngestedSessionHTML(htmlgraphDir, "sess-idem-001", "/src", result, false); err != nil {
		t.Fatalf("first render: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(htmlgraphDir, "sessions", "sess-idem-001.html"))
	if err != nil {
		t.Fatalf("read first: %v", err)
	}

	// Second run is a no-op because the file already exists.
	if err := RenderIngestedSessionHTML(htmlgraphDir, "sess-idem-001", "/src", result, false); err != nil {
		t.Fatalf("second render: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(htmlgraphDir, "sessions", "sess-idem-001.html"))
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(first) != string(second) {
		t.Error("second render must be a no-op when the target file exists")
	}
}

func TestAppendEventHTMLEscaping(t *testing.T) {
	projectDir := t.TempDir()
	sessDir := filepath.Join(projectDir, ".htmlgraph", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	s := &models.Session{
		SessionID:     "sess-escape-test-001",
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		IsSubagent:    false,
	}
	CreateSessionHTML(projectDir, s)

	// Append an event with HTML-special characters in the summary.
	AppendEventToSessionHTML(projectDir, "sess-escape-test-001", SessionEvent{
		Timestamp: time.Now().UTC(),
		ToolName:  "Bash",
		Success:   true,
		EventID:   "evt-escape-001",
		Summary:   `echo "hello <world>" && cat file`,
	})

	htmlPath := filepath.Join(sessDir, "sess-escape-test-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read session HTML: %v", err)
	}
	content := string(data)

	// The summary should be HTML-escaped.
	if strings.Contains(content, "<world>") {
		t.Error("summary should be HTML-escaped, found raw <world>")
	}
	if !strings.Contains(content, "&lt;world&gt;") {
		t.Error("summary should contain HTML-escaped &lt;world&gt;")
	}
}
