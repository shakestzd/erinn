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

func TestCodexAgentIdentityAdapter_Identify(t *testing.T) {
	codex := codexAgentIdentityAdapter{}
	if !codex.Identify(&CloudEvent{Harness: HarnessCodex}) {
		t.Fatalf("expected the Codex adapter to identify a HarnessCodex event")
	}
	if codex.Identify(&CloudEvent{Harness: HarnessClaude}) {
		t.Fatalf("expected the Codex adapter to reject a HarnessClaude event")
	}
	if codex.Identify(nil) {
		t.Fatalf("expected the Codex adapter to reject a nil event")
	}
}

func TestCodexAgentIdentityAdapter_ResolveAgentIdentity(t *testing.T) {
	codex := codexAgentIdentityAdapter{}

	agentID, source := codex.ResolveAgentIdentity(&CloudEvent{AgentID: "codex-subagent-1"})
	if agentID != "codex-subagent-1" || source != AgentIdentityFromHarness {
		t.Fatalf("got (%q, %v), want (\"codex-subagent-1\", AgentIdentityFromHarness)", agentID, source)
	}

	// parseCodexEvent's root/orchestrator case: no real payload agent_id, so
	// AgentID carries the generic per-harness placeholder. Verified against
	// Codex's own Rust source (subagent: Option<SubagentHookContext>) and a
	// dedicated schema unit test that the root case has NO agent_id key at
	// all — the same "present only inside a subagent call" contract Claude
	// documents — so this must classify as AgentIdentityRootSession, not
	// AgentIdentityUnsupported.
	agentID, source = codex.ResolveAgentIdentity(&CloudEvent{AgentID: codexGenericAgentID})
	if agentID != "" || source != AgentIdentityRootSession {
		t.Fatalf("got (%q, %v), want (\"\", AgentIdentityRootSession)", agentID, source)
	}

	// Defensive: an entirely empty AgentID (shouldn't happen in practice —
	// parseCodexEvent always sets at least the generic placeholder — but the
	// adapter must not treat it as a real identity either) also classifies
	// as root, never as a real, empty-string identity.
	agentID, source = codex.ResolveAgentIdentity(&CloudEvent{AgentID: ""})
	if agentID != "" || source != AgentIdentityRootSession {
		t.Fatalf("got (%q, %v), want (\"\", AgentIdentityRootSession)", agentID, source)
	}
}

func TestCodexAgentIdentityAdapter_RewriteCommandForAgent_NonBashToolRejected(t *testing.T) {
	codex := codexAgentIdentityAdapter{}
	_, ok := codex.RewriteCommandForAgent(&CloudEvent{ToolName: "update_plan"}, "wipnote feature start feat-1234", "codex-subagent-1")
	if ok {
		t.Fatalf("expected no rewrite for a non-Bash tool")
	}
}

func TestAgentIdentityRegistry_ResolveReturnsNilForUnregisteredHarness(t *testing.T) {
	// This is the concrete proof that the seam is harness-neutral by
	// construction: a locally-scoped registry with only Claude registered
	// finds nothing for a Codex event, and the shared caller must not
	// invent a fallback identity of its own. (Production now registers a
	// real Codex adapter too — see
	// TestApplyClaimAgentPropagation_CodexHarnessResolvesRealAdapter and
	// TestApplyClaimAgentPropagation_AntigravityHarnessNoOp below for the
	// production-registry versions of "registered" and "not registered.")
	r := &AgentIdentityRegistry{}
	r.Register(claudeAgentIdentityAdapter{})

	if got := r.Resolve(&CloudEvent{Harness: HarnessCodex}); got != nil {
		t.Fatalf("expected nil adapter for an unregistered harness, got %T", got)
	}
	if got := r.Resolve(&CloudEvent{Harness: HarnessClaude}); got == nil {
		t.Fatalf("expected the Claude adapter to be resolved for a Claude event")
	}
}

// TestApplyClaimAgentPropagation_AntigravityHarnessNoOp asserts the
// harness-neutral call site does nothing for a harness with no registered
// adapter, using the SAME package-level defaultAgentIdentityRegistry
// applyClaimAgentPropagation actually consults (not a private stand-in), so
// this fails if a future default-registry change accidentally widens what
// gets treated as identifiable. Antigravity is the correct harness to use
// here — its own audit (bug-e8c481cb, blocked upstream) found no
// per-subagent identity in hooks, no input-rewrite mechanism, and no
// telemetry emission at all, so it must never be registered.
func TestApplyClaimAgentPropagation_AntigravityHarnessNoOp(t *testing.T) {
	event := &CloudEvent{
		Harness:   HarnessAntigravity,
		ToolName:  "run_command",
		AgentID:   "some-antigravity-subagent",
		ToolInput: map[string]any{"command": "wipnote feature start feat-1234"},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput != nil {
		t.Fatalf("expected no HookSpecificOutput for a harness with no registered adapter, got %+v", result.HookSpecificOutput)
	}
}

// TestApplyClaimAgentPropagation_CodexHarnessResolvesRealAdapter is the test
// of the seam feat-b7bc4267 asked for: adding Codex required registering
// codexAgentIdentityAdapter in newAgentIdentityRegistry and nothing else —
// this test exercises the real production defaultAgentIdentityRegistry
// end-to-end and would fail if that registration were ever missing or if
// applyClaimAgentPropagation had grown any Claude-specific assumption that
// silently excluded Codex.
func TestApplyClaimAgentPropagation_CodexHarnessResolvesRealAdapter(t *testing.T) {
	event := &CloudEvent{
		Harness:   HarnessCodex,
		ToolName:  "Bash", // verified: Codex's shell tool name is literally "Bash"
		AgentID:   "codex-subagent-1",
		ToolInput: map[string]any{"command": "wipnote feature start feat-1234"},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput == nil || result.HookSpecificOutput.UpdatedInput == nil {
		t.Fatalf("expected a rewrite for a Codex event carrying a real agent id")
	}
	want := "WIPNOTE_AGENT_ID=codex-subagent-1 wipnote feature start feat-1234"
	if got := result.HookSpecificOutput.UpdatedInput["command"]; got != want {
		t.Fatalf("command = %v, want %v", got, want)
	}
}

// TestApplyClaimAgentPropagation_CodexRootSessionNoRewrite asserts a Codex
// event carrying only the generic per-harness placeholder (no real
// per-subagent agent_id — parseCodexEvent's root/orchestrator case) does NOT
// get rewritten, exactly like Claude's own root case. If this ever rewrote,
// every orchestrator-level `wipnote feature start` on Codex would silently
// claim under the string "codex" instead of __root__ — a regression, not an
// improvement.
func TestApplyClaimAgentPropagation_CodexRootSessionNoRewrite(t *testing.T) {
	event := &CloudEvent{
		Harness:   HarnessCodex,
		ToolName:  "Bash",
		AgentID:   codexGenericAgentID,
		ToolInput: map[string]any{"command": "wipnote feature start feat-1234"},
	}
	result := &HookResult{}
	applyClaimAgentPropagation(event, result)
	if result.HookSpecificOutput != nil {
		t.Fatalf("expected no rewrite for Codex's root/orchestrator case, got %+v", result.HookSpecificOutput)
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
