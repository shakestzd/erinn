package hooks

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// PreToolUse handles the PreToolUse Claude Code hook event.
// It inserts a tool_call agent_event row and allows the tool to proceed.
func PreToolUse(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	// Kill switch: HTMLGRAPH_GUARDS_OFF=1 disables ALL guards for emergency use.
	if os.Getenv("HTMLGRAPH_GUARDS_OFF") == "1" {
		return &HookResult{}, nil
	}

	ctx := resolveToolUseContext(event, database)
	if ctx == nil {
		return &HookResult{}, nil
	}

	// Guard: never intercept writes to .htmlgraph/ — mirror of
	// pretooluse-htmlgraph-guard.py to prevent accidental DB corruption.
	// Covers Write/Edit/MultiEdit tools AND Bash commands that target .htmlgraph/.
	if isHtmlGraphWrite(event) {
		return &HookResult{
			Decision: "block",
			Reason:   ".htmlgraph/ is managed by HtmlGraph SDK. Use SDK methods instead.",
		}, nil
	}
	if isBashHtmlGraphWrite(event) {
		return &HookResult{
			Decision: "block",
			Reason:   ".htmlgraph/ is managed by HtmlGraph CLI. Use `htmlgraph` commands instead of direct file manipulation.",
		}, nil
	}

	// Guard: block bare `cd` in Bash commands that pollute the working directory.
	if warn := checkBashCwdGuard(event); warn != "" {
		return &HookResult{
			Decision: "block",
			Reason:   warn,
		}, nil
	}

	// Guard: warn or block when CWD has drifted to a different project than the
	// one this session was started in.
	if result := checkProjectDivergence(event, database, ctx.SessionID); result != nil {
		return result, nil
	}

	// Plan mode bypass: when permission_mode is "plan", the agent is exploring
	// (Read/Grep/Glob) and writing only to the plan file. Skip work-item and
	// YOLO guards entirely — record the event for observability and allow.
	if event.PermissionMode == "plan" {
		debugLog(ctx.ProjectDir, "[htmlgraph] plan mode active — skipping write guards for %s",
			event.ToolName)
		return recordEventAndAllow(event, ctx, database)
	}

	// Guard: block Write/Edit/MultiEdit from subagents when THIS AGENT has no
	// active claim. Subagents are checked per-agent via claimed_by_agent_id in
	// the claims table (now supplied by the batch context query); the
	// orchestrator falls back to session-scoped FeatureID.
	// YOLO mode enforcement: subagents get a short grace period on session
	// start to claim a work item before guards fire — the parent session's
	// active feature serves as confirmation that the orchestrator has already
	// registered intent. This MUST run before the subagent work item guard
	// so that freshly spawned subagents aren't blocked before they can claim.
	subagentGrace := checkYoloSubagentGrace(
		ctx.IsYoloMode, ctx.IsSubagent,
		ctx.SessionCreatedAt, ctx.ParentSessionID, database,
	)
	if subagentGrace {
		debugLog(ctx.ProjectDir, "[htmlgraph] subagent grace period active for session %s — allowing write before claim",
			ctx.SessionID)
	}

	// Subagent work item guard: ensure subagents have claimed a work item.
	// Only enforced in YOLO mode — normal interactive subagents should not be
	// blocked by this guard (bug-ba6d1e1c).
	// Skipped during grace period (subagent just spawned, needs time to claim).
	hasAgentClaim := false
	if ctx.IsSubagent {
		hasAgentClaim = ctx.ClaimedItem != ""
	} else {
		hasAgentClaim = ctx.FeatureID != ""
	}
	if ctx.IsYoloMode && !subagentGrace {
		if warn := checkSubagentWorkItemGuard(event.ToolName, ctx.IsSubagent, hasAgentClaim, ctx.SessionID, ctx.IsYoloMode, ctx.FeatureID, ctx.ClaimedItem); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
	}

	// Always-on guards: work item and research required regardless of YOLO mode.
	// Skipped during subagent grace period (subagent just spawned, needs time to claim).
	if !subagentGrace {
		if warn := checkYoloWorkItemGuard(event.ToolName, ctx.FeatureID, ctx.IsYoloMode, ctx.SessionID, database); warn != "" {
			return &HookResult{
				Decision: "block",
				Reason:   warn,
			}, nil
		}
		// Extend work-item guard to Bash file-write commands (sed -i, rm, redirects, etc.).
		if warn := checkYoloBashWorkItemGuard(event, ctx.FeatureID, ctx.IsYoloMode, ctx.SessionID, database); warn != "" {
			return &HookResult{
				Decision: "block",
				Reason:   warn,
			}, nil
		}
		// Research-first: require at least one Read/Grep/Glob before writing.
		hasResearch := hasRecentResearch(database, ctx.SessionID)
		if warn := checkYoloResearchGuard(event.ToolName, ctx.IsYoloMode, hasResearch); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloBashResearchGuard(event, ctx.IsYoloMode, hasResearch); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
	}

	if ctx.IsYoloMode {
		// Warn (not block) when starting a work item without steps.
		if warn := checkYoloStepsGuard(event, ctx.IsYoloMode, ctx.HgDir); warn != "" {
			debugLog(ctx.ProjectDir, "[htmlgraph] YOLO steps warning: %s", warn)
		}

		// Resolve branch from the target file's worktree, not the session CWD.
		targetFile := extractFilePath(event.ToolInput)
		cwdBranch := currentBranchIn(event.CWD)
		branch := branchForFilePath(targetFile, cwdBranch)
		if warn := checkYoloWorktreeGuard(event.ToolName, branch, ctx.IsYoloMode); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		// Extend worktree guard to Bash file-write commands.
		if warn := checkYoloBashWorktreeGuard(event, branch, ctx.IsYoloMode); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		// Warn (not block) about code health — files already oversized should be
		// allowed to be edited so they can be refactored smaller.
		if warn := checkYoloCodeHealthGuard(event, ctx.IsYoloMode); warn != "" {
			debugLog(ctx.ProjectDir, "[htmlgraph] YOLO code health warning: %s", warn)
		}
		testRan := hasRecentTestRun(database, ctx.SessionID)
		if warn := checkYoloCommitGuard(event, ctx.IsYoloMode, testRan); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloDiffReviewGuard(event, ctx.IsYoloMode, hasRecentDiffReview(database, ctx.SessionID)); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloUIValidationGuard(event, ctx.IsYoloMode, database, ctx.SessionID); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloBudgetGuard(event, ctx.IsYoloMode); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloRoborevGuard(event, ctx.IsYoloMode); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}

		// Warn (not block) when the orchestrator writes directly instead of
		// delegating to a subagent (bug-06627817).
		if warn := checkYoloOrchestratorWriteGuard(event, ctx.IsSubagent); warn != "" {
			debugLog(ctx.ProjectDir, "[htmlgraph] YOLO orchestrator write warning: %s", warn)
		}
	}

	// Record the event and allow the tool to proceed.
	return recordEventAndAllow(event, ctx, database)
}

