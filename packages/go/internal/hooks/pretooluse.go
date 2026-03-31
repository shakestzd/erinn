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
	ctx := resolveToolUseContext(event, database)
	if ctx == nil {
		return &HookResult{}, nil
	}

	// Guard: never intercept writes to .htmlgraph/ — mirror of
	// pretooluse-htmlgraph-guard.py to prevent accidental DB corruption.
	if isHtmlGraphWrite(event) {
		return &HookResult{
			Decision: "block",
			Reason:   ".htmlgraph/ is managed by HtmlGraph SDK. Use SDK methods instead.",
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

	// YOLO mode enforcement: check launch mode and apply guards.
	if warn := checkYoloWorkItemGuard(event.ToolName, ctx.FeatureID, ctx.IsYoloMode, ctx.SessionID, database); warn != "" {
		return &HookResult{
			Decision: "block",
			Reason:   warn,
		}, nil
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
		if warn := checkYoloResearchGuard(event.ToolName, ctx.IsYoloMode, hasRecentResearch(database, ctx.SessionID)); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloCodeHealthGuard(event, ctx.IsYoloMode); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
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
	}

	inputSummary := summariseInput(event.ToolName, event.ToolInput)

	// Serialize the full tool_input map to JSON for storage.
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

	// Ignore insert errors (FK violations are expected before session-start runs).
	_ = db.InsertEvent(database, ev)

	// Export event ID so posttooluse can link the result.
	os.Setenv("HTMLGRAPH_CURRENT_EVENT_ID", ev.EventID)

	// feature_files are rebuilt during reindex from git_commits — see reindexFeatureFiles().
	// Writing on every tool use was removed to keep the hot path lean; git history
	// captures all files touched by a feature more completely than hook interception.

	// Return empty object to allow. We use {} instead of {"decision":"allow"}
	// because Claude Code v2.1.x shows a spurious "hook error" label for
	// PreToolUse hooks that return {"decision":"allow"}.
	return &HookResult{}, nil
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
//   - cd packages/go && go build
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

// summariseInput builds a short human-readable summary of tool input.
func summariseInput(toolName string, input map[string]any) string {
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

// agentIDFromEnv returns the current agent ID.
func agentIDFromEnv() string {
	if v := os.Getenv("HTMLGRAPH_AGENT_ID"); v != "" {
		return v
	}
	return "claude-code"
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

	eventProjectDir := ResolveProjectDir(event.CWD)
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

// isWriteTool returns true for tools that can modify the filesystem or execute
// arbitrary code. These are blocked when the CWD drifts to a different project.
func isWriteTool(toolName string) bool {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "Bash", "NotebookEdit", "Agent":
		return true
	}
	return false
}

