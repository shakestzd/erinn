package hooks

import "testing"

func TestIsSubagentEvent_GenericHarnessIDs(t *testing.T) {
	for _, agentID := range []string{"", "claude-code", "claude", "codex", "gemini", "antigravity"} {
		t.Run(agentID, func(t *testing.T) {
			if isSubagentEvent(&CloudEvent{AgentID: agentID}) {
				t.Fatalf("isSubagentEvent(%q) = true, want false", agentID)
			}
		})
	}

	if !isSubagentEvent(&CloudEvent{AgentID: "feature-coder"}) {
		t.Fatal("isSubagentEvent(\"feature-coder\") = false, want true")
	}
}

// TestIsSubagentEvent_CodexRealSubagentIdentity is bug-fa036758's sibling
// regression test for the subagent-detection side. Before feat-b7bc4267,
// every Codex event's AgentID was hardcoded to the literal "codex" and this
// always returned false — no Codex event was ever detected as a subagent.
// A real Codex subagent identity (a thread ID, per the live capture in
// feat-b7bc4267's research) must now be detected as a subagent with no
// change to this function itself — the fix was entirely upstream, in
// parseCodexEvent no longer discarding the payload's real agent_id.
func TestIsSubagentEvent_CodexRealSubagentIdentity(t *testing.T) {
	realCodexSubagentID := "019fe188-03aa-7b92-83dc-0b4dc62e0014" // shape captured live against codex-cli 0.147.0
	if !isSubagentEvent(&CloudEvent{Harness: HarnessCodex, AgentID: realCodexSubagentID}) {
		t.Fatalf("isSubagentEvent(%q) = false, want true (a real Codex subagent identity must be detected as a subagent)", realCodexSubagentID)
	}
	// The root/orchestrator case must remain unaffected: parseCodexEvent's
	// generic fallback constant is still correctly treated as non-subagent.
	if isSubagentEvent(&CloudEvent{Harness: HarnessCodex, AgentID: codexGenericAgentID}) {
		t.Fatalf("isSubagentEvent(%q) = true, want false (Codex's root/orchestrator case must not be misdetected as a subagent)", codexGenericAgentID)
	}
}

// TestResolveFeatureIDForContext exercises the priority logic in isolation
// (bug-b65b82bd): subagents must prefer their own claim over the session-wide
// legacy column, falling back to it only when they have no claim of their
// own; root/orchestrator resolution (isSubagent=false) must be unchanged.
func TestResolveFeatureIDForContext(t *testing.T) {
	cases := []struct {
		name            string
		isSubagent      bool
		activeFeatureID string
		claimedItem     string
		want            string
	}{
		{"root prefers session-wide value", false, "feat-root", "feat-claim", "feat-root"},
		{"root falls back to claim when session-wide empty", false, "", "feat-claim", "feat-claim"},
		{"root with neither returns empty", false, "", "", ""},
		{"subagent prefers own claim over session-wide value", true, "feat-root", "feat-claim", "feat-claim"},
		{"subagent falls back to session-wide value when it has no claim", true, "feat-root", "", "feat-root"},
		{"subagent with neither returns empty", true, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveFeatureIDForContext(tc.isSubagent, tc.activeFeatureID, tc.claimedItem)
			if got != tc.want {
				t.Errorf("resolveFeatureIDForContext(%v, %q, %q) = %q, want %q",
					tc.isSubagent, tc.activeFeatureID, tc.claimedItem, got, tc.want)
			}
		})
	}
}
