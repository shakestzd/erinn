package planchat

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// openTestDB creates an in-memory SQLite database with the full wipnote schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestNewBackend(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-test-123", "some plan context", "/tmp/project")

	if b.PlanID != "plan-test-123" {
		t.Errorf("PlanID: got %q, want %q", b.PlanID, "plan-test-123")
	}
	if b.PlanContext != "some plan context" {
		t.Errorf("PlanContext: got %q, want expected", b.PlanContext)
	}
	if b.ProjectDir != "/tmp/project" {
		t.Errorf("ProjectDir: got %q, want /tmp/project", b.ProjectDir)
	}
}

func TestSaveAndLoadHistory(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-hist-test", "ctx", "/tmp")

	if err := b.SaveMessage("user", "What are the risks?"); err != nil {
		t.Fatalf("save user message: %v", err)
	}
	if err := b.SaveMessage("assistant", "The main risks are..."); err != nil {
		t.Fatalf("save assistant message: %v", err)
	}

	msgs, err := b.LoadHistory()
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("message count: got %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "What are the risks?" {
		t.Errorf("msg[0]: got role=%q content=%q", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "The main risks are..." {
		t.Errorf("msg[1]: got role=%q content=%q", msgs[1].Role, msgs[1].Content)
	}
}

func TestSaveMessage_AppendsToExisting(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-append-test", "ctx", "/tmp")

	_ = b.SaveMessage("user", "first")
	_ = b.SaveMessage("assistant", "reply1")

	// Create a new backend (simulating restart) and append more
	b2 := New(database, "plan-append-test", "ctx", "/tmp")
	_ = b2.SaveMessage("user", "second")
	_ = b2.SaveMessage("assistant", "reply2")

	msgs, _ := b2.LoadHistory()
	if len(msgs) != 4 {
		t.Fatalf("message count: got %d, want 4", len(msgs))
	}
	if msgs[2].Content != "second" {
		t.Errorf("msg[2].content: got %q, want 'second'", msgs[2].Content)
	}
}

func TestSessionIDPersistence(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-sess-test", "ctx", "/tmp")

	// Initially no session ID
	if b.sessionID != "" {
		t.Errorf("initial sessionID: got %q, want empty", b.sessionID)
	}

	// Save a session ID
	b.sessionID = "sess-uuid-12345"
	b.saveSessionID()

	// New backend with same plan ID should load the session ID
	b2 := New(database, "plan-sess-test", "ctx", "/tmp")
	if b2.sessionID != "sess-uuid-12345" {
		t.Errorf("loaded sessionID: got %q, want %q", b2.sessionID, "sess-uuid-12345")
	}
}

func TestSessionIDPersistence_ClearSession(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-sess-clear", "ctx", "/tmp")
	b.sessionID = "old-session"
	b.saveSessionID()

	// Clear the session
	b.sessionID = ""
	b.saveSessionID()

	b2 := New(database, "plan-sess-clear", "ctx", "/tmp")
	if b2.sessionID != "" {
		t.Errorf("after clear sessionID: got %q, want empty", b2.sessionID)
	}
}

func TestIsAvailable(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-avail-test", "ctx", "/tmp")

	// Test IsAvailable reflects whether claude is on PATH.
	// We cannot guarantee claude is installed in CI, so just
	// verify the function returns a boolean without panicking.
	_ = b.IsAvailable()
}

func TestLoadHistory_Empty(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-empty-hist", "ctx", "/tmp")

	msgs, err := b.LoadHistory()
	if err != nil {
		t.Fatalf("load empty history: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("message count: got %d, want 0", len(msgs))
	}
}

func TestLoadHistory_DifferentPlans(t *testing.T) {
	database := openTestDB(t)
	b1 := New(database, "plan-A", "ctx", "/tmp")
	b2 := New(database, "plan-B", "ctx", "/tmp")

	_ = b1.SaveMessage("user", "message for plan A")
	_ = b2.SaveMessage("user", "message for plan B")

	msgs1, _ := b1.LoadHistory()
	msgs2, _ := b2.LoadHistory()

	if len(msgs1) != 1 || msgs1[0].Content != "message for plan A" {
		t.Errorf("plan-A messages wrong: %+v", msgs1)
	}
	if len(msgs2) != 1 || msgs2[0].Content != "message for plan B" {
		t.Errorf("plan-B messages wrong: %+v", msgs2)
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-sys-test", "my plan YAML content", "/tmp")

	prompt := b.buildSystemPrompt()

	if prompt == "" {
		t.Fatal("system prompt is empty")
	}
	// Should contain the plan context
	if !containsStr(prompt, "my plan YAML content") {
		t.Error("system prompt missing plan context")
	}
	// Should contain the plan-context XML tags
	if !containsStr(prompt, "<plan-context>") {
		t.Error("system prompt missing <plan-context> tag")
	}
	if !containsStr(prompt, "</plan-context>") {
		t.Error("system prompt missing </plan-context> tag")
	}
	// Should contain the role description
	if !containsStr(prompt, "plan review assistant") {
		t.Error("system prompt missing role description")
	}
	// Should contain AMEND directive instructions
	if !containsStr(prompt, "AMEND") {
		t.Error("system prompt missing AMEND instructions")
	}
}

// --- Argv construction tests (bug-33348600) -----------------------------
//
// buildCmd is the other headless `claude -p` consumer alongside
// cmd/wipnote/compliance_auto.go's realHeadlessInvoker. It has a different
// flag profile (--append-system-prompt instead of --system-prompt, no
// --allowedTools/--permission-mode) with the same exposure: nothing asserted
// the exact argv, so an upstream rename would go undetected until runtime.
// These two tests close that hole the same way compliance_auto_test.go's do.

// TestBuildCmd_GoldenFlags freezes the exact flag set and order buildCmd
// produces for a fresh (no prior session) chat turn. Runs everywhere (no
// `claude` binary required); catches accidental changes to wipnote's own
// argv-construction code, not upstream flag renames — see
// TestBuildCmd_ValidatedAgainstInstalledCLI for that.
func TestBuildCmd_GoldenFlags(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-golden-test", "ctx", "/tmp")

	args := b.buildCmd("/usr/bin/claude", "hello world")
	want := []string{
		"/usr/bin/claude",
		"-p", "hello world",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--append-system-prompt", b.buildSystemPrompt(),
	}

	if len(args) != len(want) {
		t.Fatalf("arg count: got %d %v, want %d %v", len(args), args, len(want), want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg[%d]: got %q, want %q\nfull got:  %v\nfull want: %v", i, args[i], want[i], args, want)
		}
	}
}

// TestBuildCmd_GoldenFlags_WithResume verifies --resume <session-id> is
// appended, in that order, once a session ID has been established.
func TestBuildCmd_GoldenFlags_WithResume(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-golden-resume-test", "ctx", "/tmp")
	b.sessionID = "sess-abc-123"

	args := b.buildCmd("/usr/bin/claude", "hello")

	if len(args) < 2 || args[len(args)-2] != "--resume" || args[len(args)-1] != "sess-abc-123" {
		t.Errorf("expected trailing [\"--resume\", \"sess-abc-123\"]; got %v", args)
	}
}

// TestBuildCmd_ValidatedAgainstInstalledCLI checks that every flag buildCmd
// constructs is still recognized by whatever `claude` binary is actually
// installed in this environment.
//
// Technique (shared with cmd/wipnote/compliance_auto_test.go's
// TestBuildHeadlessArgs_ValidatedAgainstInstalledCLI): append a canary flag
// that can never be a real claude option, then run the real args + canary
// through claude's own argument parser (commander, based on its
// "error: unknown option '<flag>'" message shape). Commander validates
// options left-to-right and fails on the FIRST one it doesn't recognize, so:
//   - if every real flag before the canary is still valid, the reported
//     error names the canary specifically -> the real flags all still parse.
//   - if any real flag has since been renamed/removed upstream, the error
//     names THAT flag instead -> fail, with the offending flag in the output.
//
// This never reaches the network or invokes the model: commander bails
// during option parsing, before the print action ever runs (confirmed
// empirically during the bug-33348600 investigation: "error: unknown option"
// on stderr with exit code 1, no model output).
//
// Skipped when `claude` is not on PATH (e.g. most CI images); the golden
// tests above are the always-on baseline that still run there.
func TestBuildCmd_ValidatedAgainstInstalledCLI(t *testing.T) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not found on PATH; skipping live argv validation")
	}

	database := openTestDB(t)
	b := New(database, "plan-live-validate", "test plan context", "/tmp")

	args := b.buildCmd(claudePath, "hello")
	const canary = "--wipnote-argv-canary-zzz"
	fullArgs := append(args[1:], canary) // args[0] is the binary path, not a CLI flag

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudePath, fullArgs...)
	var stderrBuf, stdoutBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	cmd.Stdout = &stdoutBuf

	runErr := cmd.Run()
	if runErr == nil {
		t.Fatalf("expected claude to reject canary flag %q; it exited 0 instead (stdout: %q, stderr: %q)",
			canary, stdoutBuf.String(), stderrBuf.String())
	}

	stderr := stderrBuf.String()
	if !strings.Contains(stderr, canary) {
		t.Errorf("expected the installed claude CLI to reject the canary flag %q specifically "+
			"(meaning all real flags in buildCmd still parse); got stderr: %q\nargs: %v",
			canary, stderr, fullArgs)
	}
}

func TestExtractStreamEvent_TextDelta(t *testing.T) {
	// Simulate a stream_event with content_block_delta / text_delta.
	raw := `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello..."}}}`
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	inner, _ := event["event"].(map[string]any)
	if inner == nil {
		t.Fatal("inner event is nil")
	}
	innerType, _ := inner["type"].(string)
	if innerType != "content_block_delta" {
		t.Fatalf("inner type: got %q, want content_block_delta", innerType)
	}
	delta, _ := inner["delta"].(map[string]any)
	deltaType, _ := delta["type"].(string)
	if deltaType != "text_delta" {
		t.Fatalf("delta type: got %q, want text_delta", deltaType)
	}
	text, _ := delta["text"].(string)
	if text != "Hello..." {
		t.Errorf("text: got %q, want Hello...", text)
	}
}

func TestExtractStreamEvent_NonTextDelta(t *testing.T) {
	// stream_event with a non-text delta should yield no text.
	raw := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	inner, _ := event["event"].(map[string]any)
	if inner == nil {
		t.Fatal("inner event is nil")
	}
	// type is content_block_start, not content_block_delta — no text should be extracted.
	innerType, _ := inner["type"].(string)
	if innerType == "content_block_delta" {
		t.Fatal("expected non-delta event type for this test case")
	}
}

func TestExtractTextChunks_AssistantEvent(t *testing.T) {
	event := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`
	var parsed map[string]any
	if err := json.Unmarshal([]byte(event), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	chunks := extractTextChunks(parsed)
	if len(chunks) != 1 || chunks[0] != "Hello world" {
		t.Errorf("chunks: got %v", chunks)
	}
}

func TestExtractTextChunks_MultipleBlocks(t *testing.T) {
	event := `{"type":"assistant","message":{"content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}]}}`
	var parsed map[string]any
	json.Unmarshal([]byte(event), &parsed)
	chunks := extractTextChunks(parsed)
	if len(chunks) != 2 {
		t.Errorf("chunk count: got %d, want 2", len(chunks))
	}
}

func TestExtractTextChunks_NoContent(t *testing.T) {
	event := `{"type":"assistant","message":{}}`
	var parsed map[string]any
	json.Unmarshal([]byte(event), &parsed)
	chunks := extractTextChunks(parsed)
	if len(chunks) != 0 {
		t.Errorf("expected empty chunks, got %v", chunks)
	}
}

func TestChatMessage_JSONRoundtrip(t *testing.T) {
	msg := ChatMessage{
		Role:      "user",
		Content:   "test message",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ChatMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Role != msg.Role || decoded.Content != msg.Content {
		t.Errorf("roundtrip mismatch: got %+v", decoded)
	}
}

func TestSendContext_Cancelled(t *testing.T) {
	database := openTestDB(t)
	b := New(database, "plan-ctx-test", "ctx", "/tmp")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	chunks, errCh := b.Send(ctx, "hello")

	// Drain chunks
	var text string
	for c := range chunks {
		text += c
	}
	// Check error channel
	err := <-errCh
	// Should either get a context error or "unavailable" error — just verify no panic
	_ = err
	_ = text
}

// containsStr checks if haystack contains needle as a substring.
func containsStr(haystack, needle string) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
