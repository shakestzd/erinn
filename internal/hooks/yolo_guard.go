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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/paths"
	"github.com/shakestzd/wipnote/internal/storage"
)

// mergeInProgressFn is injected for testing. In production, it checks the real
// git state. In tests, it can be overridden to return false to avoid git state
// bleeding into test isolation.
var mergeInProgressFn = isMergeInProgress

// isYoloFromEvent checks the CloudEvent permission_mode field first (live
// state from Claude Code), falling back to a SQLite DB lookup.
// Fast-path: WIPNOTE_YOLO=1 is set only by the wipnote launcher on an explicit
// --yolo launch (e.g. `wipnote codex --yolo`). It is harness-agnostic and has
// no false-positive path because nothing else sets this variable.
func isYoloFromEvent(event *CloudEvent, wipnoteDir string) bool {
	if os.Getenv("WIPNOTE_YOLO") == "1" {
		return true
	}
	if event.PermissionMode == "bypassPermissions" {
		return true
	}
	// If Claude Code reports a non-bypass mode, trust it.
	if event.PermissionMode != "" {
		return false
	}
	// Fallback: check DB for session's last known permission_mode.
	// This is populated by the ConfigChange hook handler.
	return isYoloFromDB(wipnoteDir, event.SessionID)
}

// isYoloWithInheritance checks YOLO mode for the current session and, when the
// current session has no YOLO marker of its own, walks the parent-session chain
// to check whether any ancestor session is in YOLO posture.
//
// Top-level vs subagent semantics (bug-0ed4e469):
//   - isYoloFromEvent's bypassPermissions fast-path always wins for both.
//   - For a TOP-LEVEL session (isSubagent=false), an explicit non-empty,
//     non-bypass permission_mode is a deliberate user declaration of non-YOLO
//     posture and is honored: the chain walk is skipped and we return false.
//   - For a SUBAGENT (isSubagent=true), its own reported permission_mode is NOT
//     a deliberate non-YOLO declaration — Claude Code often reports a non-empty,
//     non-bypass mode (e.g. "default"/"acceptEdits") for coder subagents inside
//     a YOLO run. So for subagents we ALWAYS fall through to the parent-chain
//     inheritance walk regardless of the subagent's own permission_mode.
//
// When YOLO is inherited from an ancestor, a debug log line is emitted for
// auditability.
func isYoloWithInheritance(event *CloudEvent, wipnoteDir string, database *sql.DB, sessionID, projectDir string, isSubagent bool) bool {
	if isYoloFromEvent(event, wipnoteDir) {
		return true
	}
	// An explicit non-empty, non-bypass permission_mode on a TOP-LEVEL session
	// is a deliberate non-YOLO declaration — do not override with an ancestor's
	// posture. Subagents do NOT get this short-circuit: their reported mode is
	// not a deliberate declaration, so they fall through to the parent walk.
	if !isSubagent && event.PermissionMode != "" {
		return false
	}
	// Walk the parent chain (subagents always reach here; top-level sessions
	// reach here only when they have no declared mode).
	if database == nil || sessionID == "" {
		return false
	}
	sessionIDs := getSessionAndParent(database, sessionID)
	if len(sessionIDs) < 2 {
		return false // no parent
	}
	for _, parentID := range sessionIDs[1:] {
		if isYoloFromDB(wipnoteDir, parentID) {
			debugLog(projectDir, "[wipnote] yolo inherited: session=%s parent=%s",
				sessionID, parentID)
			return true
		}
	}
	return false
}

// anyParentSessionYolo returns true when any ancestor session in the immediate
// parent chain of sessionID is in YOLO posture per isYoloFromDB. Used as a
// resilient defense-in-depth signal for the worktree-isolation guard so that a
// yolo-context subagent is still blocked from editing main/master even if the
// primary IsYoloMode detection resolved false. Returns false on nil DB / empty
// session / no parent.
func anyParentSessionYolo(database *sql.DB, wipnoteDir, sessionID string) bool {
	if database == nil || sessionID == "" {
		return false
	}
	sessionIDs := getSessionAndParent(database, sessionID)
	if len(sessionIDs) < 2 {
		return false
	}
	for _, parentID := range sessionIDs[1:] {
		if isYoloFromDB(wipnoteDir, parentID) {
			return true
		}
	}
	return false
}

