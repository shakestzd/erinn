package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/paths"
	"github.com/shakestzd/htmlgraph/internal/storage"
)

// mergeInProgressFn is injected for testing. In production, it checks the real
// git state. In tests, it can be overridden to return false to avoid git state
// bleeding into test isolation.
var mergeInProgressFn = isMergeInProgress

// isYoloFromEvent checks the CloudEvent permission_mode field first (live
// state from Claude Code), falling back to a SQLite DB lookup.
func isYoloFromEvent(event *CloudEvent, htmlgraphDir string) bool {
	if event.PermissionMode == "bypassPermissions" {
		return true
	}
	// If Claude Code reports a non-bypass mode, trust it.
	if event.PermissionMode != "" {
		return false
	}
	// Fallback: check DB for session's last known permission_mode.
	// This is populated by the ConfigChange hook handler.
	return isYoloFromDB(htmlgraphDir, event.SessionID)
}

// isYoloFromDB looks up the session's permission_mode from the sessions.metadata
// JSON column. This is populated by the ConfigChange hook when the user toggles
// permission mode in Claude Code.
func isYoloFromDB(htmlgraphDir, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	projectDir := filepath.Dir(htmlgraphDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return false
	}
	// Use lightweight read-only open — no pragmas, no migrations.
	database, err := sql.Open("sqlite", dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return false
	}
	defer database.Close()
	var mode sql.NullString
	err = database.QueryRow(
		"SELECT json_extract(metadata, '$.permission_mode') FROM sessions WHERE session_id = ?",
		sessionID,
	).Scan(&mode)
	if err != nil || !mode.Valid {
		return false
	}
	return mode.String == "bypassPermissions"
}

// checkYoloWorkItemGuard blocks Write/Edit tools when no active work item
// exists. Always enforced (was YOLO-only, now universal).
//
// featureID is the session's active_feature_id column (set at session-start
// or inherited from a parent session via lineage).
// sessionID is used for the fallback check: when featureID is empty, we check
// whether a feature was started mid-session and linked to THIS session — not
// whether any feature is globally in-progress (which causes false passes when
// unrelated features exist).
func checkYoloWorkItemGuard(toolName, featureID string, _ bool, sessionID string, db *sql.DB) string {
	switch toolName {
	case "Write", "Edit", "MultiEdit":
	default:
		return ""
	}
	if featureID != "" {
		return ""
	}
	// Fallback: check if a feature was started mid-session and linked to this
	// session via the sessions table or a recent feature start command.
	if sessionID != "" && db != nil && sessionHasLinkedFeature(db, sessionID) {
		return ""
	}
	// Secondary fallback: when CLAUDE_ENV_FILE is unset (common in YOLO mode),
	// HTMLGRAPH_SESSION_ID is never exported, so `bug start` writes
	// active_feature_id to a different session row than the one the hook sees.
	// Allow the edit when any work item is in-progress to prevent false blocks.
	if hasAnyActiveWorkItem(db) {
		return ""
	}
	return "An active work item is required before writing code. " +
		"Run: htmlgraph feature start <id>  or  htmlgraph feature create \"title\" --track <trk-id>"
}

// yoloSubagentGracePeriod is the window after session start during which a
// subagent is allowed to write files before claiming a work item. This gives
// the subagent time to run `htmlgraph feature start <id>` as its first action.
const yoloSubagentGracePeriod = 30 * time.Second

// checkYoloSubagentGrace returns true when the session qualifies for the
// subagent grace period: it must be a subagent (nesting_depth > 0 per
// is_subagent flag), the session must be younger than yoloSubagentGracePeriod,
// and the parent session must have an active feature. When these conditions
// hold the caller should allow the write with a warning instead of blocking.
func checkYoloSubagentGrace(yolo, isSubagent bool, sessionCreatedAt time.Time, parentSessionID string, database *sql.DB) bool {
	if !yolo || !isSubagent {
		return false
	}
	if time.Since(sessionCreatedAt) >= yoloSubagentGracePeriod {
		return false
	}
	if parentSessionID == "" || database == nil {
		return false
	}
	return db.GetActiveFeatureIDForSession(database, parentSessionID) != ""
}

