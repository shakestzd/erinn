package hooks

import (
	"database/sql"
	"encoding/json"
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
		return &HookResult{Decision: "allow"}, nil
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

	// YOLO mode enforcement: check launch mode and apply guards.
	projectDir := ResolveProjectDir(event.CWD)
	yolo := isYoloMode(filepath.Join(projectDir, ".htmlgraph"))

	if warn := checkYoloWorkItemGuard(event.ToolName, ctx.FeatureID, yolo); warn != "" {
		return &HookResult{
			Decision: "block",
			Reason:   warn,
		}, nil
	}

	if yolo {
		if warn := checkYoloWorktreeGuard(event.ToolName, currentBranchIn(event.CWD), yolo); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloResearchGuard(event.ToolName, yolo, hasRecentResearch(database, ctx.SessionID)); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloCodeHealthGuard(event, yolo); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		testRan := hasRecentTestRun(database, ctx.SessionID)
		if warn := checkYoloCommitGuard(event, yolo, testRan); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloDiffReviewGuard(event, yolo, hasRecentDiffReview(database, ctx.SessionID)); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloBudgetGuard(event, yolo); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
	}

	parentEventID := resolveParentEventID(database, ctx.SessionID, event.AgentID, ctx.IsSubagent)
	inputSummary := summariseInput(event.ToolName, event.ToolInput)

	// Resolve agent_type from CloudEvent, then env var.
	agentType := event.AgentType
	if agentType == "" {
		agentType = os.Getenv("HTMLGRAPH_AGENT_TYPE")
	}

	ev := &models.AgentEvent{
		EventID:       uuid.New().String(),
		AgentID:       ctx.AgentID,
		EventType:     models.EventToolCall,
		Timestamp:     time.Now().UTC(),
		ToolName:      event.ToolName,
		InputSummary:  inputSummary,
		SessionID:     ctx.SessionID,
		FeatureID:     ctx.FeatureID,
		ParentEventID: parentEventID,
		SubagentType:  agentType,
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

	result := &HookResult{Decision: "allow"}

	// Orchestrator-only checks: attribution warning + delegation reminder.
	if !ctx.IsSubagent {
		result.AdditionalContext = buildOrchestratorContext(event.ToolName, ctx.FeatureID)
	}

	return result, nil
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

// agentIDFromEnv returns the current agent ID.
func agentIDFromEnv() string {
	if v := os.Getenv("HTMLGRAPH_AGENT_ID"); v != "" {
		return v
	}
	return "claude-code"
}
