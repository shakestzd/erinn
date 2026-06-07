package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// agentBranchPrefixes are branch name prefixes that indicate an intentional
// per-agent branch. Sub-agents on these branches are allowed to commit even if
// the branch is not main/master.
var agentBranchPrefixes = []string{"worktree-agent-", "subagent-"}

// checkSubagentCommitGuard blocks git commit on main/master when the current
// session is a sub-agent. Orchestrators (sessions without a parent) are always
// allowed — they may need to commit to main intentionally.
//
// Escape hatches (all allow):
//   - Session has no parent → orchestrator → allow.
//   - Branch matches worktree-agent-* or subagent-* prefix → per-agent branch → allow.
//   - WIPNOTE_AGENT_BRANCH=1 env var is set → explicitly authorised → allow.
//   - Branch is not main or master → allow.
func checkSubagentCommitGuard(event *CloudEvent, parentSessionID string, projectDir string) string {
	if event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if !gitCommitPattern.MatchString(cmd) {
		return ""
	}

	// Only applies to sub-agents: sessions with a parent.
	if parentSessionID == "" {
		return "" // orchestrator — allow
	}

	// Escape hatch: explicit per-agent branch env var.
	if agentBranchEnvSet() {
		return ""
	}

	// Resolve the current branch from the project directory.
	branch := resolveCommitBranch(event, projectDir)

	// Not on a protected branch — allow.
	if branch != "main" && branch != "master" {
		return ""
	}

	// Escape hatch: branch name matches a per-agent prefix.
	for _, prefix := range agentBranchPrefixes {
		if strings.HasPrefix(branch, prefix) {
			return ""
		}
	}

	sessionShort := event.SessionID
	if len(sessionShort) > 8 {
		sessionShort = sessionShort[:8]
	}
	parentShort := parentSessionID
	if len(parentShort) > 8 {
		parentShort = parentShort[:8]
	}

	return fmt.Sprintf(
		"Sub-agent git commit blocked on protected branch.\n"+
			"  session=%s  branch=%s  parent=%s\n"+
			"Sub-agents must not commit directly to main/master. "+
			"Remediation: create a per-agent branch first:\n"+
			"  git checkout -b worktree-agent-<name>\n"+
			"Then commit and merge to the track branch via the orchestrator.",
		sessionShort, branch, parentShort,
	)
}

// resolveCommitBranch returns the current git branch for the event. It first
// tries event.CWD (the Bash tool's working directory), falling back to
// projectDir. Returns "" when git is unavailable or the branch cannot be read.
func resolveCommitBranch(event *CloudEvent, projectDir string) string {
	dir := event.CWD
	if dir == "" {
		dir = projectDir
	}
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// agentBranchEnvSet returns true when WIPNOTE_AGENT_BRANCH=1 is set in the
// current process environment. This allows dispatch tooling to explicitly mark
// a session as running on an intentional per-agent branch without requiring a
// specific branch-name prefix.
func agentBranchEnvSet() bool {
	return strings.TrimSpace(os.Getenv("WIPNOTE_AGENT_BRANCH")) == "1"
}
