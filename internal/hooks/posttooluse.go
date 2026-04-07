package hooks

import (
	"database/sql"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// PostToolUse handles the PostToolUse Claude Code hook event.
// It finds the most recent "started" event for this session/tool and marks it completed.
// Note: env vars don't persist between hook processes, so we query the DB instead.
func PostToolUse(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	ctx := resolveToolUseContext(event, database)
	if ctx == nil {
		return &HookResult{Continue: true}, nil
	}

	success := isSuccess(event.ToolResult)
	status := "completed"
	if !success {
		status = "failed"
	}

	outputSummary := summariseToolOutput(event.ToolName, event.ToolInput, event.ToolResult, success)

	// For subagent events, scope the lookup to this specific agent to avoid
	// completing events belonging to a different concurrent agent.
	var (
		eventID string
		err     error
	)
	if ctx.IsSubagent {
		eventID, err = db.FindStartedEventByAgent(database, ctx.SessionID, event.ToolName, ctx.AgentID)
		if err != nil {
			// Fall back to unscoped lookup when no agent-specific event exists.
			eventID, err = db.FindStartedEvent(database, ctx.SessionID, event.ToolName)
		}
	} else {
		eventID, err = db.FindStartedEvent(database, ctx.SessionID, event.ToolName)
	}
	if err != nil {
		return &HookResult{Continue: true}, nil
	}

	_ = db.UpdateEventFields(database, eventID, status, outputSummary)

	// Record orchestrator direct-tool usage for analytics.
	// Subagents are excluded — only direct orchestrator use is interesting here.
	if !ctx.IsSubagent {
	// Orchestrator analytics removed — stderr caused "hook error" in Claude Code UI.
	}
	// Capture git commits and link to the active work item.
	if event.ToolName == "Bash" {
		if cmd := extractBashCommand(event.ToolInput); looksLikeGitCommit(cmd) {
			if hash, msg := parseGitCommitOutput(summarizeToolOutput(event.ToolResult)); hash != "" {
				commit := &models.GitCommit{
					CommitHash:  hash,
					SessionID:   ctx.SessionID,
					FeatureID:   ctx.FeatureID,
					ToolEventID: eventID,
					Message:     msg,
					Timestamp:   time.Now().UTC(),
				}
				_ = db.InsertGitCommit(database, commit)
			}
		}
	}

	// Tag claims with agent ID when a subagent runs "htmlgraph feature start <id>".
	// The CLI doesn't know the agent_id, but the PostToolUse hook sees both.
	if ctx.IsSubagent && event.ToolName == "Bash" {
		if cmd, ok := event.ToolInput["command"].(string); ok {
			if m := featureStartRe.FindStringSubmatch(cmd); len(m) > 1 {
				workItemID := m[1]
				if err := db.UpdateClaimAgentID(database, workItemID, event.AgentID); err == nil {
					debugLog(ctx.ProjectDir, "[posttooluse] tagged claim for %s with agent %s", workItemID, event.AgentID)
				}
			}
		}
	}

	result := &HookResult{Continue: true}

	// Quality gate: warn when Write/Edit/MultiEdit produces an oversized file.
	switch event.ToolName {
	case "Write", "Edit", "MultiEdit":
		if filePath := extractFilePath(event.ToolInput); filePath != "" {
			if warnings := CheckFileQuality(filePath); warnings != "" {
				result.AdditionalContext = warnings
			}
		}
	}

	// Auto-complete work items referenced in commit messages.
	if event.ToolName == "Bash" && isSuccess(event.ToolResult) {
		if cmd := extractBashCommand(event.ToolInput); looksLikeGitCommit(cmd) {
			if _, msg := parseGitCommitOutput(summarizeToolOutput(event.ToolResult)); msg != "" {
				if completed := autoCompleteFromCommit(msg, ctx, database); len(completed) > 0 {
					notice := fmt.Sprintf("Auto-completed: %s", strings.Join(completed, ", "))
					if result.AdditionalContext != "" {
						result.AdditionalContext += "\n" + notice
					} else {
						result.AdditionalContext = notice
					}
				}
			}
		}
	}

	// Merge completeness check: warn when merging a worktree branch that has
	// in-progress work items (feature, bug, spike, or features on a track).
	if event.ToolName == "Bash" && isSuccess(event.ToolResult) {
		if cmd := extractBashCommand(event.ToolInput); looksLikeGitMerge(cmd) {
			if warning := checkMergeCompleteness(cmd, database); warning != "" {
				if result.AdditionalContext != "" {
					result.AdditionalContext += "\n" + warning
				} else {
					result.AdditionalContext = warning
				}
			}
		}
	}

	return result, nil
}


// commitClosingRe matches closing keywords followed by a work item ID in commit messages.
// Supports: "completes feat-abc123", "closes bug-def456", "fixes spk-789abc",
// "resolves feat-abc123", and parenthetical form "(feat-abc123)".
// Case-insensitive matching is applied at call site via strings.ToLower.
var commitClosingRe = regexp.MustCompile(`(?:completes?|closes?|fix(?:es)?|resolves?)\s+((?:feat|bug|spk)-[0-9a-f]{8})`)

// commitParenRe matches parenthetical work item references at the end of commit
// messages, e.g. "(feat-abc12345)". This is the existing HtmlGraph convention.
var commitParenRe = regexp.MustCompile(`\(\s*((?:feat|bug|spk)-[0-9a-f]{8})\s*\)`)

// extractClosingIDs parses a commit message for work item IDs that should be
// auto-completed. It recognises two patterns:
//  1. Closing keywords: "completes feat-abc123", "fixes bug-def456"
//  2. Parenthetical refs: "(feat-abc123)" — the existing HtmlGraph convention
//
// Returns a deduplicated slice of work item IDs.
func extractClosingIDs(commitMsg string) []string {
	seen := map[string]bool{}
	var ids []string

	lower := strings.ToLower(commitMsg)
	for _, m := range commitClosingRe.FindAllStringSubmatch(lower, -1) {
		id := m[1]
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, m := range commitParenRe.FindAllStringSubmatch(lower, -1) {
		id := m[1]
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// autoCompleteFromCommit auto-completes work items referenced in a git commit
// message. It handles two modes:
//
//  1. Keyword/parenthetical mode (all sessions): when commit message contains
//     closing keywords or parenthetical work item refs, complete those items.
//  2. YOLO mode: also complete the session's active feature on any commit,
//     even without explicit references. When the current session has no
//     FeatureID (e.g. a worktree subagent whose orchestrator started the work
//     item), falls back to the parent session's active feature.
//
// Uses the selfBinary() + exec.Command() pattern to avoid importing workitem.
func autoCompleteFromCommit(commitMsg string, ctx *toolUseContext, database *sql.DB) []string {
	var completed []string

	// Mode 1: keyword/parenthetical references — works in all sessions.
	ids := extractClosingIDs(commitMsg)
	for _, id := range ids {
		if completeIfInProgress(id, database) {
			completed = append(completed, id)
		}
	}

	// Mode 2: YOLO auto-complete of active feature (no keywords needed).
	if ctx.IsYoloMode {
		featureID := ctx.FeatureID
		// Fallback: worktree subagents may have no FeatureID of their own when
		// the orchestrator's session started the work item. Check the parent.
		if featureID == "" && ctx.ParentSessionID != "" {
			featureID = db.GetActiveFeatureIDForSession(database, ctx.ParentSessionID)
		}
		if featureID != "" {
			// Don't double-complete if already handled above.
			alreadyDone := false
			for _, id := range completed {
				if id == featureID {
					alreadyDone = true
					break
				}
			}
			if !alreadyDone {
				if completeIfInProgress(featureID, database) {
					completed = append(completed, featureID)
				}
			}
		}
	}

	return completed
}

// completeIfInProgress checks whether a work item is in-progress and, if so,
// shells out to the CLI to complete it. Returns true if completion was triggered.
func completeIfInProgress(id string, database *sql.DB) bool {
	var status string
	if err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, id).Scan(&status); err != nil {
		return false
	}
	if status != "in-progress" {
		return false
	}
	typeName := inferTypeName(id)
	cmd := exec.Command(selfBinary(), typeName, "complete", id)
	if err := cmd.Run(); err != nil {
		debugLog("", "[posttooluse] auto-complete failed for %s: %v", id, err)
		return false
	}
	debugLog("", "[posttooluse] auto-completed %s", id)
	return true
}

// featureStartRe matches "htmlgraph (feature|bug|spike) start <id>" in a Bash command.
var featureStartRe = regexp.MustCompile(`htmlgraph\s+(?:feature|bug|spike)\s+start\s+(\S+)`)

// gitCommitOutputRe matches the commit line from git commit output, e.g.:
// "[main abc1234] commit message here"
var gitCommitOutputRe = regexp.MustCompile(`\[[\w/\-]+\s+([0-9a-f]{7,40})\]\s+(.*)`)

// looksLikeGitCommit returns true when the bash command appears to be a git commit.
func looksLikeGitCommit(cmd string) bool {
	return strings.Contains(cmd, "git commit") || strings.Contains(cmd, "git-commit")
}

// parseGitCommitOutput extracts the commit hash and message from git's stdout.
// Returns ("", "") when the output does not match the expected format.
func parseGitCommitOutput(output string) (hash, message string) {
	for _, line := range strings.Split(output, "\n") {
		if m := gitCommitOutputRe.FindStringSubmatch(strings.TrimSpace(line)); len(m) == 3 {
			return m[1], strings.TrimSpace(m[2])
		}
	}
	return "", ""
}

// extractBashCommand extracts the "command" field from a Bash tool_input map.
func extractBashCommand(input map[string]any) string {
	if input == nil {
		return ""
	}
	if v, ok := input["command"].(string); ok {
		return v
	}
	return ""
}

// summarizeToolOutput extracts the full output string from a tool result for
// commit parsing (we need more than the 200-char summariseOutput truncation).
func summarizeToolOutput(result map[string]any) string {
	if result == nil {
		return ""
	}
	for _, key := range []string{"output", "content", "result"} {
		if v, ok := result[key].(string); ok {
			return v
		}
	}
	return ""
}


// isSuccess returns false when the tool result contains an explicit error flag.
func isSuccess(result map[string]any) bool {
	if result == nil {
		return true
	}
	if v, ok := result["is_error"].(bool); ok && v {
		return false
	}
	return true
}

// summariseOutput extracts a short string from the tool result map.
func summariseOutput(result map[string]any) string {
	if result == nil {
		return ""
	}
	for _, key := range []string{"output", "content", "result", "error"} {
		if v, ok := result[key].(string); ok && v != "" {
			if len(v) > 200 {
				v = v[:200] + "…"
			}
			return v
		}
	}
	return ""
}

// summariseToolOutput builds a tool-specific structured output summary that
// captures key metadata (file path, success, content length) rather than raw
// output text. Falls back to summariseOutput for unrecognised tools.
func summariseToolOutput(toolName string, input map[string]any, result map[string]any, success bool) string {
	switch toolName {
	case "Read":
		return summariseReadOutput(input, result, success)
	case "Write":
		return summariseWriteOutput(input, success)
	case "Edit", "MultiEdit":
		return summariseEditOutput(input, success)
	case "Glob":
		return summariseGlobOutput(result, success)
	case "Grep":
		return summariseGrepOutput(result, success)
	default:
		return summariseOutput(result)
	}
}

func summariseReadOutput(input, result map[string]any, success bool) string {
	filePath := extractFilePath(input)
	if filePath == "" {
		filePath = "unknown"
	}
	if !success {
		return fmt.Sprintf("%s (error)", filePath)
	}
	// Count lines in content to report size.
	content := ""
	for _, key := range []string{"output", "content", "result"} {
		if v, ok := result[key].(string); ok {
			content = v
			break
		}
	}
	lines := countLines(content)
	return fmt.Sprintf("%s (ok, %d lines)", filePath, lines)
}

func summariseWriteOutput(input map[string]any, success bool) string {
	filePath := extractFilePath(input)
	if filePath == "" {
		filePath = "unknown"
	}
	if !success {
		return fmt.Sprintf("%s (error)", filePath)
	}
	return fmt.Sprintf("%s (written)", filePath)
}

func summariseEditOutput(input map[string]any, success bool) string {
	filePath := extractFilePath(input)
	if filePath == "" {
		filePath = "unknown"
	}
	if !success {
		return fmt.Sprintf("%s (error)", filePath)
	}
	return fmt.Sprintf("%s (edited)", filePath)
}

func summariseGlobOutput(result map[string]any, success bool) string {
	if !success {
		return "glob (error)"
	}
	content := ""
	for _, key := range []string{"output", "content", "result"} {
		if v, ok := result[key].(string); ok {
			content = v
			break
		}
	}
	n := countLines(content)
	return fmt.Sprintf("%d files matched", n)
}

func summariseGrepOutput(result map[string]any, success bool) string {
	if !success {
		return "grep (error)"
	}
	content := ""
	for _, key := range []string{"output", "content", "result"} {
		if v, ok := result[key].(string); ok {
			content = v
			break
		}
	}
	n := countLines(content)
	return fmt.Sprintf("%d matches", n)
}

// countLines returns the number of non-empty lines in s.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for i := range s {
		if s[i] == '\n' {
			n++
		}
	}
	// Don't count trailing newline as an extra line.
	if len(s) > 0 && s[len(s)-1] == '\n' {
		n--
	}
	return n
}