// checkYoloBashWorkItemGuard extends the work-item guard to Bash file-write
// commands (sed -i, rm, redirects, etc.). Always enforced (was YOLO-only, now universal).
// htmlgraph CLI commands are always exempt — they are the approved write path.
func checkYoloBashWorkItemGuard(event *CloudEvent, featureID string, _ bool, sessionID string, database *sql.DB) string {
	cmd, _ := event.ToolInput["command"].(string)
	if bashHtmlGraphCLI.MatchString(cmd) {
		return ""
	}
	if !isBashFileWrite(event) {
		return ""
	}
	if featureID != "" {
		return ""
	}
	if sessionID != "" && database != nil && sessionHasLinkedFeature(database, sessionID) {
		return ""
	}
	// Secondary fallback: when CLAUDE_ENV_FILE is unset (common in YOLO mode),
	// HTMLGRAPH_SESSION_ID is never exported, so `bug start` writes
	// active_feature_id to a different session row than the one the hook sees.
	// Allow the edit when any work item is in-progress to prevent false blocks.
	if hasAnyActiveWorkItem(database) {
		return ""
	}
	return "An active work item is required before writing code via Bash. " +
		"Run: htmlgraph feature start <id>  or  htmlgraph feature create \"title\" --track <trk-id>"
}

// sessionHasLinkedFeature returns true when the given session has a feature
// linked via sessions.active_feature_id OR when a recent feature-start command
// updated the session's feature association. This replaces the old global
// hasAnyInProgressWorkItem check which false-passed when unrelated features
// were in-progress elsewhere in the project.
func sessionHasLinkedFeature(db *sql.DB, sessionID string) bool {
	var featureID sql.NullString
	db.QueryRow(
		`SELECT active_feature_id FROM sessions WHERE session_id = ? LIMIT 1`,
		sessionID,
	).Scan(&featureID)
	return featureID.Valid && featureID.String != ""
}

// hasAnyActiveWorkItem returns true when at least one work item (feature, bug,
// or spike) is in-progress in the project. All work item types are stored in
// the features table, distinguished by the type column.
//
// This is used as a YOLO-mode fallback when CLAUDE_ENV_FILE is unset (typical
// in YOLO mode), causing HTMLGRAPH_SESSION_ID to not be exported. In that case
// `bug start` falls back to the .active-session file and writes
// active_feature_id to a different session row than the one the PreToolUse
// hook resolves from the CloudEvent payload. The fallback allows the edit when
// any work item is in-progress, preventing false blocks after `bug start`.
func hasAnyActiveWorkItem(database *sql.DB) bool {
	if database == nil {
		return false
	}
	var count int
	database.QueryRow(`
		SELECT COUNT(*) FROM features
		WHERE status = 'in-progress'
		  AND type IN ('feature', 'bug', 'spike')
		LIMIT 1`).Scan(&count)
	return count > 0
}

// featureStartPattern matches htmlgraph feature/bug start commands.
var featureStartPattern = regexp.MustCompile(`\bhtmlgraph\s+(feature|bug)\s+start\s+([\w-]+)`)

// checkYoloStepsGuard warns when starting a work item that has no
// implementation steps. Returns a non-empty reason to warn, or "" to allow.
func checkYoloStepsGuard(event *CloudEvent, yolo bool, htmlgraphDir string) string {
	if !yolo || event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	m := featureStartPattern.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	itemID := m[2]
	stepsCount := countStepsForItem(htmlgraphDir, itemID)
	if stepsCount > 0 {
		return ""
	}
	return fmt.Sprintf(
		"Warning: %s has no implementation steps. "+
			"Add steps first: htmlgraph feature add-step %s \"description\"",
		itemID, itemID)
}

// countStepsForItem reads an HTML work item file and counts its steps.
func countStepsForItem(htmlgraphDir, itemID string) int {
	subdirs := []string{"features", "bugs", "spikes", "tracks", "plans", "specs"}
	for _, sub := range subdirs {
		path := filepath.Join(htmlgraphDir, sub, itemID+".html")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return strings.Count(string(data), "data-step-id=")
	}
	return 0
}

// gitCommitPattern matches "git commit" as a standalone command.
// It is anchored to the start of the string (modulo leading whitespace) and
// requires that after "commit" comes whitespace, end-of-string, or a flag
// prefix (-- or -X where X is not a lowercase letter). This excludes git
// plumbing sub-commands like "git commit-tree" and "git commit-graph" where
// the dash is part of the sub-command name rather than a flag separator.
var gitCommitPattern = regexp.MustCompile(`^\s*git\s+commit(\s|$|--|-[^a-z])`)