// isYoloFromDB looks up the session's permission_mode from the sessions.metadata
// JSON column. This is populated by the ConfigChange hook when the user toggles
// permission mode in Claude Code.
func isYoloFromDB(wipnoteDir, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	projectDir := filepath.Dir(wipnoteDir)
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
// targetFile is the Write/Edit target path extracted from the tool input.
// When the target is outside the project root (home config, /tmp, sibling repos,
// etc.) the guard is skipped — project-code discipline must not constrain
// writes to external locations like ~/.claude/ memory files.
// projectRoot is the resolved project directory (ctx.ProjectDir); when empty
// the guard applies unconditionally (conservative).
func checkYoloWorkItemGuard(toolName, featureID string, _ bool, sessionID string, db *sql.DB, targetFile, projectRoot string) string {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "apply_patch":
	default:
		return ""
	}
	// Skip for writes targeting paths outside the project root.
	if targetFile != "" && pathIsOutsideProject(targetFile, projectRoot) {
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
	return "An active work item is required before writing code. " +
		"Run: wipnote feature start <id>  or  wipnote feature create \"title\" --track <trk-id>"
}

// yoloSubagentGracePeriod is the window after session start during which a
// subagent is allowed to write files before claiming a work item. This gives
// the subagent time to run `wipnote feature start <id>` as its first action.
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
// in YOLO mode), causing WIPNOTE_SESSION_ID to not be exported. In that case
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

// featureStartPattern matches wipnote feature/bug start commands.
var featureStartPattern = regexp.MustCompile(`\bwipnote\s+(feature|bug)\s+start\s+([\w-]+)`)

// checkYoloStepsGuard warns when starting a work item that has no
// implementation steps. Returns a non-empty reason to warn, or "" to allow.
func checkYoloStepsGuard(event *CloudEvent, yolo bool, wipnoteDir string) string {
	if !yolo || !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
	m := featureStartPattern.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	itemID := m[2]
	stepsCount := countStepsForItem(wipnoteDir, itemID)
	if stepsCount > 0 {
		return ""
	}
	return fmt.Sprintf(
		"Warning: %s has no implementation steps. "+
			"Add steps first: wipnote feature add-step %s \"description\"",
		itemID, itemID)
}

// countStepsForItem reads an HTML work item file and counts its steps.
func countStepsForItem(wipnoteDir, itemID string) int {
	subdirs := []string{"features", "bugs", "spikes", "tracks", "plans", "specs"}
	for _, sub := range subdirs {
		path := filepath.Join(wipnoteDir, sub, itemID+".html")
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
	if !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
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
	if !yolo || !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
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
	case "Write", "Edit", "MultiEdit", "apply_patch":
	default:
		return ""
	}
	if branch == "main" || branch == "master" {
		if mergeInProgressFn() {
			return ""
		}
		return "YOLO mode requires a feature or track branch. " +
			"Use: wipnote yolo --track <id> or wipnote yolo --feature <id>"
	}
	return ""
}

// checkYoloBashWorktreeGuard extends the worktree guard to Bash file-write
// commands on main/master branch.
// wipnote CLI commands are always exempt — they are the approved write path.
func checkYoloBashWorktreeGuard(event *CloudEvent, branch string, yolo bool) string {
	if !yolo {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
	if isWipnoteCLICommand(cmd) {
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
			"Use: wipnote yolo --track <id> or wipnote yolo --feature <id>"
	}
	return ""
}

// checkYoloResearchGuard blocks Write/Edit when no Read/Grep/Glob has
// occurred in the session (research-first principle). Always enforced.
//
// targetFile is the Write/Edit target path extracted from the tool input.
// When the target is outside the project root (home config, /tmp, sibling repos,
// etc.) the guard is skipped — research-first discipline applies to project code,
// not to external config or memory files.
// projectRoot is the resolved project directory (ctx.ProjectDir); when empty
// the guard applies unconditionally (conservative).
func checkYoloResearchGuard(toolName string, _ bool, hasResearch bool, targetFile, projectRoot string) string {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "apply_patch":
	default:
		return ""
	}
	// Skip for writes targeting paths outside the project root.
	if targetFile != "" && pathIsOutsideProject(targetFile, projectRoot) {
		return ""
	}
	if hasResearch {
		return ""
	}
	return "Research is required before writing code. " +
		"Read the relevant code (Read/Grep/Glob) and/or consult official docs, " +
		"GitHub issues, or the web (WebSearch/WebFetch, `gh search`) — especially " +
		"for external libraries, upstream tools, or unfamiliar error messages."
}

// checkYoloBashResearchGuard extends the research guard to Bash file-write commands.
// Always enforced. wipnote CLI commands are always exempt.
//
// When the write targets a path outside the project tree (e.g. ~/.config/…),
// the message omits the "use Read/Grep/Glob" suggestion — those tools cannot
// reach paths outside the project root (bug-d0c8b1e2).
func checkYoloBashResearchGuard(event *CloudEvent, _ bool, hasResearch bool) string {
	cmd := shellCommand(event.ToolInput)
	if isWipnoteCLICommand(cmd) {
		return ""
	}
	if !isBashFileWrite(event) {
		return ""
	}
	if hasResearch {
		return ""
	}
	projectRoot := ResolveProjectDir(event.CWD, event.SessionID)
	if bashCommandTargetsExternalPath(cmd, projectRoot) {
		return "Research is required before modifying files outside the project. " +
			"Review the target files with Bash (cat, head, stat) before making changes."
	}
	return "Research is required before writing code via Bash. " +
		"Read the relevant code (Read/Grep/Glob) and/or consult official docs, " +
		"GitHub issues, or the web (WebSearch/WebFetch, `gh search`) — especially " +
		"for external libraries, upstream tools, or unfamiliar error messages."
}

// pathIsOutsideProject returns true when path refers to a location outside the
// project root. It is the shared classification heuristic used by both the
// Bash research guard and the Write/Edit work-item and research guards.
//
// Rules (applied in order):
//  1. Empty path → false (cannot classify, treat as in-project).
//  2. ~/- or ~\-prefixed home paths are external, EXCEPT allow-listed dirs
//     (~/.gotest, ~/.tmp, ~/.cache) which are test/temp scratch space.
//  3. Absolute paths inside the project root (via filepath.Rel) → internal.
//  4. Absolute paths in /workspaces/ siblings when the project is also under
//     /workspaces/ → internal (Codespaces convention).
//  5. All other absolute paths → external.
//  6. Relative paths → false (treated as in-project; caller is responsible for
//     resolving against CWD if needed).
func pathIsOutsideProject(path, projectRoot string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		// Allow common test/temp directories in home.
		if strings.HasPrefix(path, "~/.gotest") || strings.HasPrefix(path, "~/.tmp") || strings.HasPrefix(path, "~/.cache") {
			return false
		}
		return true
	}
	if strings.HasPrefix(path, "/") {
		// Allow siblings in /workspaces/ if the project itself is in /workspaces/.
		if strings.HasPrefix(projectRoot, "/workspaces/") && strings.HasPrefix(path, "/workspaces/") {
			return false
		}
		// Resolve against project root: if the path is inside the project,
		// it is internal — not an external write.
		if projectRoot != "" {
			rel, err := filepath.Rel(projectRoot, path)
			if err == nil && !strings.HasPrefix(rel, "..") {
				return false // in-repo absolute path
			}
		}
		return true
	}
	// Relative path — treat as in-project.
	return false
}

