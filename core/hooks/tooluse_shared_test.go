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