// recordEventAndAllow inserts a tool_call agent_event row for observability
// and returns an allow result. Used by the plan mode bypass and the normal
// flow to avoid duplicating the event recording logic.
func recordEventAndAllow(event *CloudEvent, ctx *toolUseContext, database *sql.DB) (*HookResult, error) {
	inputSummary := SummariseInput(event.ToolName, event.ToolInput)

	var toolInputStr string
	if event.ToolInput != nil {
		if b, err := json.Marshal(event.ToolInput); err == nil {
			toolInputStr = string(b)
		}
	}

	ev := &models.AgentEvent{
		EventID:       uuid.New().String(),
		AgentID:       ctx.AgentID,
		EventType:     models.EventToolCall,
		Timestamp:     time.Now().UTC(),
		ToolName:      event.ToolName,
		InputSummary:  inputSummary,
		ToolInput:     toolInputStr,
		SessionID:     ctx.SessionID,
		FeatureID:     ctx.FeatureID,
		ParentEventID: ctx.ParentEventID,
		SubagentType:  ctx.AgentType,
		Status:        "started",
		StepID:        event.ToolUseID,
		Source:        "hook",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	_ = db.InsertEvent(database, ev)

	if ctx.FeatureID != "" {
		_ = db.HeartbeatClaimByWorkItem(database, ctx.FeatureID, ctx.SessionID, 30*time.Minute)
	}
	_, _ = db.ReapExpiredClaims(database)

	os.Setenv("HTMLGRAPH_CURRENT_EVENT_ID", ev.EventID)

	// Capture the parent UserQuery event ID at PreToolUse time (before the tool
	// executes) and persist it to CLAUDE_ENV_FILE so PostToolUse reads the same
	// parent even when a new UserQuery has been inserted while the tool ran.
	// This eliminates the race in resolveParentEventID's LatestEventByTool fallback.
	if ctx.ParentEventID != "" {
		writeParentPromptEvent(ctx.ParentEventID)
	}

	return &HookResult{}, nil
}

// writeParentPromptEvent persists parentEventID as HTMLGRAPH_PARENT_PROMPT_EVENT
// to CLAUDE_ENV_FILE so PostToolUse hook processes can read the correct parent
// without querying the DB (which would return the wrong UserQuery when a new
// prompt has arrived since the tool started).
//
// Falls back to os.Setenv only (no-op for PostToolUse) when CLAUDE_ENV_FILE is
// unset — the existing LatestEventByTool DB fallback remains correct in that case.
func writeParentPromptEvent(parentEventID string) {
	// Keep the in-process env var current so any same-process callers see it.
	os.Setenv("HTMLGRAPH_PARENT_PROMPT_EVENT", parentEventID)

	envFile := os.Getenv("CLAUDE_ENV_FILE")
	if envFile == "" {
		// CLAUDE_ENV_FILE unset (YOLO mode, worktree subagents, or plugin-dir
		// launches). The in-process os.Setenv above covers same-process callers;
		// PostToolUse will fall through to the existing DB fallback chain.
		return
	}
	f, err := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "export HTMLGRAPH_PARENT_PROMPT_EVENT=%s\n", parentEventID)
}

