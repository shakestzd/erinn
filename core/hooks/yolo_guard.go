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

	"github.com/shakestzd/wipnote/core/agent"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/paths"
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
	// Fallback: the session's last recorded permission_mode, persisted by the
	// ConfigChange hook handler.
	return isYoloFromRecordedMode(wipnoteDir, event.SessionID)
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
	// Inherit from the session family root (subagents always reach here;
	// top-level sessions reach here only when they have no declared mode).
	// database is no longer consulted — ancestry comes from the canonical
	// session-family index, not the vanished sessions table.
	_ = database
	root := yoloFamilyRoot(wipnoteDir, sessionID)
	if root == "" {
		return false // no distinct ancestor
	}
	if isYoloFromRecordedMode(wipnoteDir, root) {
		debugLog(projectDir, "[wipnote] yolo inherited: session=%s parent=%s",
			sessionID, root)
		return true
	}
	return false
}

// anyParentSessionYolo returns true when sessionID's session-family root is in
// YOLO posture. Used as a resilient defense-in-depth signal for the
// worktree-isolation guard so that a yolo-context subagent is still blocked from
// editing main/master even if the primary IsYoloMode detection resolved false.
// Returns false on an empty session or when there is no distinct ancestor.
//
// database is accepted for call-shape compatibility and ignored: ancestry is
// read from the canonical session-family index (see yoloFamilyRoot).
func anyParentSessionYolo(database *sql.DB, wipnoteDir, sessionID string) bool {
	_ = database
	root := yoloFamilyRoot(wipnoteDir, sessionID)
	return root != "" && isYoloFromRecordedMode(wipnoteDir, root)
}

// permissionModeFileName is the per-session marker recording the last
// permission mode the harness reported for that session. It lives beside the
// existing .session-pid / .collector-pid anchors in
// .wipnote/sessions/<session-id>/, which is already the canonical home for
// per-session process facts (session_liveness.go).
//
// This replaces the sessions.metadata JSON column the ConfigChange hook used to
// UPDATE. That column lived only in the per-project SQLite read index; once the
// hook tree moved to a process-local in-memory projection (feat-fc3cc9e0) the
// write went to a throwaway database and every YOLO-posture read came back
// false — silently disarming the inheritance half of the guard. A file also
// satisfies the project's own hook rule: gates answer from durable file state,
// not from state that evaporates with the process.
const permissionModeFileName = ".permission-mode"

// permissionModePath is the marker path for one session. Empty when either
// component is missing, which every caller treats as "no record".
func permissionModePath(wipnoteDir, sessionID string) string {
	if wipnoteDir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(wipnoteDir, "sessions", sessionID, permissionModeFileName)
}

