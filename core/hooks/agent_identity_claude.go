package hooks

// claudeAgentIdentityAdapter implements AgentIdentityAdapter for Claude
// Code. This is the ONLY place in the package that is allowed to know
// Claude-specific facts about identity (event.AgentID, tool name "Bash",
// the updatedInput rewrite mechanism) — see agent_identity.go for the
// harness-neutral seam this plugs into, and pretooluse_agent_propagation.go
// for the anchored command-rewrite mechanics this delegates to.
type claudeAgentIdentityAdapter struct{}

// Identify claims every event stamped with HarnessClaude by
// ParseEventForHarness — the same detection runHookNamed already performs
// via DetectHarness, not a re-inference from event content.
func (claudeAgentIdentityAdapter) Identify(event *CloudEvent) bool {
	return event != nil && event.Harness == HarnessClaude
}

// ResolveAgentIdentity reads event.AgentID, the field Claude Code documents
// as present only inside a subagent call
// (https://code.claude.com/docs/en/hooks, "Common input fields": "agent_id:
// Unique identifier for the subagent. Present only when the hook fires
// inside a subagent call."). Its absence is therefore a positive, documented
// signal that this event fired at the root/orchestrator level — Claude Code
// DOES support per-subagent identity, it simply has none to report here —
// so this never returns AgentIdentityUnsupported.
func (claudeAgentIdentityAdapter) ResolveAgentIdentity(event *CloudEvent) (string, AgentIdentitySource) {
	if event.AgentID != "" {
		return event.AgentID, AgentIdentityFromHarness
	}
	return "", AgentIdentityRootSession
}

// RewriteCommandForAgent only applies to Bash — Claude Code's shell tool
// name (Codex uses "exec_command", Gemini "run_shell_command"; this adapter
// only ever sees Claude events, but the tool-name gate still belongs here,
// not in the shared caller, since "which tool is the shell tool" is itself
// harness-specific knowledge). Delegates the actual anchored rewrite to
// rewriteWipnoteClaimCommandsForAgent.
func (claudeAgentIdentityAdapter) RewriteCommandForAgent(event *CloudEvent, cmd, agentID string) (string, bool) {
	if event.ToolName != "Bash" {
		return "", false
	}
	return rewriteWipnoteClaimCommandsForAgent(cmd, agentID)
}