// checkBashCwdGuard detects Bash commands that would permanently change the
// working directory. Bare `cd dir && cmd` pollutes CWD for all subsequent
// tool calls in the session. Subshells `(cd dir && cmd)` are safe.
//
// Returns a non-empty reason string to block the command, or "" to allow.
func checkBashCwdGuard(event *CloudEvent) string {
	if event.ToolName != "Bash" {
		return ""
	}
	cmd, _ := event.ToolInput["command"].(string)
	if cmd == "" {
		return ""
	}
	if !bareCdPattern.MatchString(cmd) {
		return ""
	}
	return "Bare `cd` changes the working directory permanently. " +
		"Use a subshell instead: `(cd dir && command)` — " +
		"this returns to the original directory when done."
}

// bareCdPattern matches a bare `cd` at the start of a command that is NOT
// wrapped in a subshell. It does NOT match:
//   - (cd dir && cmd)   — subshell, safe
//   - cd /absolute/path && pwd  — going to project root is fine... actually still bad
//
// It matches:
//   - cd some-dir && go build
//   - cd dir && cmd1 && cmd2
var bareCdPattern = regexp.MustCompile(`^cd\s+[^;)]+&&`)

// isHtmlGraphWrite returns true for file-write tools targeting .htmlgraph/.
func isHtmlGraphWrite(event *CloudEvent) bool {
	switch event.ToolName {
	case "Write", "Edit", "MultiEdit":
	default:
		return false
	}
	path, _ := event.ToolInput["path"].(string)
	if path == "" {
		path, _ = event.ToolInput["file_path"].(string)
	}
	return containsHtmlgraphDir(path)
}

func containsHtmlgraphDir(path string) bool {
	for i := range path {
		if path[i] == '.' && i+11 <= len(path) && path[i:i+11] == ".htmlgraph/" {
			return true
		}
	}
	return path == ".htmlgraph"
}

// isBashHtmlGraphWrite detects Bash commands that directly manipulate
// .htmlgraph/ files (rm, sed, echo/cat redirect, python -c, mv, cp, etc.).
// These bypass the structured Write/Edit tools and must be blocked.
func isBashHtmlGraphWrite(event *CloudEvent) bool {
	if event.ToolName != "Bash" {
		return false
	}
	cmd, _ := event.ToolInput["command"].(string)
	if cmd == "" {
		return false
	}
	// Skip commands that are HtmlGraph CLI invocations — those are allowed.
	if bashHtmlGraphCLI.MatchString(cmd) {
		return false
	}
	return bashHtmlGraphWritePattern.MatchString(cmd)
}