// fallbackTestSuggestion is used when the project's language can't be
// detected from manifest files. It enumerates the supported test
// commands so the user can pick the relevant one rather than seeing
// a confidently-wrong single command (bug-f616c2a8).
const fallbackTestSuggestion = "your project's test suite (go test ./..., uv run pytest, npm test, cargo test, etc.)"

// checkYoloCommitGuard blocks git commit when tests haven't run in
// the current session. Returns a non-empty reason to block, or "" to allow.
//
// The error message names the test command for the detected project
// type (Go, Python, Node, Rust). Falls back to a generic enumeration
// when no manifest file is found in the project root or its monorepo
// subdirectories. Previously emitted "go test ./... or uv run pytest"
// regardless of project, which was confusing in single-language
// projects (bug-f616c2a8).
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
	suggestion := paths.TestCommandFor(paths.DetectProjectType(ResolveProjectDir(event.CWD, event.SessionID)))
	if suggestion == "" {
		suggestion = fallbackTestSuggestion
	}
	return "YOLO mode requires tests to pass before committing. Run: " + suggestion
}

// checkYoloBudgetGuard blocks git commit when the staged diff exceeds
// YOLO hard limits (20 files or 600 lines added). Merge commits are
// exempt — they combine already-reviewed sub-feature work.
func checkYoloBudgetGuard(event *CloudEvent, yolo bool) string {
	if !yolo || event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if !gitCommitPattern.MatchString(cmd) {
		return ""
	}
	if mergeInProgressFn() {
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
	if fileCount > yoloBudgetMaxFiles || totalAdded > yoloBudgetMaxLines {
		return fmt.Sprintf(
			"YOLO budget HARD LIMIT: %d files, %d lines (max %d/%d). "+
				"Split into sub-features.", fileCount, totalAdded, yoloBudgetMaxFiles, yoloBudgetMaxLines)
	}
	return ""
}

// isMergeInProgress returns true when git is resolving a merge (MERGE_HEAD exists).
func isMergeInProgress() bool {
	out, err := exec.Command("git", "rev-parse", "--git-dir").Output()
	if err != nil {
		return false
	}
	gitDir := strings.TrimSpace(string(out))
	_, err = os.Stat(filepath.Join(gitDir, "MERGE_HEAD"))
	return err == nil
}

// checkYoloWorktreeGuard blocks Write/Edit on main/master branch in YOLO mode.
// Merge conflict resolution is exempt — edits on main during an active merge
// are integration work, not feature development.
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
		if mergeInProgressFn() {
			return ""
		}
		return "YOLO mode requires a feature or track branch. " +
			"Use: htmlgraph yolo --track <id> or htmlgraph yolo --feature <id>"
	}
	return ""
}

