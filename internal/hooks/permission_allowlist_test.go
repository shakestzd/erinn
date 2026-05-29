package hooks

import "testing"

// --- feat-b396bd33: PermissionRequest auto-approve ---

// TestPermissionRequest_AllowlistedReadOnly_AutoAllows verifies that an
// allowlisted read-only wipnote query at low risk produces a
// hookSpecificOutput.decision.behavior == "allow".
func TestPermissionRequest_AllowlistedReadOnly_AutoAllows(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID:          sessionID,
		CWD:                t.TempDir(),
		ToolName:           "Bash",
		ToolInput:          map[string]any{"command": "wipnote status"},
		PermissionCategory: "execute",
		RiskLevel:          "low",
	}

	result, err := PermissionRequest(event, td.DB)
	if err != nil {
		t.Fatalf("PermissionRequest: %v", err)
	}
	if result == nil || result.HookSpecificOutput == nil || result.HookSpecificOutput.Decision == nil {
		t.Fatalf("expected hookSpecificOutput.decision to be set, got %+v", result)
	}
	if result.HookSpecificOutput.Decision.Behavior != "allow" {
		t.Errorf("behavior = %q, want allow", result.HookSpecificOutput.Decision.Behavior)
	}
	if result.HookSpecificOutput.HookEventName != "PermissionRequest" {
		t.Errorf("hookEventName = %q, want PermissionRequest", result.HookSpecificOutput.HookEventName)
	}

	// The checkpoint must still have been recorded.
	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND tool_name = 'PermissionRequest'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 PermissionRequest checkpoint, got %d", count)
	}
}

// TestPermissionRequest_WriteCommand_NoDecision verifies that a non-allowlisted
// / side-effecting command receives NO decision (CC prompts as usual) and is
// never auto-denied.
func TestPermissionRequest_WriteCommand_NoDecision(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "rm -rf /tmp/x"},
		RiskLevel: "low",
	}

	result, err := PermissionRequest(event, td.DB)
	if err != nil {
		t.Fatalf("PermissionRequest: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.HookSpecificOutput != nil {
		t.Errorf("expected no decision for write/arbitrary command, got %+v", result.HookSpecificOutput)
	}
}

// TestPermissionRequest_ArbitraryTool_NoDecision verifies that a non-Bash tool
// (e.g. Write) is never auto-allowed.
func TestPermissionRequest_ArbitraryTool_NoDecision(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		ToolName:  "Write",
		ToolInput: map[string]any{"file_path": "/tmp/x", "content": "y"},
		RiskLevel: "low",
	}

	result, err := PermissionRequest(event, td.DB)
	if err != nil {
		t.Fatalf("PermissionRequest: %v", err)
	}
	if result == nil || result.HookSpecificOutput != nil {
		t.Errorf("expected no decision for Write tool, got %+v", result)
	}
}

// TestPermissionRequest_HighRisk_VetoesAllow verifies belt-and-suspenders:
// even an allowlisted command is NOT auto-allowed when risk_level is not low.
func TestPermissionRequest_HighRisk_VetoesAllow(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "wipnote status"},
		RiskLevel: "high",
	}

	result, err := PermissionRequest(event, td.DB)
	if err != nil {
		t.Fatalf("PermissionRequest: %v", err)
	}
	if result == nil || result.HookSpecificOutput != nil {
		t.Errorf("expected no decision when risk is high, got %+v", result)
	}
}

// TestIsReadOnlyWipnoteCommand_Table covers the command-matcher edge cases.
func TestIsReadOnlyWipnoteCommand_Table(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"wipnote status", true},
		{"wipnote find foo", true},
		{"  wipnote   show feat-1  ", true},
		{"/home/u/.local/bin/wipnote snapshot --summary", true},
		{"wipnote start feat-1", false},          // write subcommand
		{"wipnote feature complete x", false},    // not in allowlist
		{"wipnote status; rm -rf /", false},      // chained
		{"wipnote status && git push", false},    // chained
		{"wipnote status | tee out", false},      // piped
		{"wipnote status > out.txt", false},      // redirect
		{"echo hi", false},                       // not wipnote
		{"wipnote", false},                       // no subcommand
		{"FOO=1 wipnote status", false},          // env prefix wrapper
		{"git wipnote status", false},            // not wipnote first
		{"", false},                              // empty
	}
	for _, c := range cases {
		if got := isReadOnlyWipnoteCommand(c.command); got != c.want {
			t.Errorf("isReadOnlyWipnoteCommand(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}