// bashHtmlGraphCLI matches commands that invoke the htmlgraph CLI binary.
// These are allowed since the CLI is the approved interface to .htmlgraph/.
var bashHtmlGraphCLI = regexp.MustCompile(`\bhtmlgraph\b`)

// bashHtmlGraphWritePattern matches Bash commands that write to .htmlgraph/.
// Covers: rm, sed -i, echo/cat/tee redirects (> or >>), mv, cp, python -c,
// touch, chmod, mkdir, and any other direct manipulation.
var bashHtmlGraphWritePattern = regexp.MustCompile(
	`(?:` +
		`\brm\s+.*\.htmlgraph/` +
		`|` +
		`\bsed\s+-i.*\.htmlgraph/` +
		`|` +
		`>[^&\s]\S*\.htmlgraph/` +
		`|` +
		`>>[^&\s]\S*\.htmlgraph/` +
		`|` +
		`\btee\s+\S*\.htmlgraph/` +
		`|` +
		`\bmv\s+.*\.htmlgraph/` +
		`|` +
		`\bcp\s+.*\.htmlgraph/` +
		`|` +
		`\btouch\s+\S*\.htmlgraph/` +
		`|` +
		`\bchmod\s+.*\.htmlgraph/` +
		`|` +
		`\bmkdir\s+.*\.htmlgraph/` +
		`|` +
		`\bpython[23]?\s+-c\s+.*\.htmlgraph/` +
		`)`,
)

// isBashFileWrite detects Bash commands that modify source files (as opposed
// to read-only commands like git status, ls, grep, etc.). Used by YOLO guards
// to extend Write/Edit/MultiEdit protections to Bash file manipulation.
func isBashFileWrite(event *CloudEvent) bool {
	if event.ToolName != "Bash" {
		return false
	}
	cmd, _ := event.ToolInput["command"].(string)
	if cmd == "" {
		return false
	}
	return bashFileWritePattern.MatchString(cmd)
}

// bashFileWritePattern matches Bash commands that write/modify files.
// Intentionally conservative — matches known destructive patterns only.
// The redirect branches use negative lookbehind for digits (to skip 2>/dev/null)
// and [^&\s] to avoid matching fd-to-fd redirects like 2>&1.
var bashFileWritePattern = regexp.MustCompile(
	`(?:` +
		`\bsed\s+-i` +
		`|` +
		`\bperl\s+-[pi]` +
		`|` +
		`\bawk\s+-i` +
		`|` +
		`(?:^|[^0-9])>[^&\s]\S*` +
		`|` +
		`(?:^|[^0-9])>>[^&\s]\S*` +
		`|` +
		`\brm\s` +
		`|` +
		`\bmv\s` +
		`|` +
		`\bpatch\s` +
		`|` +
		`\bdd\s` +
		`|` +
		`\bpython[23]?\s+-c\s+.*(?:open|write)` +
		`)`,
)

