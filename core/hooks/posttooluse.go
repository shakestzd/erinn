package hooks

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// PostToolUse handles the PostToolUse Claude Code hook event.
// It finds the most recent "started" event for this session/tool and marks it completed.
// Note: env vars don't persist between hook processes, so we query the DB instead.
func PostToolUse(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	// PostToolUse trusts WIPNOTE_PARENT_PROMPT_EVENT set by the paired PreToolUse
	// (trustParentEnvVar=true) to avoid the race where a new UserQuery arrives while
	// the tool was executing and the LatestEventByTool fallback would return the wrong parent.
	ctx := resolveToolUseContext(event, database, true)
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

	// Append event to session HTML activity log (non-critical, errors silently logged).
	AppendEventToSessionHTML(ctx.ProjectDir, ctx.SessionID, SessionEvent{
		Timestamp: time.Now().UTC(),
		ToolName:  event.ToolName,
		Success:   success,
		EventID:   eventID,
		FeatureID: ctx.FeatureID,
		Summary:   SummariseInput(event.ToolName, event.ToolInput),
	})

	// Lazy orphan sweep for this session — picks up any prior PreToolUse
	// that never saw a PostToolUse (tool crash, Claude Code kill) and
	// closes it out with a synthetic aborted entry. Non-critical.
	SweepOrphanedEventsForSession(database, ctx.ProjectDir, ctx.SessionID)

	// Record orchestrator direct-tool usage for analytics.
	// Subagents are excluded — only direct orchestrator use is interesting here.
	if !ctx.IsSubagent {
		// Orchestrator analytics removed — stderr caused "hook error" in Claude Code UI.
	}
	// Capture git commits and link to the active work item.
	if event.ToolName == "Bash" {
		if cmd := extractBashCommand(event.ToolInput); looksLikeGitCommit(cmd) {
			if hash, msg := parseGitCommitOutput(summarizeToolOutput(event.ToolResult)); hash != "" {
				featureID := ctx.FeatureID
				// Fallback: when session has no active work item, derive the ID from commit message.
				// Extract the first work-item ID from the commit message if present.
				// Only use this when ctx.FeatureID is empty (never override an active session attribution).
				if featureID == "" && msg != "" {
					ids := extractClosingIDs(msg)
					if len(ids) > 0 {
						featureID = ids[0]
					}
				}
				commit := &models.GitCommit{
					CommitHash:  hash,
					SessionID:   ctx.SessionID,
					FeatureID:   featureID,
					ToolEventID: eventID,
					Message:     msg,
					Timestamp:   time.Now().UTC(),
				}
				_ = db.InsertGitCommit(database, commit)
			}
		}
	}

	// Tag claims with agent ID when a subagent runs "wipnote feature start <id>".
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

	// Record file attribution when Edit/Write tools are used with an active feature.
	if ctx.FeatureID != "" {
		switch event.ToolName {
		case "Edit", "Write", "MultiEdit":
			if filePath := extractFilePath(event.ToolInput); filePath != "" {
				ff := &models.FeatureFile{
					ID:        ctx.FeatureID + "-" + filePathHash(filePath),
					FeatureID: ctx.FeatureID,
					FilePath:  filePath,
					Operation: strings.ToLower(event.ToolName),
					SessionID: ctx.SessionID,
				}
				_ = db.UpsertFeatureFile(database, ff)
			}
		}
	} else if event.ToolName == "Edit" || event.ToolName == "Write" || event.ToolName == "MultiEdit" {
		// Claimless visibility (feat-793844bd slice-4): previously this branch
		// only debugLog'd and DROPPED the attribution when there was no active
		// feature, making cross-session file work invisible. Instead record the
		// touch in the lightweight session_files ledger keyed on
		// (session_id,file_path). feature_files cannot hold these rows
		// (feature_id is NOT NULL with UNIQUE(feature_id,file_path)). This is
		// exactly ONE upsert — the only new write on the claimless hot path,
		// preserving the feat-156e0a1a zero-SQLITE_BUSY guarantee.
		if filePath := extractFilePath(event.ToolInput); filePath != "" {
			if err := db.UpsertSessionFile(database, ctx.SessionID, filePath, strings.ToLower(event.ToolName)); err != nil {
				debugLog(ctx.ProjectDir, "[posttooluse] session_files upsert failed for %s: %v", filePath, err)
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

		// Auto-complete work items when a branch merge completes.
		// "git merge trk-xxxxx" or "git merge feat-xxxxx" should close
		// all in-progress items on that track/feature.
		if cmd := extractBashCommand(event.ToolInput); looksLikeGitMerge(cmd) {
			if branch := extractMergeBranch(cmd); branch != "" {
				if completed := autoCompleteByBranch(branch, database); len(completed) > 0 {
					notice := fmt.Sprintf("Auto-completed (merge): %s", strings.Join(completed, ", "))
					if result.AdditionalContext != "" {
						result.AdditionalContext += "\n" + notice
					} else {
						result.AdditionalContext = notice
					}
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
// messages, e.g. "(feat-abc12345)" or "(bug-900f6655, #114)".
// Allows trailing tokens (e.g. issue numbers) inside the parens via [^)]*.
var commitParenRe = regexp.MustCompile(`\(\s*((?:feat|bug|spk)-[0-9a-f]{8})\b[^)]*\)`)

// extractClosingIDs parses a commit message for work item IDs that should be
// auto-completed. It recognises two patterns:
//  1. Closing keywords: "completes feat-abc123", "fixes bug-def456"
//  2. Parenthetical refs: "(feat-abc123)" — the existing wipnote convention
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

// completeIfInProgressFn is the implementation used by completeIfInProgress.
// It is a package-level variable so tests can inject a stub to avoid shelling
// out to the CLI binary (which requires a real on-disk DB at a known path).
var completeIfInProgressFn = completeIfInProgressImpl

// completeIfInProgress checks whether a work item is in-progress and, if so,
// shells out to the CLI to complete it. Returns true if completion was triggered.
func completeIfInProgress(id string, database *sql.DB) bool {
	return completeIfInProgressFn(id, database)
}

// completeIfInProgressImpl is the real implementation of completeIfInProgress.
//
// The status read goes through LookupWorkItem (feat-f6759e37): under a
// launcher-guaranteed session it is answered from canonical state by the
// daemon, and under no guarantee from the derived index exactly as before. A
// daemon failure under guarantee returns not-found here AND latches a breach,
// so this returns false without acting — and the hook's response is replaced
// with a loud block before that false is mistaken for "nothing to complete".
func completeIfInProgressImpl(id string, database *sql.DB) bool {
	item, found := LookupWorkItem(database, id)
	if !found || item.Status != "in-progress" {
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

// featureStartRe matches "wipnote (feature|bug|spike) start <id>" in a Bash command.
var featureStartRe = regexp.MustCompile(`wipnote\s+(?:feature|bug|spike)\s+start\s+(\S+)`)

// gitCommitOutputRe matches the commit line from git commit output, e.g.:
// "[main abc1234] commit message here"
var gitCommitOutputRe = regexp.MustCompile(`\[[\w/\-]+\s+([0-9a-f]{7,40})\]\s+(.*)`)

// looksLikeGitCommit returns true when the bash command appears to be a git commit.
func looksLikeGitCommit(cmd string) bool {
	return strings.Contains(cmd, "git commit") || strings.Contains(cmd, "git-commit")
}

// looksLikeGitMerge returns true when the bash command appears to be a git merge.
func looksLikeGitMerge(cmd string) bool {
	return strings.Contains(cmd, "git merge") || strings.Contains(cmd, "git-merge")
}

// gitMergeBranchRe matches the branch name in a git merge command.
// Handles: "git merge trk-abc123", "git merge --no-ff feat-abc123", etc.
var gitMergeBranchRe = regexp.MustCompile(`git[-\s]merge\s+(?:--\S+\s+)*(\S+)`)

// extractMergeBranch parses a git merge command and returns the branch being merged.
// Returns "" when the branch cannot be determined.
func extractMergeBranch(cmd string) string {
	// Find the last argument after flags (skip option flags starting with -)
	for _, line := range strings.Split(cmd, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "git merge") && !strings.Contains(line, "git-merge") {
			continue
		}
		if m := gitMergeBranchRe.FindStringSubmatch(line); len(m) == 2 {
			branch := m[1]
			// Filter out common flags that look like arguments
			if strings.HasPrefix(branch, "-") {
				return ""
			}
			return branch
		}
	}
	return ""
}

// workItemBranchRe matches a branch name that is itself a work item ID:
// feat-xxxxxxxx, bug-xxxxxxxx, spk-xxxxxxxx (8 hex chars).
var workItemBranchRe = regexp.MustCompile(`^((?:feat|bug|spk)-[0-9a-f]{8})$`)

// trackBranchRe matches a branch that is a track ID: trk-xxxxxxxx.
var trackBranchRe = regexp.MustCompile(`^(trk-[0-9a-f]{8})$`)

// autoCompleteByBranch completes in-progress work items based on a branch name.
// When the branch is a track ID (trk-xxxxxxxx), all in-progress features/bugs/spikes
// on that track are completed. When it is a direct work item ID, only that item
// is completed. Returns the IDs of completed items.
func autoCompleteByBranch(branch string, database *sql.DB) []string {
	// Direct work item branch: feat-xxxxxxxx, bug-xxxxxxxx, spk-xxxxxxxx
	if workItemBranchRe.MatchString(branch) {
		if completeIfInProgress(branch, database) {
			return []string{branch}
		}
		return nil
	}

	// Track branch: trk-xxxxxxxx — complete all in-progress items on this track
	if m := trackBranchRe.FindStringSubmatch(branch); len(m) == 2 {
		trackID := m[1]
		return completeInProgressByTrack(trackID, database)
	}

	return nil
}

// completeInProgressByTrack queries for all in-progress work items on a track
// and shells out to complete each one. Returns the IDs of completed items.
//
// The listing goes through ListWorkItems (feat-f6759e37) — canonical state via
// the daemon under a launcher guarantee, the derived index otherwise.
func completeInProgressByTrack(trackID string, database *sql.DB) []string {
	items := ListWorkItems(database, daemon.WorkItemListArgs{
		TrackID:  trackID,
		Statuses: []string{"in-progress"},
	})
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}

	var completed []string
	for _, id := range ids {
		if completeIfInProgress(id, database) {
			completed = append(completed, id)
		}
	}
	return completed
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

// filePathHash returns an 8-char hex digest of a file path, used to generate
// deterministic IDs for feature_files rows.
func filePathHash(filePath string) string {
	h := sha256.Sum256([]byte(filePath))
	return fmt.Sprintf("%x", h[:4])
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
