package hooks

import "github.com/shakestzd/wipnote/core/harness"

// codexAgentIdentityAdapter implements AgentIdentityAdapter for the Codex
// CLI. This is the only place in the package that is allowed to know
// Codex-specific facts about identity — see agent_identity.go for the
// harness-neutral seam this plugs into.
//
// Audit basis (feat-b7bc4267, 2026-08-08): Codex declares agent_id/agent_type
// on PreToolUse, PostToolUse, PermissionRequest, SubagentStart, and
// SubagentStop, verified against Codex's own generated JSON Schema
// (codex-rs/hooks/schema/generated/*.command.input.schema.json,
// openai/codex @ main) — the schema is generated from the Rust wire types
// and is ground truth; the prose docs are wrong and list these fields as
// subagent-lifecycle-only. Codex also supports the same
// permissionDecision + updatedInput output shape PreToolUse rewrites
// through, and its shell tool is literally named "Bash", same as Claude —
// the existing rewrite mechanics in pretooluse_agent_propagation.go apply
// unchanged.
type codexAgentIdentityAdapter struct{}

// Identify claims every event stamped with HarnessCodex by
// ParseEventForHarness.
func (codexAgentIdentityAdapter) Identify(event *CloudEvent) bool {
	return event != nil && event.Harness == HarnessCodex
}

// ResolveAgentIdentity reads event.AgentID, which parseCodexEvent
// (harness.go) sets to either a real per-subagent identity from the payload,
// or the generic per-harness constant ("codex",
// harness.GetByHooksHarness(harness.HooksCodex).AgentID) when the payload
// carries none — mirroring the convention the rest of this package already
// uses to distinguish root from subagent (see isSubagentEvent,
// tooluse_shared.go). The generic constant is therefore treated as "no real
// identity," identically to how isSubagentEvent already treats it.
//
// Judgment call (explicitly asked for by review, since Claude's mapping
// must not be assumed to generalise): does Codex's absence of a real agent_id
// mean the same thing Claude's documented absence means — "this positively
// is the root/orchestrator" — or does it mean "this harness cannot tell us"?
// Verified against Codex's Rust source: PreToolUseRequest models this field
// as `subagent: Option<common::SubagentHookContext>`
// (codex-rs/hooks/src/events/pre_tool_use.rs), and
// codex-rs/hooks/src/schema.rs carries a dedicated unit test
// (test names reference "root_input") asserting the root/orchestrator case
// serializes with NO agent_id/agent_type keys in the JSON at all — the exact
// same "present only inside a subagent call" contract Claude Code documents,
// not a capability gap. So this maps to AgentIdentityRootSession, the same
// mapping Claude's adapter uses — reused here because Codex's own source
// confirms the semantics generalise, not because the Claude answer was
// assumed to.
func (codexAgentIdentityAdapter) ResolveAgentIdentity(event *CloudEvent) (string, AgentIdentitySource) {
	if event.AgentID != "" && event.AgentID != codexGenericAgentID {
		return event.AgentID, AgentIdentityFromHarness
	}
	return "", AgentIdentityRootSession
}

// RewriteCommandForAgent only applies to Bash — verified as Codex's own
// shell tool name (not assumed from Claude), per the audit. Delegates to the
// same anchored rewrite mechanics the Claude adapter uses; there is nothing
// Codex-specific about the rewrite itself once the tool-name gate passes.
func (codexAgentIdentityAdapter) RewriteCommandForAgent(event *CloudEvent, cmd, agentID string) (string, bool) {
	if event.ToolName != "Bash" {
		return "", false
	}
	return rewriteWipnoteClaimCommandsForAgent(cmd, agentID)
}

// codexGenericAgentID is the per-harness placeholder parseCodexEvent falls
// back to when a Codex payload carries no real per-subagent agent_id
// (harness.go). Resolved once via the harness registry rather than
// hardcoding the literal "codex" a second time in this file.
var codexGenericAgentID = harness.GetByHooksHarness(harness.HooksCodex).AgentID