// checkYoloBashWorktreeGuard extends the worktree guard to Bash file-write
// commands on main/master branch.
// htmlgraph CLI commands are always exempt — they are the approved write path.
func checkYoloBashWorktreeGuard(event *CloudEvent, branch string, yolo bool) string {
	if !yolo {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if bashHtmlGraphCLI.MatchString(cmd) {
		return ""
	}
	if !isBashFileWrite(event) {
		return ""
	}
	if branch == "main" || branch == "master" {
		if mergeInProgressFn() {
			return ""
		}
		return "YOLO mode requires a feature or track branch for Bash file writes. " +
			"Use: htmlgraph yolo --track <id> or htmlgraph yolo --feature <id>"
	}
	return ""
}

// checkYoloResearchGuard blocks Write/Edit when no Read/Grep/Glob has
// occurred in the session (research-first principle). Always enforced.
func checkYoloResearchGuard(toolName string, _ bool, hasResearch bool) string {
	switch toolName {
	case "Write", "Edit", "MultiEdit":
	default:
		return ""
	}
	if hasResearch {
		return ""
	}
	return "Research is required before writing code. " +
		"Read existing code first: use Read, Grep, or Glob tools."
}

// checkYoloBashResearchGuard extends the research guard to Bash file-write commands.
// Always enforced. htmlgraph CLI commands are always exempt.
func checkYoloBashResearchGuard(event *CloudEvent, _ bool, hasResearch bool) string {
	cmd, _ := event.ToolInput["command"].(string)
	if bashHtmlGraphCLI.MatchString(cmd) {
		return ""
	}
	if !isBashFileWrite(event) {
		return ""
	}
	if hasResearch {
		return ""
	}
	return "Research is required before writing code via Bash. " +
		"Read existing code first: use Read, Grep, or Glob tools."
}

// checkYoloOrchestratorWriteGuard warns (does not block) when the top-level
// orchestrator session writes files directly instead of delegating to a
// subagent. This is a soft enforcement of the "delegate, don't implement"
// rule — logged for observability but not blocking to avoid breaking
// non-YOLO or legitimate orchestrator writes.
func checkYoloOrchestratorWriteGuard(event *CloudEvent, isSubagent bool) string {
	if isSubagent {
		return "" // Subagents are expected to write files.
	}
	switch event.ToolName {
	case "Write", "Edit", "MultiEdit":
		return "Orchestrator writing directly instead of delegating. " +
			"Consider using a coder agent for implementation work."
	}
	return ""
}

// checkYoloRoborevGuard blocks git commit when there are completed roborev
// reviews with findings (verdict == "F") from prior commits in this session.
// This is a "review gate, not a review wall": it only fires when roborev has
// already finished reviewing a prior commit and found issues. Entries with no
// verdict (still running) are not blocking. Any error (roborev not installed,
// daemon down, timeout) causes a fail-open return of "" to avoid blocking
// unrelated work.
func checkYoloRoborevGuard(event *CloudEvent, yolo bool) string {
	if !yolo || event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if !gitCommitPattern.MatchString(cmd) {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	roborevCmd := exec.CommandContext(ctx, "roborev", "list", "--open", "--json")
	if event.CWD != "" {
		roborevCmd.Dir = event.CWD
	}
	out, err := roborevCmd.Output()
	if err != nil {
		return "" // fail-open: not installed, daemon down, timeout, etc.
	}

	type roborevEntry struct {
		ID            string `json:"id"`
		Verdict       string `json:"verdict"`
		CommitSubject string `json:"commit_subject"`
	}

	var entries []roborevEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return "" // fail-open: unexpected output format
	}

	var failedIDs []string
	for _, e := range entries {
		if e.Verdict == "F" {
			failedIDs = append(failedIDs, e.ID)
		}
	}
	if len(failedIDs) == 0 {
		return ""
	}

	return fmt.Sprintf(
		"roborev: %d open review(s) with findings — fix before committing (job IDs: %s). "+
			"Run /roborev-fix to address them.",
		len(failedIDs), strings.Join(failedIDs, ", "))
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

// checkYoloCodeHealthGuard warns about oversized source files (>yoloCodeHealthMaxLines)
// in YOLO mode. When a file is already oversized, the guard allows edits to
// proceed — blocking would prevent the refactoring needed to reduce file size.
// The warning message is returned for logging in pretooluse; PostToolUse
// performs the actual enforcement via CheckFileQuality.
// Covers Go, Python, JavaScript, and TypeScript files.
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
	if !isCodeHealthCheckedFile(path) {
		return ""
	}
	// Check existing file size — if it's already >yoloCodeHealthMaxLines, warn
	// but allow (blocking would prevent the refactoring needed to fix it).
	data, err := os.ReadFile(path)
	if err != nil {
		return "" // new file, allow
	}
	lines := strings.Count(string(data), "\n")
	if lines > yoloCodeHealthMaxLines {
		return fmt.Sprintf(
			"Code health: %s has %d lines (limit %d). Consider refactoring into smaller modules.",
			filepath.Base(path), lines, yoloCodeHealthMaxLines)
	}
	return ""
}

// isCodeHealthCheckedFile returns true for file extensions that are subject
// to the YOLO code-health line-count guard.
func isCodeHealthCheckedFile(path string) bool {
	for _, ext := range []string{".go", ".py", ".js", ".ts", ".tsx", ".jsx"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// hasRecentResearch checks if Read/Grep/Glob was used in this session
// or its parent session. Also checks if the session has any events at all —
// if not, the event recording pipeline is likely broken (e.g. DB mismatch
// in worktrees) and we fail-open rather than blocking valid work.
func hasRecentResearch(database *sql.DB, sessionID string) bool {
	if database == nil || sessionID == "" {
		return true // fail-open: can't verify, don't block
	}
	// Check this session and its parent (worktree subagents inherit context).
	sessionIDs := getSessionAndParent(database, sessionID)
	for _, sid := range sessionIDs {
		var count int
		database.QueryRow(`
			SELECT COUNT(*) FROM agent_events
			WHERE session_id = ? AND tool_name IN ('Read', 'Grep', 'Glob')
			LIMIT 1`,
			sid,
		).Scan(&count)
		if count > 0 {
			return true
		}
	}
	// If the session has zero events total, event recording is broken —
	// fail-open to avoid blocking valid work.
	var totalEvents int
	for _, sid := range sessionIDs {
		var c int
		database.QueryRow(`SELECT COUNT(*) FROM agent_events WHERE session_id = ? LIMIT 1`, sid).Scan(&c)
		totalEvents += c
	}
	if totalEvents == 0 {
		return true // no events recorded at all — recording issue, not laziness
	}
	return false
}

// getSessionAndParent returns the current session ID plus its parent session
// ID (if any). Worktree subagents inherit context from the outer orchestrator
// session that spawned them.
func getSessionAndParent(database *sql.DB, sessionID string) []string {
	sessionIDs := []string{sessionID}
	var parentID string
	database.QueryRow(
		`SELECT COALESCE(parent_session_id, '') FROM sessions WHERE session_id = ?`,
		sessionID,
	).Scan(&parentID)
	if parentID != "" {
		sessionIDs = append(sessionIDs, parentID)
	}
	return sessionIDs
}

// hasRecentDiffReview checks if git diff was run in this session or its
// parent session. Worktree subagents inherit diff reviews from the outer
// orchestrator session that spawned them.
func hasRecentDiffReview(database *sql.DB, sessionID string) bool {
	for _, sid := range getSessionAndParent(database, sessionID) {
		var count int
		database.QueryRow(`
			SELECT COUNT(*) FROM agent_events
			WHERE session_id = ? AND tool_name = 'Bash'
			  AND (input_summary LIKE '%git diff%'
			    OR input_summary LIKE '%git show%')`,
			sid,
		).Scan(&count)
		if count > 0 {
			return true
		}
	}
	return false
}

// currentBranchIn returns the git branch for the given directory.
func currentBranchIn(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// branchForFilePath returns the git branch for the worktree that owns filePath.
// When the file lives in a linked worktree (e.g. .claude/worktrees/yolo-feat-xxx),
// this returns that worktree's branch rather than the main repo's branch.
// Falls back to cwdBranch when filePath is empty or not under git control.
func branchForFilePath(filePath, cwdBranch string) string {
	if filePath == "" {
		return cwdBranch
	}
	dir := filepath.Dir(filePath)
	branch := currentBranchIn(dir)
	if branch == "" {
		return cwdBranch
	}
	return branch
}

// testPattern matches common test runner commands in Bash input summaries.
var testPattern = regexp.MustCompile(`\bgo test\b|\bpytest\b|\buv run pytest\b|\buv run ruff\b`)

// hasRecentTestRun checks if a test command was executed in this session
// or its parent session by scanning recent agent_events for Bash commands
// matching test patterns. Worktree subagents inherit test runs from the
// outer orchestrator session that spawned them.
func hasRecentTestRun(database *sql.DB, sessionID string) bool {
	for _, sid := range getSessionAndParent(database, sessionID) {
		var count int
		database.QueryRow(`
			SELECT COUNT(*) FROM agent_events
			WHERE session_id = ? AND tool_name = 'Bash'
			  AND (input_summary LIKE '%go test%'
			    OR input_summary LIKE '%go build%'
			    OR input_summary LIKE '%pytest%'
			    OR input_summary LIKE '%uv run ruff%')`,
			sid,
		).Scan(&count)
		if count > 0 {
			return true
		}
	}
	return false
}

// uiFileExtensions are the file extensions considered UI files for the purpose
// of visual validation gating. A commit that only stages backend files skips
// the screenshot requirement entirely.
var uiFileExtensions = []string{".html", ".css", ".tsx", ".jsx", ".vue", ".svelte", ".js", ".ts"}

// hasStagedUIFiles runs git diff --cached --name-only and checks whether any
// staged file has a UI extension or lives under a UI directory (templates/,
// dashboard/). Returns false on any error so the gate degrades to allow.
func hasStagedUIFiles() bool {
	out, err := exec.Command("git", "diff", "--cached", "--name-only").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		// Skip data files — .htmlgraph/ HTML files are work items, not UI.
		if strings.Contains(lower, ".htmlgraph/") {
			continue
		}
		// UI directories.
		if strings.Contains(lower, "templates/") || strings.Contains(lower, "dashboard/") {
			return true
		}
		// UI file extensions.
		for _, ext := range uiFileExtensions {
			if strings.HasSuffix(lower, ext) {
				return true
			}
		}
	}
	return false
}

// checkYoloUIValidationGuard blocks git commit when UI files are staged and no
// screenshot or visual validation was performed in the session.
// Returns a non-empty reason to block, or "" to allow.
//
// Fix: three structural problems were repaired (bug-a10ae96a / GH#36):
//  1. Staged-diff precheck — fires only when UI files are actually staged;
//     backend-only commits pass immediately without screenshot requirement.
//  2. Bash scope — gitCommitPattern is now anchored to `^\s*git\s+commit`
//     with a suffix guard that rejects "commit-tree" and "commit-graph"
//     (plumbing sub-commands). "gh issue create" and similar never matched.
//  3. Screenshot detection — the old LIKE '%screenshot%' pattern never matched
//     the only available Chrome MCP tool (mcp__claude-in-chrome__computer).
//     Now also checks tool_name = 'mcp__claude-in-chrome__computer' with
//     "action":"screenshot" in the tool_input JSON column. Existing
//     take_screenshot patterns are retained for other MCP server flavours.
func checkYoloUIValidationGuard(event *CloudEvent, yolo bool, database *sql.DB, sessionID string) string {
	if !yolo || event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if !gitCommitPattern.MatchString(cmd) {
		return ""
	}

	// Fix 1: precheck the actual staged diff before touching session state.
	// If no UI files are staged, the gate is a no-op — backend-only commits
	// pass immediately without needing a screenshot.
	if !hasStagedUIFiles() {
		return ""
	}

	// Check if any UI files were modified in this session.
	// Exclude .htmlgraph/ work item HTML files — those are data, not UI.
	var uiFileCount int
	database.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		WHERE session_id = ? AND tool_name IN ('Write', 'Edit', 'MultiEdit')
		  AND (input_summary LIKE '%.html%' OR input_summary LIKE '%.css%'
		    OR input_summary LIKE '%.js%'  OR input_summary LIKE '%.ts%'
		    OR input_summary LIKE '%.tsx%' OR input_summary LIKE '%.vue%'
		    OR input_summary LIKE '%.svelte%')
		  AND input_summary NOT LIKE '%.htmlgraph/%'
		  AND status = 'completed'`,
		sessionID,
	).Scan(&uiFileCount)

	if uiFileCount == 0 {
		return "" // no UI files touched in this session
	}

	// Fix 3: check for screenshot / UI validation in session (+ parent).
	// Supported screenshot tool names:
	//   - mcp__claude-in-chrome__computer  (Chrome MCP, action=screenshot)
	//   - take_screenshot                  (other MCP server flavours)
	//   - *screenshot*                     (catch-all for future servers)
	for _, sid := range getSessionAndParent(database, sessionID) {
		var validationCount int
		database.QueryRow(`
			SELECT COUNT(*) FROM agent_events
			WHERE session_id = ?
			  AND (
			    -- Chrome MCP: tool_name is mcp__claude-in-chrome__computer,
			    -- action discriminator lives in the tool_input JSON column.
			    (tool_name = 'mcp__claude-in-chrome__computer'
			      AND tool_input LIKE '%"action":"screenshot"%')
			    -- Other MCP servers that expose a dedicated screenshot tool.
			    OR tool_name LIKE '%take_screenshot%'
			    OR tool_name LIKE '%screenshot%'
			  )`,
			sid,
		).Scan(&validationCount)
		if validationCount > 0 {
			return ""
		}
	}

	return "UI files were modified but no visual validation was performed. " +
		"Take a screenshot before committing."
}
