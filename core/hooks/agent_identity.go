package hooks

// This file is the harness-neutral seam for bug-190950e0. Identity
// acquisition (how a harness tells wipnote which subagent is calling) is
// necessarily harness-specific — Claude Code exposes event.AgentID on hook
// payloads and a command-rewrite mechanism to seed it into a spawned
// process's environment; other harnesses may expose something entirely
// different, or nothing at all. Everything in THIS file must stay ignorant
// of which harness is in play — it only ever talks to AgentIdentityAdapter,
// mirroring the shape observe/otel/adapter/adapter.go already uses to keep
// per-harness OTel translation out of the shared receiver path.

// AgentIdentitySource classifies WHY ResolveAgentIdentity did or did not
// return an agent id. Collapsing "no identity" into a single case is exactly
// what let bug-190950e0 go unnoticed: every claim silently fell back to
// __root__ whether or not there was ever a real subagent identity to have.
// A downstream consumer (the claim ledger, feat-21d12cdb) needs to tell
// "this genuinely is the root/orchestrator" apart from "this harness has no
// way to say" — those are different facts about the world and must not look
// identical in the data.
type AgentIdentitySource int

const (
	// AgentIdentityFromHarness: the harness supplied a real, distinguishing
	// agent id for this event.
	AgentIdentityFromHarness AgentIdentitySource = iota
	// AgentIdentityRootSession: the harness DOES support per-subagent
	// identity, and positively reports that this event is not a subagent
	// call. For Claude Code this is documented behavior (agent_id is
	// "present only when the hook fires inside a subagent call" —
	// https://code.claude.com/docs/en/hooks), so its absence is a meaningful
	// positive signal, not a gap.
	AgentIdentityRootSession
	// AgentIdentityUnsupported: this harness has no mechanism to report
	// per-subagent identity (yet, or at all) — the absence of an agent id
	// here is a capability gap, not a claim about who ran this event.
	AgentIdentityUnsupported
)

// AgentIdentityAdapter resolves per-harness agent identity and, where the
// harness supports it, rewrites a shell command to carry that identity into
// a spawned process's environment. Register one implementation per harness;
// see agent_identity_claude.go for the Claude Code adapter.
type AgentIdentityAdapter interface {
	// Identify reports whether this adapter owns event (i.e. event came from
	// this adapter's harness).
	Identify(event *CloudEvent) bool

	// ResolveAgentIdentity returns the agent id this harness supplies for
	// event, and classifies why one is or is not present. Implementations
	// must never guess: an empty agentID must always be paired with
	// AgentIdentityRootSession or AgentIdentityUnsupported, never silently
	// treated as interchangeable with a real id.
	ResolveAgentIdentity(event *CloudEvent) (agentID string, source AgentIdentitySource)

	// RewriteCommandForAgent returns cmd rewritten to carry agentID via
	// whatever mechanism this harness supports for seeding a spawned
	// process's environment (Claude Code: PreToolUse's updatedInput), and
	// whether a rewrite was produced. Harnesses without such a mechanism, or
	// tool calls the mechanism doesn't apply to (e.g. a non-shell tool),
	// must always return ("", false) — never a partial or best-guess rewrite.
	RewriteCommandForAgent(event *CloudEvent, cmd, agentID string) (string, bool)
}

// AgentIdentityRegistry owns adapter lookup, mirroring
// observe/otel/adapter.Registry: adapters are registered up front, and
// Resolve returns the first one whose Identify claims the event.
type AgentIdentityRegistry struct {
	adapters []AgentIdentityAdapter
}

// Register adds an adapter. Registration order is the Identify probe order.
func (r *AgentIdentityRegistry) Register(a AgentIdentityAdapter) {
	r.adapters = append(r.adapters, a)
}

// Resolve returns the adapter that owns event, or nil if none claim it.
// nil is the correct, honest result for a harness with no registered
// adapter — callers must treat it as "nothing to do here" and must NOT
// synthesize a fallback identity of their own.
func (r *AgentIdentityRegistry) Resolve(event *CloudEvent) AgentIdentityAdapter {
	for _, a := range r.adapters {
		if a.Identify(event) {
			return a
		}
	}
	return nil
}

// defaultAgentIdentityRegistry is the process-wide registry consulted by
// applyClaimAgentPropagation. Hook handlers are one-shot CLI invocations
// (a fresh process per hook call), so a package-level registry built once
// at init is the equivalent of the OTel receiver's per-request Registry
// construction — there is no concurrent-request lifetime to manage here.
//
// Claude Code and Codex are registered (feat-b7bc4267's audit found Codex
// has the same shape: agent_id/agent_type on the same event set, the same
// updatedInput mechanism, the same "Bash" shell tool name). Adding Codex
// required registering codexAgentIdentityAdapter here and nothing else in
// this package — pretooluse.go was not touched, confirming the seam holds.
//
// Antigravity is deliberately NOT registered: its own audit
// (bug-e8c481cb, blocked upstream) found no per-subagent identity in hooks,
// no input-rewrite mechanism, and no telemetry emission at all — there is
// nothing to adapt, and a mechanism that would silently no-op is worse than
// no mechanism. An Antigravity event simply finds no adapter here and
// applyClaimAgentPropagation is a no-op for it, which is the correct,
// harness-neutral behavior for "nothing to plug in," not a stopgap.
var defaultAgentIdentityRegistry = newAgentIdentityRegistry()

func newAgentIdentityRegistry() *AgentIdentityRegistry {
	r := &AgentIdentityRegistry{}
	r.Register(claudeAgentIdentityAdapter{})
	r.Register(codexAgentIdentityAdapter{})
	return r
}

// applyClaimAgentPropagation attaches an UpdatedInput rewrite to result when
// the harness that produced event can supply a real per-subagent identity
// and has a mechanism for propagating it into a spawned command's
// environment. Entirely harness-neutral: it never inspects event.Harness,
// event.ToolName, or any other harness-specific field itself — every such
// decision belongs to the resolved AgentIdentityAdapter.
//
// Callers MUST only invoke this from a path that has already decided to
// allow the tool call — see recordEventAndAllow, the sole call site.
func applyClaimAgentPropagation(event *CloudEvent, result *HookResult) {
	if result == nil || event == nil {
		return
	}
	adapter := defaultAgentIdentityRegistry.Resolve(event)
	if adapter == nil {
		return // no adapter for this harness — nothing to do
	}
	agentID, source := adapter.ResolveAgentIdentity(event)
	if source != AgentIdentityFromHarness || agentID == "" {
		// Root session, or this harness can't tell us — never rewrite, and
		// never invent an identity of our own. Downstream keeps resolving
		// to __root__ exactly as it does today; this file adds no new claim
		// there, it only stops MISattributing when a real identity exists.
		return
	}

	cmd := shellCommand(event.ToolInput)
	if cmd == "" {
		return
	}
	rewritten, ok := adapter.RewriteCommandForAgent(event, cmd, agentID)
	if !ok {
		return
	}

	// updatedInput (Claude's mechanism, and presumptively any future
	// harness's equivalent) REPLACES the tool's entire input rather than
	// merging with it, so every existing field must be carried forward
	// unchanged; only "command" changes.
	updated := make(map[string]any, len(event.ToolInput))
	for k, v := range event.ToolInput {
		updated[k] = v
	}
	updated["command"] = rewritten

	if result.HookSpecificOutput == nil {
		result.HookSpecificOutput = &HookSpecificOutput{}
	}
	result.HookSpecificOutput.HookEventName = "PreToolUse"
	result.HookSpecificOutput.UpdatedInput = updated
}
