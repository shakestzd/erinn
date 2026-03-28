package hooks

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// isYoloMode checks if the current session is in YOLO mode by reading
// the .htmlgraph/.launch-mode file.
func isYoloMode(htmlgraphDir string) bool {
	data, err := os.ReadFile(filepath.Join(htmlgraphDir, ".launch-mode"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), `"yolo`)
}

// checkYoloWorkItemGuard blocks Write/Edit tools when no active feature
// exists in YOLO mode. Returns a non-empty reason to block, or "" to allow.
func checkYoloWorkItemGuard(toolName, featureID string, yolo bool) string {
	if !yolo {
		return ""
	}
	switch toolName {
	case "Write", "Edit", "MultiEdit":
	default:
		return ""
	}
	if featureID != "" {
		return ""
	}
	return "YOLO mode requires an active work item before writing code. " +
		"Create or start a feature first: htmlgraph feature create \"title\""
}

// gitCommitPattern matches git commit commands in Bash.
var gitCommitPattern = regexp.MustCompile(`\bgit\s+commit\b`)

// checkYoloCommitGuard blocks git commit when tests haven't run in
// the current session. Returns a non-empty reason to block, or "" to allow.
func checkYoloCommitGuard(event *CloudEvent, yolo, testRan bool) string {
	if !yolo {
		return ""
	}
	if event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if !gitCommitPattern.MatchString(cmd) {
		return ""
	}
	if testRan {
		return ""
	}
	return "YOLO mode requires tests to pass before committing. " +
		"Run: go test ./... or uv run pytest"
}

// checkYoloBudgetGuard blocks git commit when the staged diff exceeds
// YOLO hard limits (20 files or 600 lines added).
func checkYoloBudgetGuard(event *CloudEvent, yolo bool) string {
	if !yolo || event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if !gitCommitPattern.MatchString(cmd) {
		return ""
	}
	out, err := exec.Command("git", "diff", "--cached", "--numstat").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var fileCount, totalAdded int
	for _, line := range lines {
		if line == "" {
			continue
		}
		fileCount++
		parts := strings.Fields(line)
		if len(parts) >= 1 && parts[0] != "-" {
			n, _ := strconv.Atoi(parts[0])
			totalAdded += n
		}
	}
	if fileCount > 20 || totalAdded > 600 {
		return fmt.Sprintf(
			"YOLO budget HARD LIMIT: %d files, %d lines (max 20/600). "+
				"Split into sub-features.", fileCount, totalAdded)
	}
	return ""
}

// checkYoloWorktreeGuard blocks Write/Edit on main/master branch in YOLO mode.
func checkYoloWorktreeGuard(toolName, branch string, yolo bool) string {
	if !yolo {
		return ""
	}
	switch toolName {
	case "Write", "Edit", "MultiEdit":
	default:
		return ""
	}
	if branch == "main" || branch == "master" {
		return "YOLO mode requires a feature branch or worktree. " +
			"Create one: git worktree add -b feat-xxx .claude/worktrees/xxx main"
	}
	return ""
}

// checkYoloResearchGuard blocks Write/Edit when no Read/Grep/Glob has
// occurred in the session (research-first principle).
func checkYoloResearchGuard(toolName string, yolo, hasResearch bool) string {
	if !yolo {
		return ""
	}
	switch toolName {
	case "Write", "Edit", "MultiEdit":
	default:
		return ""
	}
	if hasResearch {
		return ""
	}
	return "YOLO mode requires research before writing code. " +
		"Read existing code first: use Read, Grep, or Glob tools."
}

// checkYoloDiffReviewGuard blocks git commit when no git diff has been
// reviewed in this session.
func checkYoloDiffReviewGuard(event *CloudEvent, yolo, diffRan bool) string {
	if !yolo || event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if !gitCommitPattern.MatchString(cmd) {
		return ""
	}
	if diffRan {
		return ""
	}
	return "YOLO mode requires a diff review before committing. " +
		"Run: git diff --stat"
}

// checkYoloCodeHealthGuard blocks writes that would create oversized
// Go/Python files (>500 lines) in YOLO mode.
func checkYoloCodeHealthGuard(event *CloudEvent, yolo bool) string {
	if !yolo {
		return ""
	}
	switch event.ToolName {
	case "Write", "Edit", "MultiEdit":
	default:
		return ""
	}
	path, _ := event.ToolInput["file_path"].(string)
	if path == "" {
		path, _ = event.ToolInput["path"].(string)
	}
	if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".py") {
		return ""
	}
	// Check existing file size — if it's already >500 lines, warn
	data, err := os.ReadFile(path)
	if err != nil {
		return "" // new file, allow
	}
	lines := strings.Count(string(data), "\n")
	if lines > 500 {
		return fmt.Sprintf(
			"YOLO code health: %s has %d lines (limit 500). "+
				"Refactor into smaller modules.", filepath.Base(path), lines)
	}
	return ""
}

// hasRecentResearch checks if Read/Grep/Glob was used in this session.
func hasRecentResearch(database *sql.DB, sessionID string) bool {
	var count int
	database.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		WHERE session_id = ? AND tool_name IN ('Read', 'Grep', 'Glob', 'Agent')
		LIMIT 1`,
		sessionID,
	).Scan(&count)
	return count > 0
}

// hasRecentDiffReview checks if git diff was run in this session.
func hasRecentDiffReview(database *sql.DB, sessionID string) bool {
	var count int
	database.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		WHERE session_id = ? AND tool_name = 'Bash'
		  AND (input_summary LIKE '%git diff%'
		    OR input_summary LIKE '%git show%')`,
		sessionID,
	).Scan(&count)
	return count > 0
}

// currentBranchIn returns the git branch for the repository containing dir.
// Using "git -C dir" ensures the branch is resolved from the correct worktree
// rather than from the hook binary's working directory.
func currentBranchIn(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// testPattern matches common test runner commands in Bash input summaries.
var testPattern = regexp.MustCompile(`\bgo test\b|\bpytest\b|\buv run pytest\b|\buv run ruff\b`)

// hasRecentTestRun checks if a test command was executed in this session
// by scanning recent agent_events for Bash commands matching test patterns.
func hasRecentTestRun(database *sql.DB, sessionID string) bool {
	var count int
	database.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		WHERE session_id = ? AND tool_name = 'Bash'
		  AND status = 'completed'
		  AND input_summary REGEXP ?`,
		sessionID, `(go test|pytest|uv run ruff)`,
	).Scan(&count)
	if count > 0 {
		return true
	}
	// Fallback: LIKE-based check for SQLite without REGEXP
	database.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		WHERE session_id = ? AND tool_name = 'Bash'
		  AND (input_summary LIKE '%go test%'
		    OR input_summary LIKE '%pytest%'
		    OR input_summary LIKE '%uv run ruff%')`,
		sessionID,
	).Scan(&count)
	return count > 0
}
