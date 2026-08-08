package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteWipnoteClaimCommandsForAgent_SimpleCommand(t *testing.T) {
	got, ok := rewriteWipnoteClaimCommandsForAgent("wipnote feature start feat-1234", "impl-graph")
	if !ok {
		t.Fatalf("expected a rewrite")
	}
	want := "WIPNOTE_AGENT_ID=impl-graph wipnote feature start feat-1234"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_ChainedAndCommand(t *testing.T) {
	// The orchestrator batches wipnote calls with && constantly — this is the
	// common case, not an edge case (per bug-190950e0 review notes).
	cmd := "wipnote feature start feat-1234 && echo started"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
	if !ok {
		t.Fatalf("expected a rewrite")
	}
	want := "WIPNOTE_AGENT_ID=impl-graph wipnote feature start feat-1234 && echo started"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_WipnoteNotFirstInChain(t *testing.T) {
	cmd := "cd /tmp && wipnote bug complete bug-abcd1234"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "fix-argv")
	if !ok {
		t.Fatalf("expected a rewrite")
	}
	want := "cd /tmp && WIPNOTE_AGENT_ID=fix-argv wipnote bug complete bug-abcd1234"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_MultipleSegmentsBothRewritten(t *testing.T) {
	cmd := "wipnote feature start feat-1234 && wipnote feature start feat-1234"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
	if !ok {
		t.Fatalf("expected a rewrite")
	}
	want := "WIPNOTE_AGENT_ID=impl-graph wipnote feature start feat-1234 && " +
		"WIPNOTE_AGENT_ID=impl-graph wipnote feature start feat-1234"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_QuotedStringNotRewritten(t *testing.T) {
	cmd := `echo "wipnote feature start feat-1234"`
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
	if ok {
		t.Fatalf("expected no rewrite, got %q", got)
	}
	if got != cmd {
		t.Fatalf("command must be returned unchanged, got %q", got)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_CommentNotRewritten(t *testing.T) {
	cmd := "# wipnote feature start feat-1234\nwipnote feature list"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
	if ok {
		t.Fatalf("expected no rewrite (comment line + non-claim subcommand), got %q", got)
	}
	if got != cmd {
		t.Fatalf("command must be returned unchanged, got %q", got)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_TrailingCommentIgnored(t *testing.T) {
	cmd := "echo done # wipnote bug complete bug-abcd1234"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
	if ok {
		t.Fatalf("expected no rewrite, got %q", got)
	}
	if got != cmd {
		t.Fatalf("command must be returned unchanged, got %q", got)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_HeredocNotRewritten(t *testing.T) {
	cmd := "cat <<'EOF'\nwipnote feature start feat-1234\nEOF"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
	if ok {
		t.Fatalf("expected no rewrite for a command containing a heredoc, got %q", got)
	}
	if got != cmd {
		t.Fatalf("command must be returned unchanged, got %q", got)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_EmptyAgentIDNoRewrite(t *testing.T) {
	cmd := "wipnote feature start feat-1234"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "")
	if ok {
		t.Fatalf("expected no rewrite when agentID is empty, got %q", got)
	}
	if got != cmd {
		t.Fatalf("command must be returned unchanged, got %q", got)
	}
	if strings.Contains(got, "WIPNOTE_AGENT_ID=") {
		t.Fatalf("must never emit WIPNOTE_AGENT_ID= with an empty value: %q", got)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_EmptyCommandNoRewrite(t *testing.T) {
	got, ok := rewriteWipnoteClaimCommandsForAgent("", "impl-graph")
	if ok || got != "" {
		t.Fatalf("expected no-op for empty command, got (%q, %v)", got, ok)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_NonClaimSubcommandNotRewritten(t *testing.T) {
	for _, cmd := range []string{
		"wipnote feature list",
		"wipnote feature show feat-1234",
		"wipnote status",
		"wipnote track list",
	} {
		got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
		if ok {
			t.Fatalf("expected no rewrite for %q, got %q", cmd, got)
		}
		if got != cmd {
			t.Fatalf("command must be returned unchanged for %q, got %q", cmd, got)
		}
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_PathToWipnoteBinary(t *testing.T) {
	cmd := "/usr/local/bin/wipnote feature start feat-1234"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
	if !ok {
		t.Fatalf("expected a rewrite for a path-qualified wipnote invocation")
	}
	want := "WIPNOTE_AGENT_ID=impl-graph /usr/local/bin/wipnote feature start feat-1234"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteWipnoteClaimCommandsForAgent_LeadingEnvVarPreserved(t *testing.T) {
	cmd := "FOO=bar wipnote feature start feat-1234"
	got, ok := rewriteWipnoteClaimCommandsForAgent(cmd, "impl-graph")
	if !ok {
		t.Fatalf("expected a rewrite")
	}
	want := "FOO=bar WIPNOTE_AGENT_ID=impl-graph wipnote feature start feat-1234"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestApplyClaimAgentPropagation_FullFieldPreservation asserts updatedInput
// carries every original tool_input field forward unchanged except command —
// updatedInput REPLACES the input rather than merging, so dropping a field
// here would silently change the tool call's behaviour (bug-190950e0 review
// note).
func TestApplyClaimAgentPropagation_FullFieldPreservation(t *testing.T) {
	event := &CloudEvent{
		ToolName: "Bash",
		AgentID:  "impl-graph",
		ToolInput: map[string]any{
			"command":           "wipnote feature start feat-1234",
			"description":       "Start the feature",
			"timeout":           float64(120000),
			"run_in_background": false,
		},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)

	if result.HookSpecificOutput == nil {
		t.Fatalf("expected HookSpecificOutput to be set")
	}
	if result.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("expected hookEventName PreToolUse, got %q", result.HookSpecificOutput.HookEventName)
	}
	updated := result.HookSpecificOutput.UpdatedInput
	if updated == nil {
		t.Fatalf("expected UpdatedInput to be set")
	}
	wantCommand := "WIPNOTE_AGENT_ID=impl-graph wipnote feature start feat-1234"
	if updated["command"] != wantCommand {
		t.Fatalf("command = %v, want %v", updated["command"], wantCommand)
	}
	if updated["description"] != "Start the feature" {
		t.Fatalf("description not preserved: %v", updated["description"])
	}
	if updated["timeout"] != float64(120000) {
		t.Fatalf("timeout not preserved: %v", updated["timeout"])
	}
	if updated["run_in_background"] != false {
		t.Fatalf("run_in_background not preserved: %v", updated["run_in_background"])
	}
	if len(updated) != len(event.ToolInput) {
		t.Fatalf("field count changed: got %d fields, want %d", len(updated), len(event.ToolInput))
	}
}

func TestApplyClaimAgentPropagation_EmptyAgentIDNoOp(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "Bash",
		AgentID:   "",
		ToolInput: map[string]any{"command": "wipnote feature start feat-1234"},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput != nil {
		t.Fatalf("expected no HookSpecificOutput when AgentID is empty (root session), got %+v", result.HookSpecificOutput)
	}
}

func TestApplyClaimAgentPropagation_NonBashToolNoOp(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "Edit",
		AgentID:   "impl-graph",
		ToolInput: map[string]any{"command": "wipnote feature start feat-1234"},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput != nil {
		t.Fatalf("expected no HookSpecificOutput for a non-Bash tool, got %+v", result.HookSpecificOutput)
	}
}

func TestApplyClaimAgentPropagation_NonMatchingCommandNoOp(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "Bash",
		AgentID:   "impl-graph",
		ToolInput: map[string]any{"command": "go test ./..."},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput != nil {
		t.Fatalf("expected no HookSpecificOutput for an unrelated command, got %+v", result.HookSpecificOutput)
	}
}

// TestApplyClaimAgentPropagation_ResolvesRealAgentIDNotRoot is the assertion
// this bug was missing: it doesn't just check the rewritten string looks
// right, it actually executes it through a shell (as Claude Code would) with
// a stub `wipnote` on PATH that reports what
// dbpkg.NormaliseAgentID(os.Getenv("WIPNOTE_AGENT_ID")) would resolve to, and
// asserts the result is the real agent id — not empty, and not the
// __root__ sentinel it collapses to today.
func TestApplyClaimAgentPropagation_ResolvesRealAgentIDNotRoot(t *testing.T) {
	binDir := t.TempDir()
	stub := "#!/bin/sh\nif [ -z \"$WIPNOTE_AGENT_ID\" ]; then echo __root__; else echo \"$WIPNOTE_AGENT_ID\"; fi\n"
	stubPath := filepath.Join(binDir, "wipnote")
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	event := &CloudEvent{
		ToolName: "Bash",
		AgentID:  "impl-graph",
		ToolInput: map[string]any{
			"command": "wipnote feature start feat-1234 && echo done",
		},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput == nil || result.HookSpecificOutput.UpdatedInput == nil {
		t.Fatalf("expected a rewrite for this command")
	}
	rewrittenCmd, _ := result.HookSpecificOutput.UpdatedInput["command"].(string)

	cmd := exec.Command("sh", "-c", rewrittenCmd)
	cmd.Env = []string{"PATH=" + binDir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running rewritten command failed: %v (output: %s)", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		t.Fatalf("no output from rewritten command")
	}
	resolved := lines[0]
	if resolved == "__root__" || resolved == "" {
		t.Fatalf("resolved agent id collapsed to %q, want the real agent id %q", resolved, event.AgentID)
	}
	if resolved != event.AgentID {
		t.Fatalf("resolved agent id = %q, want %q", resolved, event.AgentID)
	}
	if strings.TrimSpace(strings.Join(lines[1:], "\n")) != "done" {
		t.Fatalf("chained && command did not run: output = %q", out)
	}
}
