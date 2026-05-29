package hooks

import (
	"strings"
)

// permission_allowlist.go implements the CONSERVATIVE auto-approve policy for
// the PermissionRequest controlling hook (feat-b396bd33).
//
// SECURITY CONTRACT (read before editing):
//
//   - wipnote auto-ALLOWS only a tight, explicit, demonstrably read-only /
//     side-effect-free set of operations. The allowlist below is the entire
//     surface; anything not matched here gets NO decision so Claude Code falls
//     through to its normal interactive permission prompt.
//   - wipnote NEVER auto-DENIES. Denying could block legitimate work; the worst
//     a miss costs is one extra user prompt, which is the safe failure mode.
//   - Auto-allow is belt-and-suspenders: it fires only when BOTH (a) the harness
//     classifies the request as low risk (or supplies no risk signal at all) AND
//     (b) the concrete operation matches the read-only allowlist. A high/medium
//     risk_level vetoes auto-allow even for an allowlisted command.
//
// The allowlist deliberately covers only `wipnote` QUERY subcommands — commands
// that read wipnote state and have no side effects. Writes, arbitrary Bash,
// destructive operations, and any non-wipnote tool are intentionally excluded.

// readOnlyWipnoteSubcommands is the explicit, reviewable allowlist of `wipnote`
// subcommands that are demonstrably read-only / side-effect-free. Each entry is
// a query that inspects existing state and never mutates the canonical store,
// the index, git, or the filesystem.
//
// Keep this list TIGHT. Adding an entry is a security decision: only add a
// subcommand after confirming it cannot write, delete, spawn, or otherwise
// produce side effects. When in doubt, leave it off — the cost is one prompt.
var readOnlyWipnoteSubcommands = map[string]struct{}{
	"status":   {}, // prints current work-item / session status
	"find":     {}, // searches the index
	"snapshot": {}, // renders a read-only summary
	"lineage":  {}, // prints delegation lineage
	"trace":    {}, // prints a recorded trace
	"history":  {}, // prints event history
	"who":      {}, // prints attribution
	"wip":      {}, // prints work-in-progress
	"list":     {}, // lists work items
	"show":     {}, // shows a single item
}

// lowRiskPermissionLevels is the set of harness risk_level values under which
// auto-allow is permitted. An empty risk_level (harness supplied none) is also
// treated as eligible — the allowlist match itself is the primary gate, and
// requiring a populated low signal would defeat auto-allow on harnesses that
// omit the field. Any explicit medium/high value vetoes auto-allow.
var lowRiskPermissionLevels = map[string]struct{}{
	"":     {}, // no signal supplied — defer to allowlist match
	"low":  {},
	"none": {},
	"safe": {},
}

// isLowRiskPermission reports whether the harness-supplied risk_level permits
// auto-allow consideration. Comparison is case-insensitive.
func isLowRiskPermission(riskLevel string) bool {
	_, ok := lowRiskPermissionLevels[strings.ToLower(strings.TrimSpace(riskLevel))]
	return ok
}

// permissionAutoAllow returns true when the PermissionRequest event describes a
// demonstrably read-only operation that is safe to approve without a user
// prompt. It returns false for everything else — including any case of doubt —
// so the caller emits no decision and CC prompts as normal.
//
// Belt-and-suspenders: both the risk signal AND the concrete-operation match
// must pass. The function NEVER signals "deny"; a false return means "no
// opinion", not "block".
func permissionAutoAllow(event *CloudEvent) bool {
	if event == nil {
		return false
	}
	// Risk gate: an explicit non-low risk level vetoes auto-allow even if the
	// command would otherwise match the allowlist.
	if !isLowRiskPermission(event.RiskLevel) {
		return false
	}
	// Operation gate: only Bash invocations of allowlisted read-only `wipnote`
	// query subcommands qualify. Every other tool (Write, Edit, MCP tools,
	// arbitrary Bash, etc.) falls through to a normal prompt.
	if event.ToolName != "Bash" {
		return false
	}
	command, _ := event.ToolInput["command"].(string)
	return isReadOnlyWipnoteCommand(command)
}

// isReadOnlyWipnoteCommand reports whether a Bash command string is a single
// invocation of a read-only `wipnote` query subcommand on the allowlist.
//
// It is intentionally STRICT: the command must be exactly `wipnote <subcommand>
// [args…]` with no shell metacharacters that could chain, redirect, or escape
// into a side-effecting command. A command containing ';', '&&', '||', '|',
// '>', '<', '`', or '$(' is rejected outright — even if it starts with an
// allowlisted wipnote query — because the trailing portion could do anything.
func isReadOnlyWipnoteCommand(command string) bool {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}
	// Reject any shell metacharacter that could chain or redirect into a
	// side-effecting command. This keeps the matched surface to a single,
	// reviewable wipnote query invocation.
	for _, meta := range []string{";", "&&", "||", "|", ">", "<", "`", "$(", "\n", "&"} {
		if strings.Contains(cmd, meta) {
			return false
		}
	}
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return false
	}
	// First token must be the wipnote binary (bare or absolute path), with no
	// leading env-var assignments or wrappers.
	bin := fields[0]
	if bin != "wipnote" && !strings.HasSuffix(bin, "/wipnote") {
		return false
	}
	subcommand := fields[1]
	_, ok := readOnlyWipnoteSubcommands[subcommand]
	return ok
}