// bashCommandTargetsExternalPath returns true when the Bash command's first
// path-like argument starts with a home-directory shorthand (~) or is an absolute
// path that falls outside the project root. This is a best-effort heuristic used
// to tailor error messages — it does not gate execution and must err on the side
// of false negatives.
//
// projectRoot is used to classify absolute paths: paths inside the project root
// are considered internal (returns false); paths outside are external (returns true).
// When projectRoot is empty, any absolute path is treated as external.
func bashCommandTargetsExternalPath(cmd, projectRoot string) bool {
	// Whitelist osascript: typically used to drive macOS apps (Notes, Mail, etc.)
	// via AppleScript. While it can write files, its primary use in research
	// is app-control and doesn't warrant a filesystem-protection block.
	if strings.HasPrefix(cmd, "osascript") {
		return false
	}

	// Look for the first argument that looks like a path (starts with ~ or /).
	for _, field := range strings.Fields(cmd) {
		if pathIsOutsideProject(field, projectRoot) {
			return true
		}
		// pathIsOutsideProject returns false for relative paths AND for in-project
		// absolute paths. For the Bash heuristic we must stop at the first
		// path-like token (~ or /), so only continue scanning for non-path tokens.
		if strings.HasPrefix(field, "~/") || strings.HasPrefix(field, "~\\") || strings.HasPrefix(field, "/") {
			return false
		}
	}
	return false
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
	case "Write", "Edit", "MultiEdit", "apply_patch":
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
	if !yolo || !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
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

	// RawID uses json.RawMessage so both string ("j1") and integer (46) IDs
	// from different roborev versions parse without type-mismatch errors.
	type roborevEntry struct {
		RawID         json.RawMessage `json:"id"`
		Verdict       string          `json:"verdict"`
		CommitSubject string          `json:"commit_subject"`
	}

	var entries []roborevEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return "" // fail-open: unexpected output format
	}

	var failedIDs []string
	for _, e := range entries {
		if e.Verdict == "F" {
			// Strip surrounding quotes for string IDs; numbers pass through as-is.
			failedIDs = append(failedIDs, strings.Trim(string(e.RawID), `"`))
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
	if !yolo || !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
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
	case "Write", "Edit", "MultiEdit", "apply_patch":
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

// genericAgentIDs lists harness-level agent identifiers that must not be used
// as a cross-session bridge — they appear in many unrelated sessions.
var genericAgentIDs = []string{"claude-code", "codex", "gemini", "human"}

// isGenericAgentID returns true when id is one of the well-known harness
// identifiers that should never be used for cross-session matching.
func isGenericAgentID(id string) bool {
	return slices.Contains(genericAgentIDs, id)
}

// collectRelatedSessionIDs builds a deduplicated slice of session IDs that
// are related to sessionID via two mechanisms:
//
//  1. Transitive parent walk: follows sessions.parent_session_id upward until
//     NULL or a cycle (capped at maxLineageHops hops).
//  2. Lineage trace fallback: for any session ID in the walking set, if an
//     agent_lineage_trace row exists with session_id = that ID, its
//     root_session_id is added too. This catches cases where
//     sessions.parent_session_id is NULL but SubagentStart wrote a trace row.
const maxLineageHops = 8

func collectRelatedSessionIDs(database *sql.DB, sessionID string) []string {
	if database == nil || sessionID == "" {
		return []string{sessionID}
	}
	seen := map[string]struct{}{}
	result := []string{}

	add := func(id string) bool {
		if id == "" {
			return false
		}
		if _, ok := seen[id]; ok {
			return false
		}
		seen[id] = struct{}{}
		result = append(result, id)
		return true
	}

	// Transitive parent walk.
	current := sessionID
	for hop := 0; hop <= maxLineageHops; hop++ {
		if !add(current) {
			break // cycle detected or already visited
		}
		var parentID string
		database.QueryRow(
			`SELECT COALESCE(parent_session_id, '') FROM sessions WHERE session_id = ?`,
			current,
		).Scan(&parentID)
		if parentID == "" {
			break
		}
		current = parentID
	}

	// Lineage trace fallback: for each ID we already have, check if it has a
	// trace row pointing to a root we haven't seen yet.
	snapshot := make([]string, len(result))
	copy(snapshot, result)
	for _, sid := range snapshot {
		var rootID string
		database.QueryRow(
			`SELECT COALESCE(root_session_id, '') FROM agent_lineage_trace WHERE session_id = ? LIMIT 1`,
			sid,
		).Scan(&rootID)
		add(rootID) // no-op if empty or already present
	}

	return result
}

// hasRecentResearch checks if Read/Grep/Glob (or equivalent research tools)
// were used in this session, any ancestor session, or by the same agent across
// sessions. It also handles two failure modes that previously caused misfires:
//
//  1. Agent-ID mismatch: sub-agent Reads are stored under the orchestrator's
//     session_id with the sub-agent's agent_id. The agentID parameter is used
//     as an additional match key so those events are found even when the
//     session walk misses them.
//
//  2. Orphaned sessions: when sessions.parent_session_id is NULL but a lineage
//     trace exists, collectRelatedSessionIDs follows the trace to the root.
//
// When the event recording pipeline is broken (zero tool_call events across all
// related IDs), the function fails open and emits a debug-log warning rather
// than silently blocking valid work.
func hasRecentResearch(database *sql.DB, sessionID, agentID, projectDir string) bool {
	if database == nil || sessionID == "" {
		return true // fail-open: can't verify, don't block
	}

	relatedSIDs := collectRelatedSessionIDs(database, sessionID)

	// Build the parameterized IN clause for session IDs.
	inClause, inArgs := buildInClause(relatedSIDs)

	// Determine whether agentID is usable as a cross-session bridge.
	useAgentID := agentID != "" && !isGenericAgentID(agentID)

	// Compose the research query.
	researchQuery, researchArgs := buildResearchQuery(inClause, inArgs, useAgentID, agentID, projectDir)

	var researchCount int
	database.QueryRow(researchQuery, researchArgs...).Scan(&researchCount)
	if researchCount > 0 {
		return true
	}

	// Research count is 0 — determine whether that's because no tool calls were
	// recorded at all (recording gap → fail-open) or because tool calls ran but
	// none were research-y (genuine no-research → block).
	toolCallQuery, toolCallArgs := buildToolCallQuery(inClause, inArgs, useAgentID, agentID, projectDir)
	var toolCallCount int
	database.QueryRow(toolCallQuery, toolCallArgs...).Scan(&toolCallCount)

	if toolCallCount == 0 {
		// No tool_call events at all — likely a recording-pipeline gap (e.g. FK
		// failures, DB mismatch in worktrees, fresh session where only a SessionStart
		// event has been recorded).
		debugLog(projectDir,
			"[wipnote] research-gate fail-open: no tool_call events recorded for session=%s agent=%s — recording pipeline may be broken",
			sessionID, agentID)
		return true
	}

	// Tool calls were recorded but none qualify as research → block.
	return false
}

// buildInClause returns a SQL fragment like "(?, ?, ?)" and the matching args
// slice. If ids is empty the clause is "(NULL)" so the query is syntactically
// valid but matches nothing.
func buildInClause(ids []string) (string, []any) {
	if len(ids) == 0 {
		return "(NULL)", nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return "(" + strings.Join(placeholders, ", ") + ")", args
}

// buildResearchQuery builds the research detection SQL and its argument slice.
// It matches Read/Grep/Glob (and equivalents) plus web/docs/GitHub research
// (WebSearch/WebFetch and `gh ...`) under any of the related session IDs, and
// optionally also by agentID when useAgentID is true.
//
// The research-disposition source of truth lives in
// cmd/wipnote/prompts/research-routing.md and the agent-context skill
// (plugin/skills/agent-context/SKILL.md). Keep the qualifying-tool list here in
// sync so the guard does not penalize the web/docs/GitHub-first research those
// prompts encourage.
// When agentID is used, the query is scoped to events from the same project
// and from the last 24 hours to prevent cross-project and stale event leakage.
func buildResearchQuery(inClause string, inArgs []any, useAgentID bool, agentID, projectDir string) (string, []any) {
	sessionFilter, args := sessionOrAgentFilter(inClause, inArgs, useAgentID, agentID, projectDir)
	query := fmt.Sprintf(`
		SELECT COUNT(*) FROM agent_events
		WHERE %s
		  AND (
			tool_name IN (
				'Read', 'Grep', 'Glob',
				'WebSearch', 'WebFetch',
				'read_file', 'grep_search', 'glob', 'list_directory',
				'web_fetch', 'web_search', 'google_web_search'
			) OR (
				tool_name = 'Bash' AND (
					input_summary LIKE 'ls %%' OR input_summary = 'ls'
					OR input_summary LIKE 'find %%'
					OR input_summary LIKE 'cat %%'
					OR input_summary LIKE 'grep %%'
					OR input_summary LIKE 'head %%'
					OR input_summary LIKE 'tail %%'
					OR input_summary LIKE 'stat %%'
					OR input_summary LIKE 'gh %%'
				)
			)
		  )
		LIMIT 1`, sessionFilter)
	return query, args
}

// buildToolCallQuery builds a query that counts all tool_call events (any tool)
// matching the session/agent filter. Used to distinguish a recording gap from
// a genuine no-research case.
// When agentID is used, the query is scoped to events from the same project
// and from the last 24 hours to prevent cross-project and stale event leakage.
func buildToolCallQuery(inClause string, inArgs []any, useAgentID bool, agentID, projectDir string) (string, []any) {
	sessionFilter, args := sessionOrAgentFilter(inClause, inArgs, useAgentID, agentID, projectDir)
	query := fmt.Sprintf(`
		SELECT COUNT(*) FROM agent_events
		WHERE %s
		  AND event_type = 'tool_call'
		LIMIT 1`, sessionFilter)
	return query, args
}

// sessionOrAgentFilter builds a SQL WHERE fragment that matches rows belonging
// to any of the related sessions OR (optionally) to the specific agentID.
// When agentID is used (i.e. useAgentID is true), additional scoping
// constraints are applied to ALL matched rows — including those matched via
// session_id IN (...):
//  1. Time window: only match events from the last 24 hours
//  2. Project scope (agent_id branch only): only match agent_id events whose
//     session_id belongs to the same project
//
// The 24h window is applied as an outer AND so that stale Reads in the
// current session don't bypass the freshness check just because the session
// happens to be long-running. The project scope is only enforced on the
// agent_id fallback branch — session lineage is project-authoritative.
func sessionOrAgentFilter(inClause string, inArgs []any, useAgentID bool, agentID, projectDir string) (string, []any) {
	if !useAgentID {
		return fmt.Sprintf("session_id IN %s", inClause), inArgs
	}

	// Build time window: 24 hours ago
	cutoffTime := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)

	// Build the agent_id branch with project scoping. The 24h time window is
	// applied as an outer AND below so it constrains both branches.
	agentBranch := "(agent_id IS NOT NULL AND agent_id != '' AND agent_id = ?"
	args := append([]any{}, inArgs...)
	args = append(args, agentID)
	if projectDir != "" {
		normalizedDir := paths.NormalizeProjectDir(projectDir)
		agentBranch += " AND session_id IN (SELECT session_id FROM sessions WHERE project_dir = ?)"
		args = append(args, normalizedDir)
	}
	agentBranch += ")"

	filter := fmt.Sprintf(
		"((session_id IN %s OR %s) AND created_at > ?)",
		inClause, agentBranch,
	)
	args = append(args, cutoffTime)
	return filter, args
}

// getSessionAndParent returns the current session ID plus its parent session
// ID (if any). Worktree subagents inherit context from the outer orchestrator
// session that spawned them.
//
// Callers that need full transitive lineage should use collectRelatedSessionIDs
// instead. This function is preserved for existing callers (e.g.
// getClaimFromParentChain) that only need one level of parent resolution.
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

// getClaimFromParentChain walks the parent session chain for sessionID and
// returns the work_item_id of the first active claim found on an ancestor
// session. Only walks when the current session has no claim of its own
// (claimedItem == ""). Returns "" when no ancestor claim is found.
//
// This allows sub-agent sessions to inherit the orchestrator's claim so that
// Write/Edit guards don't block agents dispatched by an orchestrator that ran
// `wipnote feature start`.
func getClaimFromParentChain(database *sql.DB, sessionID, claimedItem string) (string, string) {
	if claimedItem != "" || database == nil || sessionID == "" {
		return claimedItem, ""
	}
	// Walk the parent chain: check parent session for an active claim.
	sessionIDs := getSessionAndParent(database, sessionID)
	if len(sessionIDs) < 2 {
		return "", ""
	}
	activeList := "'proposed','claimed','in_progress','blocked','handoff_pending'"
	for _, sid := range sessionIDs[1:] { // skip current session (index 0)
		var inherited string
		query := fmt.Sprintf(`
			SELECT work_item_id FROM claims
			WHERE owner_session_id = ?
			  AND status IN (%s)
			ORDER BY leased_at DESC
			LIMIT 1`, activeList)
		database.QueryRow(query, sid).Scan(&inherited)
		if inherited != "" {
			return inherited, sid
		}
	}
	return "", ""
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
		// Skip data files — .wipnote/ HTML files are work items, not UI.
		if strings.Contains(lower, ".wipnote/") {
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
	if !yolo || !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
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
	// Exclude .wipnote/ work item HTML files — those are data, not UI.
	var uiFileCount int
	database.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		WHERE session_id = ? AND tool_name IN ('Write', 'Edit', 'MultiEdit')
		  AND (input_summary LIKE '%.html%' OR input_summary LIKE '%.css%'
		    OR input_summary LIKE '%.js%'  OR input_summary LIKE '%.ts%'
		    OR input_summary LIKE '%.tsx%' OR input_summary LIKE '%.vue%'
		    OR input_summary LIKE '%.svelte%')
		  AND input_summary NOT LIKE '%.wipnote/%'
		  AND status = 'completed'`,
		sessionID,
	).Scan(&uiFileCount)

	if uiFileCount == 0 {
		return "" // no UI files touched in this session
	}

	// Fix 3: check for screenshot / UI validation in session (+ parent).
	// Supported screenshot patterns:
	//   - tool_input contains "action":"screenshot" (Chrome MCP, including browser_batch)
	//   - tool_name matches *take_screenshot* or *screenshot* (other MCP server flavours)
	// This generalization covers browser_batch and other batch-style MCP tools that
	// nest screenshot actions inside tool_input rather than exposing them as top-level tool_name.
	for _, sid := range getSessionAndParent(database, sessionID) {
		var validationCount int
		database.QueryRow(`
			SELECT COUNT(*) FROM agent_events
			WHERE session_id = ?
			  AND (
			    -- Chrome MCP and batch tools: action discriminator in tool_input JSON.
			    tool_input LIKE '%"action":"screenshot"%'
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