// RecordSessionPermissionMode durably records the harness-reported permission
// mode for sessionID. Best-effort by contract: the ConfigChange hook must never
// fail a session over a marker write, and a missing marker degrades to
// "no recorded posture" (not YOLO), which is the safe direction — guards stay
// ON rather than being skipped.
func RecordSessionPermissionMode(wipnoteDir, sessionID, mode string) error {
	path := permissionModePath(wipnoteDir, sessionID)
	if path == "" || strings.TrimSpace(mode) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(mode)+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// recordedSessionPermissionMode reads back the marker, or "" when absent.
func recordedSessionPermissionMode(wipnoteDir, sessionID string) string {
	path := permissionModePath(wipnoteDir, sessionID)
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// isYoloFromRecordedMode reports whether the session's last recorded permission
// mode was bypassPermissions. It is the fallback consulted when the live
// CloudEvent carries no permission_mode of its own.
func isYoloFromRecordedMode(wipnoteDir, sessionID string) bool {
	return recordedSessionPermissionMode(wipnoteDir, sessionID) == "bypassPermissions"
}

// yoloFamilyRoot returns the family-root session for sessionID when that root is
// a DIFFERENT session — i.e. sessionID is a descendant whose posture may be
// inherited. Empty when there is no distinct ancestor to inherit from.
//
// The session family index (.wipnote/session-families.json, written by
// SessionStart) is the canonical ancestry record now that sessions.parent_session_id
// is gone with the read index. For posture inheritance the family ROOT is the
// right ancestor to consult — the launch that established the YOLO posture in
// the first place — rather than one immediate parent hop.
func yoloFamilyRoot(wipnoteDir, sessionID string) string {
	if wipnoteDir == "" || sessionID == "" {
		return ""
	}
	root := agent.SessionFamilyFor(filepath.Dir(wipnoteDir), sessionID)
	if root == "" || root == sessionID {
		return ""
	}
	return root
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
func checkYoloWorkItemGuard(toolName, featureID string, _ bool, sessionID string, database *sql.DB, targetFile, projectRoot string) string {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "apply_patch":
	default:
		return ""
	}
	// Skip for writes targeting paths outside the project root.
	if targetFile != "" && pathIsOutsideProject(targetFile, projectRoot) {
		return ""
	}
	// Check 1: this session's own active work item (set at session-start or via
	// lineage attribution promoted into featureID at the call site).
	if featureID != "" {
		return ""
	}
	// Check 2: a feature started mid-session and linked to THIS exact session row.
	if sessionID != "" && database != nil && sessionHasLinkedFeature(database, sessionID) {
		return ""
	}
	// Check 3: parent-chain fallback for nested/subagent sessions. A subagent's
	// `wipnote bug start` writes active_feature_id to the orchestrator's session
	// row, not the subagent's own row, so checks 1-2 miss even though the work is
	// legitimately attributed. One hop only (getSessionAndParent) keeps this tied
	// to direct lineage and avoids the global "any in-progress item" false-pass.
	if sessionID != "" && database != nil && ancestorHasActiveWorkItem(database, sessionID) {
		return ""
	}
	msg := "An active work item is required before writing code. " +
		"Run: wipnote feature start <id>  or  wipnote feature create \"title\" --track <trk-id>"
	if sessionID != "" {
		msg += fmt.Sprintf("\nChecked session: %s", sessionID)
	}
	if database != nil {
		if claimSession, workItem := latestProjectClaim(database, projectRoot); claimSession != "" && workItem != "" {
			msg += fmt.Sprintf("\nNearest active claim: session=%s work_item=%s", claimSession, workItem)
		}
	}
	return msg
}

func latestProjectClaim(database *sql.DB, projectRoot string) (string, string) {
	if database == nil || projectRoot == "" {
		return "", ""
	}
	var sessionID, workItemID string
	err := database.QueryRow(`
		SELECT c.owner_session_id, c.work_item_id
		FROM claims c
		JOIN sessions s ON s.session_id = c.owner_session_id
		WHERE s.status = 'active'
		  AND COALESCE(s.project_dir, '') = ?
		  AND c.status IN ('proposed','claimed','in_progress','blocked','handoff_pending')
		ORDER BY c.leased_at DESC
		LIMIT 1`, projectRoot).Scan(&sessionID, &workItemID)
	if err != nil {
		return "", ""
	}
	return sessionID, workItemID
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

// ancestorHasActiveWorkItem returns true when sessionID's direct parent session
// has a non-empty active_feature_id pointing at an in-progress feature. This
// lets a nested/subagent session inherit the orchestrator's work-item context:
// subagents don't write their own sessions.active_feature_id, so a legitimately
// attributed edit would otherwise be false-blocked.
//
// One hop only (getSessionAndParent) — tied to direct lineage so it does NOT
// reintroduce the global "any in-progress work item" false-pass that the removed
// hasAnyActiveWorkItem caused. The JOIN to features enforces status =
// 'in-progress', matching GetToolUseContext semantics so a stale pointer to a
// completed feature does not pass. Returns false on nil DB / empty sessionID /
// no parent.
func ancestorHasActiveWorkItem(database *sql.DB, sessionID string) bool {
	if database == nil || sessionID == "" {
		return false
	}
	sessionIDs := getSessionAndParent(database, sessionID)
	if len(sessionIDs) < 2 {
		return false // no parent
	}
	for _, parentID := range sessionIDs[1:] {
		var count int
		database.QueryRow(`
			SELECT COUNT(*) FROM sessions s
			JOIN features f ON f.id = s.active_feature_id
			WHERE s.session_id = ?
			  AND f.status = 'in-progress'
			LIMIT 1`, parentID).Scan(&count)
		if count > 0 {
			return true
		}
	}
	return false
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

// isExternalTechEdit reports whether a Write/Edit target is a dependency
// manifest — the durable, low-false-positive signal that the edit introduces or
// changes an external technology dependency. Following the project's "prefer
// durable file/diff state over brittle session/substring state" hook rule,
// detection is keyed on the target file's basename rather than fuzzy
// library-name matching against prompt text. The canonical manifest set lives in
// paths.IsDependencyManifest, shared with the pre-commit and completion gates.
func isExternalTechEdit(targetFile string) bool {
	return paths.IsDependencyManifest(targetFile)
}

// applyPatchFileHeaderRe matches the file headers of a Codex apply_patch envelope
// (`*** Add File: <path>`, `*** Update File: <path>`, `*** Delete File: <path>`).
var applyPatchFileHeaderRe = regexp.MustCompile(`(?m)^\*\*\*\s+(?:Add|Update|Delete)\s+File:\s*(.+?)\s*$`)

// applyPatchMoveRe matches the rename target of an apply_patch update
// (`*** Move to: <path>`).
var applyPatchMoveRe = regexp.MustCompile(`(?m)^\*\*\*\s+Move\s+to:\s*(.+?)\s*$`)

// applyPatchTouchedPaths extracts every file path referenced by a Codex
// apply_patch payload. Codex bundles multiple file edits in one call, so the
// path lives in the patch body rather than a file_path field — without this the
// research guards would see an empty target and silently skip apply_patch
// manifest/harness edits (roborev #563/#566).
func applyPatchTouchedPaths(patch string) []string {
	if patch == "" {
		return nil
	}
	var paths []string
	for _, m := range applyPatchFileHeaderRe.FindAllStringSubmatch(patch, -1) {
		if p := strings.TrimSpace(m[1]); p != "" {
			paths = append(paths, p)
		}
	}
	for _, m := range applyPatchMoveRe.FindAllStringSubmatch(patch, -1) {
		if p := strings.TrimSpace(m[1]); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// editTargetPaths returns every file path a Write/Edit-class tool will modify.
// For apply_patch it parses the patch payload (paths are not in a file_path
// field); for Write/Edit/MultiEdit it returns the single extractFilePath result.
// Returns nil for non-edit tools or when no path can be resolved.
func editTargetPaths(event *CloudEvent) []string {
	if event == nil {
		return nil
	}
	if event.ToolName == "apply_patch" {
		patch, _ := event.ToolInput["patch"].(string)
		return applyPatchTouchedPaths(patch)
	}
	if p := extractFilePath(event.ToolInput); p != "" {
		return []string{p}
	}
	return nil
}

// firstPathMatching returns the first path satisfying pred, or "" if none do.
func firstPathMatching(paths []string, pred func(string) bool) string {
	for _, p := range paths {
		if pred(p) {
			return p
		}
	}
	return ""
}

// checkExternalTechResearchGuard requires at least one web/docs research call
// (WebSearch/WebFetch or `gh ...`) before a Write/Edit that modifies a
// dependency manifest. A plain local Read does NOT satisfy this gate: the
// always-on research guard (checkYoloResearchGuard) accepts any read, but
// external-technology changes demand current upstream docs/changelogs that a
// local read cannot provide (spk-0a982f70 root cause: one `cat go.mod` cleared
// the only always-on research gate, so web research was never actually
// required).
//
// Non-dependency-manifest edits keep the existing any-read behavior (this guard
// returns "" for them, so there is no regression). Writes outside the project
// root are skipped, mirroring checkYoloResearchGuard. Always enforced — not
// YOLO-gated.
func checkExternalTechResearchGuard(toolName string, hasWebResearch bool, targetFile, projectRoot string) string {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "apply_patch":
	default:
		return ""
	}
	if targetFile != "" && pathIsOutsideProject(targetFile, projectRoot) {
		return ""
	}
	if !isExternalTechEdit(targetFile) {
		return ""
	}
	if hasWebResearch {
		return ""
	}
	return "Research is required before changing dependencies. Editing " +
		filepath.Base(targetFile) + " adds or changes an external technology, so " +
		"verify current official docs/changelogs via the web (WebSearch/WebFetch, " +
		"`gh search`) this session before writing — a local file read does NOT " +
		"satisfy this gate. Emergency override: WIPNOTE_GUARDS_OFF=1."
}

// harnessContractPathSignals are path substrings that mark a file as encoding an
// AI-harness integration contract (Claude Code / Codex / Gemini). These schemas
// drift silently across vendor releases — a field set in an agent manifest or
// hook config may stop being honored with no error — so edits to them MUST be
// preceded by web research against current provider docs (see the "Monitoring
// Upstream Harnesses" mandate in CLAUDE.md). The list is intentionally narrow to
// keep false-positives low: only genuine harness-contract source files match.
var harnessContractPathSignals = []string{
	"plugin/agents/",            // agent frontmatter — per-harness schema contract
	"plugin/hooks/",             // hook event wiring
	"plugin-core/manifest.json", // hook event matrix + per-target output paths
	"pluginbuild/",              // per-harness plugin/agent generation logic
	"/prompts/system-prompt",    // harness system prompt
}

// isHarnessContractEdit reports whether a Write/Edit target is a harness-contract
// source file. Like isExternalTechEdit it keys on the durable target path rather
// than fuzzy prompt/content substring matching, per the project's hook-state
// rule (prefer file/diff state over brittle session/substring state).
func isHarnessContractEdit(targetFile string) bool {
	if targetFile == "" {
		return false
	}
	p := filepath.ToSlash(targetFile)
	for _, sig := range harnessContractPathSignals {
		if strings.Contains(p, sig) {
			return true
		}
	}
	return false
}

// checkHarnessContractResearchGuard BLOCKS a Write/Edit to a harness-contract
// file when no web/docs research has happened this session — the inverse of the
// non-blocking checkOrchestratorResearchDelegationAdvisory. Harness contracts
// (agent manifests, hook matrices, the plugin generator, system prompts) drift
// silently across vendor releases, so contract-touching work is escalated from
// an advisory nudge to a hard block until current provider docs are consulted
// (feat-ff62b911 / spk-0a982f70).
//
// Non-harness-contract edits return "" (no regression). Writes outside the
// project root are skipped, mirroring the other research guards. Always
// enforced — not YOLO-gated. False-positives are kept low by matching only the
// narrow harnessContractPathSignals set.
func checkHarnessContractResearchGuard(toolName string, hasWebResearch bool, targetFile, projectRoot string) string {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "apply_patch":
	default:
		return ""
	}
	if targetFile != "" && pathIsOutsideProject(targetFile, projectRoot) {
		return ""
	}
	if !isHarnessContractEdit(targetFile) {
		return ""
	}
	if hasWebResearch {
		return ""
	}
	return "Web research is required before harness-contract work. Editing " +
		filepath.Base(targetFile) + " changes a Claude Code / Codex / Gemini " +
		"integration contract, which drifts silently across vendor releases — " +
		"verify the current provider docs with WebSearch/WebFetch (or `gh search`) " +
		"this session before writing. A local file read does NOT satisfy this gate. " +
		"Emergency override: WIPNOTE_GUARDS_OFF=1."
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
var genericAgentIDs = []string{"claude-code", "claude", "codex", "gemini", "antigravity", "human"}

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

// hasRecentWebResearch reports whether web/docs research — WebSearch/WebFetch
// (and harness equivalents) or a `gh ...` Bash command — ran in this session,
// an ancestor session, or by the same agent. It is the web-only subset of
// hasRecentResearch: local reads (Read/Grep/Glob/cat/head/…) are deliberately
// EXCLUDED, so an external-technology edit cannot be satisfied by reading a
// local file (feat-868c752b / spk-0a982f70).
//
// It mirrors hasRecentResearch's fail-open behavior: when the event-recording
// pipeline is broken (zero tool_call events across all related IDs) it returns
// true rather than false-blocking valid work.
func hasRecentWebResearch(database *sql.DB, sessionID, agentID, projectDir string) bool {
	if database == nil || sessionID == "" {
		return true // fail-open: can't verify, don't block
	}

	relatedSIDs := collectRelatedSessionIDs(database, sessionID)
	inClause, inArgs := buildInClause(relatedSIDs)
	useAgentID := agentID != "" && !isGenericAgentID(agentID)

	webQuery, webArgs := buildWebResearchQuery(inClause, inArgs, useAgentID, agentID, projectDir)
	var webCount int
	database.QueryRow(webQuery, webArgs...).Scan(&webCount)
	if webCount > 0 {
		return true
	}

	// No web research recorded — distinguish a recording gap (fail-open) from a
	// genuine no-web-research case (block), using the same tool_call probe as
	// hasRecentResearch.
	toolCallQuery, toolCallArgs := buildToolCallQuery(inClause, inArgs, useAgentID, agentID, projectDir)
	var toolCallCount int
	database.QueryRow(toolCallQuery, toolCallArgs...).Scan(&toolCallCount)
	if toolCallCount == 0 {
		debugLog(projectDir,
			"[wipnote] web-research-gate fail-open: no tool_call events recorded for session=%s agent=%s — recording pipeline may be broken",
			sessionID, agentID)
		return true
	}

	// Tool calls ran but none were web/docs research → block.
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

// researchShellToolNamesSQL is the SQL IN-list fragment for all harness shell
// tool names that can carry research reads: Bash (Claude Code), exec_command
// and functions.exec_command (Codex), run_shell_command (Gemini),
// run_command (Antigravity — Antigravity translates run_shell_command to
// run_command in its agent manifests). Used in both buildResearchQuery and
// buildWebResearchQuery so the two stay in lockstep (issue #144).
const researchShellToolNamesSQL = `'Bash', 'exec_command', 'functions.exec_command', 'run_shell_command', 'run_command'`

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
				tool_name IN (`+researchShellToolNamesSQL+`) AND (
					input_summary LIKE 'ls %%' OR input_summary = 'ls'
					OR input_summary LIKE 'find %%'
					OR input_summary LIKE 'cat %%'
					OR input_summary LIKE 'grep %%'
					OR input_summary LIKE 'head %%'
					OR input_summary LIKE 'tail %%'
					OR input_summary LIKE 'stat %%'
					OR input_summary LIKE 'gh %%'
					OR input_summary LIKE 'curl %%'
					OR input_summary LIKE 'wipnote sh %%'
					OR input_summary LIKE 'wipnote search %%'
				)
			)
		  )
		LIMIT 1`, sessionFilter)
	return query, args
}

// buildWebResearchQuery builds the web/docs research detection SQL and its
// argument slice. It matches ONLY upstream-research tools — WebSearch/WebFetch
// (and the Codex/Gemini/Codex-browser equivalents) plus `gh ...` commands run
// through any supported shell tool — and deliberately EXCLUDES local reads
// (Read/Grep/Glob/cat/head/ls/find/…) so a dependency-manifest edit cannot be
// cleared by reading a local file (feat-868c752b).
//
// The web tool-name list is kept in lockstep with isOrchestratorResearchTool
// (which classifies the same tools as web/docs research), and the shell-tool
// list with isShellTool, so a session that researched via Codex's web.* tools or
// ran `gh search` through exec_command/functions.exec_command/run_shell_command
// is not falsely blocked (roborev #563/#566/#570, issue #144).
func buildWebResearchQuery(inClause string, inArgs []any, useAgentID bool, agentID, projectDir string) (string, []any) {
	sessionFilter, args := sessionOrAgentFilter(inClause, inArgs, useAgentID, agentID, projectDir)
	query := fmt.Sprintf(`
		SELECT COUNT(*) FROM agent_events
		WHERE %s
		  AND (
			tool_name IN (
				'WebSearch', 'WebFetch',
				'web_search', 'web_fetch', 'google_web_search',
				'web.search_query', 'web.open', 'web.find', 'web.click'
			) OR (
				tool_name IN (`+researchShellToolNamesSQL+`)
				AND input_summary LIKE 'gh %%'
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
	// Nil-DB-safe (roborev-478 finding 1): guard-only hook dispatch may pass a
	// nil DB; return just the current session (no parent walk) instead of
	// panicking on QueryRow.
	if database == nil {
		return sessionIDs
	}
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
	if database == nil {
		return false
	}
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
	if database == nil {
		return false
	}
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
	// Nil-DB-safe (roborev-478 finding 1): guard-only dispatch may pass a nil
	// DB. Without the derived index we cannot confirm UI work was screenshotted,
	// so fail-open (don't block) rather than panic on QueryRow.
	if database == nil {
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