// SummariseInput builds a short human-readable summary of tool input.
func SummariseInput(toolName string, input map[string]any) string {
	if input == nil {
		return toolName
	}

	// Read tool: include offset/limit as line range suffix.
	if toolName == "Read" {
		return summariseReadInput(input)
	}

	// For file tools, use the path.
	for _, key := range []string{"path", "file_path", "command", "query", "prompt"} {
		if v, ok := input[key].(string); ok && v != "" {
			if len(v) > 120 {
				v = v[:120] + "…"
			}
			return v
		}
	}
	// Fallback: compact JSON of first 200 chars.
	b, _ := json.Marshal(input)
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// summariseReadInput builds a summary for the Read tool that includes the file
// path and optional line range from offset/limit parameters.
// Examples:
//
//	"/path/to/file.go"              — no offset/limit
//	"/path/to/file.go [100:150]"    — offset=100, limit=50
//	"/path/to/file.go [100:]"       — offset=100, no limit
//	"/path/to/file.go [:50]"        — no offset, limit=50
func summariseReadInput(input map[string]any) string {
	filePath := extractFilePath(input)
	if filePath == "" {
		return "Read"
	}

	offset := toInt(input["offset"])
	limit := toInt(input["limit"])

	if offset > 0 || limit > 0 {
		switch {
		case offset > 0 && limit > 0:
			filePath += fmt.Sprintf(" [%d:%d]", offset, offset+limit)
		case offset > 0:
			filePath += fmt.Sprintf(" [%d:]", offset)
		default:
			filePath += fmt.Sprintf(" [:%d]", limit)
		}
	}

	if len(filePath) > 120 {
		filePath = filePath[:120] + "…"
	}
	return filePath
}

// toInt converts a JSON number (float64) to int, returning 0 for non-numeric values.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// checkProjectDivergence compares the CWD of the current event against the
// project_dir stored in the session row. When they resolve to different
// .htmlgraph/ roots:
//   - Write tools are blocked with a clear error message.
//   - Read-only tools are silently allowed, but a warning is written to debug.log.
//
// Returns nil to allow the event to proceed.
func checkProjectDivergence(event *CloudEvent, database *sql.DB, sessionID string) *HookResult {
	if sessionID == "" || event.CWD == "" {
		return nil
	}

	sess, err := db.GetSession(database, sessionID)
	if err != nil || sess == nil || sess.ProjectDir == "" {
		// No stored project_dir — nothing to compare against.
		return nil
	}

	eventProjectDir := ResolveProjectDir(event.CWD, event.SessionID)
	sessionProjectDir := sess.ProjectDir

	if eventProjectDir == sessionProjectDir {
		return nil
	}

	// Normalise both paths to eliminate symlink / trailing-slash differences.
	cleanEvent := filepath.Clean(eventProjectDir)
	cleanSession := filepath.Clean(sessionProjectDir)
	if cleanEvent == cleanSession {
		return nil
	}

	if isWriteTool(event.ToolName) {
		return &HookResult{
			Decision: "block",
			Reason: fmt.Sprintf(
				"CWD has changed to a different project (%s). "+
					"Start a new session in that project.",
				eventProjectDir,
			),
		}
	}

	// Read-only tool: allow but log the drift.
	debugLog(sessionProjectDir, "[htmlgraph] CWD divergence (read-only %s): session=%s event_cwd=%s",
		event.ToolName, sessionProjectDir, event.CWD)
	return nil
}

// checkSubagentWorkItemGuard blocks Write/Edit/MultiEdit from subagents when
// no active work item is registered for THIS session. Returns a non-empty
// reason to block, or "" to allow.
//
// hasWorkItem must be derived from ctx.FeatureID (session-scoped), not from a
// global DB scan — a global check always passes on projects that have any
// in-progress item, defeating the guard entirely.
//
// Subagents ignore prompt-based instructions to register work items before
// writing code. Enforcing at the hook layer is the reliable alternative.
func checkSubagentWorkItemGuard(toolName string, isSubagent, hasWorkItem bool, sessionID string, isYoloMode bool, featureID, claimedItem string) string {
	if !isSubagent {
		return ""
	}
	switch toolName {
	case "Write", "Edit", "MultiEdit":
	default:
		return ""
	}
	if hasWorkItem {
		return ""
	}

	sess := sessionID
	if len(sess) > 8 {
		sess = sess[:8]
	}
	feat := featureID
	if feat == "" {
		feat = "none"
	}
	claim := claimedItem
	if claim == "" {
		claim = "none"
	}
	return fmt.Sprintf(
		"Write blocked: no claimed work item.\n"+
			"  session=%s yolo=%v subagent=%v\n"+
			"  feature=%s  claim=%s\n"+
			"To unblock: htmlgraph feature start <id>  (or: htmlgraph feature create \"...\" --track <trk-id>)",
		sess, isYoloMode, isSubagent,
		feat, claim,
	)
}

// isWriteTool returns true for tools that can modify the filesystem or execute
// arbitrary code. These are blocked when the CWD drifts to a different project.
func isWriteTool(toolName string) bool {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "Bash", "NotebookEdit", "Agent":
		return true
	}
	return false
}

