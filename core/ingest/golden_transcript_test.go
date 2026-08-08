package ingest

import (
	"encoding/json"
	"testing"
)

// TestGoldenTranscript_ParseFile is bug-d7741c8f's answer to the same
// drift-detection gap bug-735806ff closed for the OTLP pipeline: the parser
// test suite in parser_test.go asserts against hand-built JSONL that the
// author constructed to match parse()'s existing expectations, which cannot
// notice when a real Claude Code transcript stops matching those
// expectations. This test runs wipnote's OWN ParseFile entry point — the
// same one core/ingest's file-discovery/sync path calls — against a REAL,
// redacted transcript capture (testdata/golden_transcript/ — see
// PROVENANCE.md there for what produced it, when, and how to refresh it).
//
// Each assertion names the field it checks, per the same design intent as
// the OTLP golden capture test: a future Claude Code transcript format
// change that renames or restructures a field this test depends on
// (message.usage.*, message.model, message.stop_reason, tool_use blocks)
// must fail loudly and specifically here, naming the field — not silently
// zero out cost/token/model attribution downstream the way a passing but
// unrepresentative synthetic fixture would.
func TestGoldenTranscript_ParseFile(t *testing.T) {
	result, err := ParseFile("testdata/golden_transcript/transcript.jsonl")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if result.SessionID != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("SessionID = %q, want the capture's (redacted) session id", result.SessionID)
	}

	// ai-title: real captures name sessions via an "ai-title" line, and this
	// capture has no "custom-title" line, so the ai-title must win outright.
	if result.Title != "Append text to hello.txt file" {
		t.Errorf("Title = %q, want %q (from the capture's ai-title line)", result.Title, "Append text to hello.txt file")
	}

	// Most-used model across the capture's 4 assistant turns, all on the
	// same model in this capture — this is the field cost/token attribution
	// keys off of when a session's usage rows don't carry their own model.
	if result.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want %q (most-used model across the capture)", result.Model, "claude-opus-5")
	}

	// The capture's 3 "attachment"-type lines (deferred_tools_delta,
	// agent_listing_delta, skill_listing) and 2 "queue-operation" lines and
	// 1 "last-prompt" line must produce zero messages: parse()'s switch has
	// no case for "attachment" or "last-prompt" (silently falls through) and
	// an explicit skip case for "queue-operation". The capture's 2
	// tool-result "user" lines must also produce zero messages (filtered by
	// the hasToolResult check). That leaves exactly 5: 1 user prompt + 4
	// assistant turns.
	if len(result.Messages) != 5 {
		t.Fatalf("got %d messages, want 5 (1 user + 4 assistant; attachment/queue-operation/last-prompt/tool-result lines must not produce messages)", len(result.Messages))
	}

	user := result.Messages[0]
	if user.Role != "user" {
		t.Errorf("Messages[0].Role = %q, want %q", user.Role, "user")
	}
	wantPrompt := "Read hello.txt and then use the Edit tool to append a fourth line that says 'line four'."
	if user.Content != wantPrompt {
		t.Errorf("Messages[0].Content = %q, want %q", user.Content, wantPrompt)
	}

	// First assistant turn: text only, no tool use yet.
	asst1 := result.Messages[1]
	if asst1.Role != "assistant" {
		t.Errorf("Messages[1].Role = %q, want %q", asst1.Role, "assistant")
	}
	if asst1.Content != "I'll read the file first." {
		t.Errorf("Messages[1].Content = %q, want %q", asst1.Content, "I'll read the file first.")
	}
	if asst1.Model != "claude-opus-5" {
		t.Errorf("Messages[1].Model = %q, want %q", asst1.Model, "claude-opus-5")
	}
	if asst1.StopReason != "tool_use" {
		t.Errorf("Messages[1].StopReason = %q, want %q", asst1.StopReason, "tool_use")
	}
	if asst1.InputTokens != 2 {
		t.Errorf("Messages[1].InputTokens = %d, want 2", asst1.InputTokens)
	}
	if asst1.OutputTokens != 128 {
		t.Errorf("Messages[1].OutputTokens = %d, want 128", asst1.OutputTokens)
	}
	if asst1.CacheReadTokens != 15498 {
		t.Errorf("Messages[1].CacheReadTokens = %d, want 15498", asst1.CacheReadTokens)
	}
	if asst1.HasToolUse {
		t.Error("Messages[1].HasToolUse = true, want false (this turn is text-only)")
	}

	// Second assistant turn: the Read tool_use block, no text.
	asst2 := result.Messages[2]
	if !asst2.HasToolUse {
		t.Error("Messages[2].HasToolUse = false, want true (this turn carries the Read tool_use block)")
	}
	if asst2.Content != "" {
		t.Errorf("Messages[2].Content = %q, want empty (no text block in this turn)", asst2.Content)
	}
	if asst2.OutputTokens != 128 {
		t.Errorf("Messages[2].OutputTokens = %d, want 128", asst2.OutputTokens)
	}

	// Fourth (final) assistant turn: end_turn, text only again.
	asst4 := result.Messages[4]
	wantFinal := "Appended `line four` as the fourth line of `hello.txt`."
	if asst4.Content != wantFinal {
		t.Errorf("Messages[4].Content = %q, want %q", asst4.Content, wantFinal)
	}
	if asst4.StopReason != "end_turn" {
		t.Errorf("Messages[4].StopReason = %q, want %q", asst4.StopReason, "end_turn")
	}
	if asst4.OutputTokens != 24 {
		t.Errorf("Messages[4].OutputTokens = %d, want 24", asst4.OutputTokens)
	}
	if asst4.CacheReadTokens != 23449 {
		t.Errorf("Messages[4].CacheReadTokens = %d, want 23449", asst4.CacheReadTokens)
	}

	// Tool calls: exactly the Read then the Edit, in order, each carrying
	// its real (redacted) tool_use_id and a real InputJSON payload — this is
	// the same class of field bug-5652a5ba found silently empty on the OTLP
	// side for the column's entire lifetime because no synthetic fixture
	// ever needed to set it.
	if len(result.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2 (Read, Edit)", len(result.ToolCalls))
	}

	read := result.ToolCalls[0]
	if read.ToolName != "Read" {
		t.Errorf("ToolCalls[0].ToolName = %q, want %q", read.ToolName, "Read")
	}
	if read.Category != "Read" {
		t.Errorf("ToolCalls[0].Category = %q, want %q", read.Category, "Read")
	}
	if read.ToolUseID != "toolu_000000000000000000000001" {
		t.Errorf("ToolCalls[0].ToolUseID = %q, want the capture's (redacted) tool_use_id", read.ToolUseID)
	}
	if read.MessageOrdinal != 2 {
		t.Errorf("ToolCalls[0].MessageOrdinal = %d, want 2 (must link back to Messages[2])", read.MessageOrdinal)
	}
	var readInput struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(read.InputJSON), &readInput); err != nil {
		t.Fatalf("ToolCalls[0].InputJSON did not parse as JSON: %v (raw: %s)", err, read.InputJSON)
	}
	if readInput.FilePath != "/home/dev/sample-project/hello.txt" {
		t.Errorf("ToolCalls[0] input file_path = %q, want %q", readInput.FilePath, "/home/dev/sample-project/hello.txt")
	}

	edit := result.ToolCalls[1]
	if edit.ToolName != "Edit" {
		t.Errorf("ToolCalls[1].ToolName = %q, want %q", edit.ToolName, "Edit")
	}
	if edit.ToolUseID != "toolu_000000000000000000000002" {
		t.Errorf("ToolCalls[1].ToolUseID = %q, want the capture's (redacted) tool_use_id", edit.ToolUseID)
	}
	if edit.ToolUseID == read.ToolUseID {
		t.Error("ToolCalls[1].ToolUseID equals ToolCalls[0].ToolUseID, want distinct ids per real capture")
	}
	if edit.MessageOrdinal != 3 {
		t.Errorf("ToolCalls[1].MessageOrdinal = %d, want 3 (must link back to Messages[3])", edit.MessageOrdinal)
	}
	var editInput struct {
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal([]byte(edit.InputJSON), &editInput); err != nil {
		t.Fatalf("ToolCalls[1].InputJSON did not parse as JSON: %v (raw: %s)", err, edit.InputJSON)
	}
	if editInput.FilePath != "/home/dev/sample-project/hello.txt" {
		t.Errorf("ToolCalls[1] input file_path = %q, want %q", editInput.FilePath, "/home/dev/sample-project/hello.txt")
	}
	if editInput.OldString != "line three" {
		t.Errorf("ToolCalls[1] input old_string = %q, want %q", editInput.OldString, "line three")
	}
	if editInput.NewString != "line three\nline four" {
		t.Errorf("ToolCalls[1] input new_string = %q, want %q", editInput.NewString, "line three\nline four")
	}
}
