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
