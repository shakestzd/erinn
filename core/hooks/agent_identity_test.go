package hooks

import "testing"

func TestClaudeAgentIdentityAdapter_Identify(t *testing.T) {
	claude := claudeAgentIdentityAdapter{}
	if !claude.Identify(&CloudEvent{Harness: HarnessClaude}) {
		t.Fatalf("expected the Claude adapter to identify a HarnessClaude event")
	}
	if claude.Identify(&CloudEvent{Harness: HarnessCodex}) {
		t.Fatalf("expected the Claude adapter to reject a HarnessCodex event")
	}
	if claude.Identify(nil) {
		t.Fatalf("expected the Claude adapter to reject a nil event")
	}
}

func TestClaudeAgentIdentityAdapter_ResolveAgentIdentity(t *testing.T) {
	claude := claudeAgentIdentityAdapter{}

	agentID, source := claude.ResolveAgentIdentity(&CloudEvent{AgentID: "impl-graph"})
	if agentID != "impl-graph" || source != AgentIdentityFromHarness {
		t.Fatalf("got (%q, %v), want (\"impl-graph\", AgentIdentityFromHarness)", agentID, source)
	}

	// Claude Code documents agent_id as present only inside a subagent call —
	// its absence must classify as a positive "this is the root session"
	// signal, never as AgentIdentityUnsupported (Claude DOES support
	// per-subagent identity, it simply has none to report at the root).
	agentID, source = claude.ResolveAgentIdentity(&CloudEvent{AgentID: ""})
	if agentID != "" || source != AgentIdentityRootSession {
		t.Fatalf("got (%q, %v), want (\"\", AgentIdentityRootSession)", agentID, source)
	}
}

func TestClaudeAgentIdentityAdapter_RewriteCommandForAgent_NonBashToolRejected(t *testing.T) {
	claude := claudeAgentIdentityAdapter{}
	_, ok := claude.RewriteCommandForAgent(&CloudEvent{ToolName: "Edit"}, "wipnote feature start feat-1234", "impl-graph")
	if ok {
		t.Fatalf("expected no rewrite for a non-Bash tool")
	}
}

func TestAgentIdentityRegistry_ResolveReturnsNilForUnregisteredHarness(t *testing.T) {
	// This is the concrete proof that the seam is harness-neutral by
	// construction: an event from a harness with no registered adapter
	// (Codex/Antigravity, pending their own audit) finds nothing here, and
	// the shared caller must not invent a fallback identity of its own.
	r := &AgentIdentityRegistry{}
	r.Register(claudeAgentIdentityAdapter{})

	if got := r.Resolve(&CloudEvent{Harness: HarnessCodex}); got != nil {
		t.Fatalf("expected nil adapter for an unregistered harness, got %T", got)
	}
	if got := r.Resolve(&CloudEvent{Harness: HarnessClaude}); got == nil {
		t.Fatalf("expected the Claude adapter to be resolved for a Claude event")
	}
}

// TestApplyClaimAgentPropagation_UnregisteredHarnessNoOp asserts the
// harness-neutral call site does nothing for a harness with no registered
// adapter, using the SAME package-level defaultAgentIdentityRegistry
// applyClaimAgentPropagation actually consults (not a private stand-in),
// so this fails if a future default-registry change accidentally widens
// what gets treated as Claude.
func TestApplyClaimAgentPropagation_UnregisteredHarnessNoOp(t *testing.T) {
	event := &CloudEvent{
		Harness:   HarnessCodex,
		ToolName:  "exec_command",
		AgentID:   "some-codex-subagent",
		ToolInput: map[string]any{"command": "wipnote feature start feat-1234"},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput != nil {
		t.Fatalf("expected no HookSpecificOutput for a harness with no registered adapter, got %+v", result.HookSpecificOutput)
	}
}

// TestApplyClaimAgentPropagation_ClaudeHarnessStampedByDefault asserts a
// CloudEvent that never explicitly sets Harness (the zero value) still
// resolves through the Claude adapter — this is the real-world shape:
// ParseEventForHarness stamps Harness itself; only tests and any code that
// hand-builds a CloudEvent skip that step, and the zero value must land on
// the historically-default harness, not silently on "no adapter."
func TestApplyClaimAgentPropagation_ClaudeHarnessStampedByDefault(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "Bash",
		AgentID:   "impl-graph",
		ToolInput: map[string]any{"command": "wipnote feature start feat-1234"},
	}
	if event.Harness != HarnessClaude {
		t.Fatalf("zero-value Harness must equal HarnessClaude, got %v", event.Harness)
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput == nil || result.HookSpecificOutput.UpdatedInput == nil {
		t.Fatalf("expected a rewrite for a zero-value-Harness (i.e. Claude) event")
	}
}
