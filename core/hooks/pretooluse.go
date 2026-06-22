package hooks

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
)

// PreToolUse handles the PreToolUse Claude Code hook event.
// It inserts a tool_call agent_event row and allows the tool to proceed.
func PreToolUse(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	// Kill switch: WIPNOTE_GUARDS_OFF=1 disables ALL guards for emergency use.
	if os.Getenv("WIPNOTE_GUARDS_OFF") == "1" {
		return &HookResult{}, nil
	}

	// PreToolUse is the authority for parent event resolution — always re-resolve
	// from the DB (trustParentEnvVar=false) so stale env vars from prior tool calls
	// do not parent this tool call to the wrong prompt.
	ctx := resolveToolUseContext(event, database, false)
	if ctx == nil {
		return &HookResult{}, nil
	}

	// Guard: never intercept writes to .wipnote/ — mirror of
	// pretooluse-wipnote-guard.py to prevent accidental DB corruption.
	// Covers Write/Edit/MultiEdit tools AND Bash commands that target .wipnote/.
	if iswipnoteWrite(event) {
		return &HookResult{
			Decision: "block",
			Reason:   ".wipnote/ is managed by wipnote SDK. Use SDK methods instead.",
		}, nil
	}
	if isBashwipnoteWrite(event) {
		return &HookResult{
			Decision: "block",
			Reason:   ".wipnote/ is managed by wipnote CLI. Use `wipnote` commands instead of direct file manipulation.",
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
		debugLog(ctx.ProjectDir, "[wipnote] plan mode active — skipping write guards for %s",
			event.ToolName)
		return recordEventAndAllow(event, ctx, database)
	}

	orchestrationResearchAdvisory := checkOrchestratorResearchDelegationAdvisory(event, ctx, database)

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
		debugLog(ctx.ProjectDir, "[wipnote] subagent grace period active for session %s — allowing write before claim",
			ctx.SessionID)
	}

	// Subagent work item guard: ensure subagents have claimed a work item.
	// Only enforced in YOLO mode — normal interactive subagents should not be
	// blocked by this guard (bug-ba6d1e1c).
	// Skipped during grace period (subagent just spawned, needs time to claim).
	//
	// Parent-chain claim walk (feat-ecd82f68): when the sub-agent has no direct
	// claim, check the parent session chain. The orchestrator may have run
	// `wipnote feature start` and holds the claim under its session ID.
	claimedItem := ctx.ClaimedItem
	if ctx.IsSubagent && claimedItem == "" {
		inherited, parentSessID := getClaimFromParentChain(database, ctx.SessionID, claimedItem)
		if inherited != "" {
			claimedItem = inherited
			if ctx.FeatureID == "" {
				ctx.FeatureID = inherited
			}
			debugLog(ctx.ProjectDir, "[wipnote] claim inherited: session=%s parent=%s feat=%s",
				ctx.SessionID, parentSessID, inherited)
		}
	}
	hasAgentClaim := false
	if ctx.IsSubagent {
		hasAgentClaim = claimedItem != ""
	} else {
		hasAgentClaim = ctx.FeatureID != ""
	}
	if ctx.IsYoloMode && !subagentGrace {
		if warn := checkSubagentWorkItemGuard(event.ToolName, ctx.IsSubagent, hasAgentClaim, ctx.SessionID, ctx.IsYoloMode, ctx.FeatureID, claimedItem); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
	}

	// Guard: block sub-agent git commit on main/master.
	// Orchestrators (no parent) are allowed to commit on main intentionally.
	// This runs unconditionally — it is not gated on YOLO mode.
	if warn := checkSubagentCommitGuard(event, ctx.ParentSessionID, ctx.ProjectDir); warn != "" {
		return &HookResult{Decision: "block", Reason: warn}, nil
	}

	// Guard: block git commit when generator-input files are staged but the
	// generated plugin trees are stale (not regenerated via build-ports).
	// Always-on correctness gate — not YOLO-gated. Fast path: a single
	// `git diff --cached --name-only` call; CheckPorts only runs when a
	// generator-input file is actually staged. This replaces the expensive
	// per-turn CheckPorts that was previously called from the Stop hook
	// (bug-3fb22f7e).
	if warn := checkPortDriftCommitGuard(event); warn != "" {
		return &HookResult{Decision: "block", Reason: warn}, nil
	}

	// Always-on guards: work item and research required regardless of YOLO mode.
	// Skipped during subagent grace period (subagent just spawned, needs time to claim).
	if !subagentGrace {
		activeWorkItem := ctx.FeatureID
		if activeWorkItem == "" {
			activeWorkItem = claimedItem
		}
		// Extract the target file path once and reuse for both guards below.
		targetFile := extractFilePath(event.ToolInput)
		if warn := checkYoloWorkItemGuard(event.ToolName, activeWorkItem, ctx.IsYoloMode, ctx.SessionID, database, targetFile, ctx.ProjectDir); warn != "" {
			return &HookResult{
				Decision: "block",
				Reason:   warn,
			}, nil
		}
		// Research-first: require at least one Read/Grep/Glob before writing.
		hasResearch := hasRecentResearch(database, ctx.SessionID, ctx.AgentID, ctx.ProjectDir)
		if warn := checkYoloResearchGuard(event.ToolName, ctx.IsYoloMode, hasResearch, targetFile, ctx.ProjectDir); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		if warn := checkYoloBashResearchGuard(event, ctx.IsYoloMode, hasResearch); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
	}

	// Worktree-isolation guard runs under a RESILIENT yolo-context signal —
	// not just ctx.IsYoloMode (bug-0ed4e469). A coder subagent in a yolo run can
	// have its own IsYoloMode resolve false (Claude reports a non-bypass
	// permission_mode), yet a main/master-targeted edit must still be blocked.
	// The resilient signal is true when EITHER primary detection says yolo, OR
	// this is a subagent whose parent-session chain is yolo per isYoloFromDB.
	// This is deliberately narrow: it does NOT block main edits in genuinely
	// non-yolo sessions (plain `wipnote claude`, normal dev, or a top-level
	// session deliberately running on main) because those have no yolo ancestor.
	yoloContext := ctx.IsYoloMode ||
		(ctx.IsSubagent && anyParentSessionYolo(database, ctx.HgDir, ctx.SessionID))
	if yoloContext {
		// Resolve branch from the target file's worktree, not the session CWD.
		targetFile := extractFilePath(event.ToolInput)
		cwdBranch := currentBranchIn(event.CWD)
		branch := branchForFilePath(targetFile, cwdBranch)
		if warn := checkYoloWorktreeGuard(event.ToolName, branch, yoloContext); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
		// Extend worktree guard to Bash file-write commands.
		if warn := checkYoloBashWorktreeGuard(event, branch, yoloContext); warn != "" {
			return &HookResult{Decision: "block", Reason: warn}, nil
		}
	}

	if ctx.IsYoloMode {
		// Warn (not block) when starting a work item without steps.
		if warn := checkYoloStepsGuard(event, ctx.IsYoloMode, ctx.HgDir); warn != "" {
			debugLog(ctx.ProjectDir, "[wipnote] YOLO steps warning: %s", warn)
		}

		// Warn (not block) about code health — files already oversized should be
		// allowed to be edited so they can be refactored smaller.
		if warn := checkYoloCodeHealthGuard(event, ctx.IsYoloMode); warn != "" {
			debugLog(ctx.ProjectDir, "[wipnote] YOLO code health warning: %s", warn)
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
			debugLog(ctx.ProjectDir, "[wipnote] YOLO orchestrator write warning: %s", warn)
		}
	}

	// Tier 1 — live file-overlap advisory. Runs for Write/Edit/MultiEdit only.
	// A single indexed SELECT (idx_feature_files_path_seen) with ZERO writes:
	// it never acquires a write lock, so the feat-156e0a1a zero-SQLITE_BUSY
	// hot-path guarantee is preserved. Non-blocking by default
	// (warn_on_file_overlap=true); blocks with exit 2 + recovery hint only when
	// block_on_file_overlap=true.
	if advisory, blockErr := checkFileOverlapAdvisory(event, ctx, database); blockErr != nil {
		return nil, blockErr
	} else if advisory != "" {
		result, err := recordEventAndAllow(event, ctx, database)
		if err == nil && result != nil {
			appendAdditionalContext(result, orchestrationResearchAdvisory)
			appendAdditionalContext(result, advisory)
		}
		return result, err
	}

	// Record the event and allow the tool to proceed.
	result, err := recordEventAndAllow(event, ctx, database)
	if err == nil && result != nil {
		appendAdditionalContext(result, orchestrationResearchAdvisory)
	}
	return result, err
}

func appendAdditionalContext(result *HookResult, context string) {
	if result == nil || context == "" {
		return
	}
	if result.AdditionalContext != "" {
		result.AdditionalContext += "\n" + context
	} else {
		result.AdditionalContext = context
	}
}

func checkOrchestratorResearchDelegationAdvisory(event *CloudEvent, ctx *toolUseContext, database *sql.DB) string {
	if event == nil || ctx == nil || database == nil || ctx.IsSubagent || ctx.SessionID == "" {
		return ""
	}
	if !isOrchestratorResearchTool(event.ToolName) {
		return ""
	}
	counts, err := db.CountEventsByTool(database, ctx.SessionID)
	if err != nil {
		return ""
	}
	if hasPriorOrchestratorResearch(counts) || hasPriorDelegationEvidence(counts) || hasPriorSidecarCommand(database, ctx.SessionID) {
		return ""
	}
	return "wipnote orchestration advisory: root-session web/docs research is starting before any sidecar dispatch was recorded. In orchestrator mode, delegate web/docs research to a researcher/codebase sidecar when available. If this is a one-shot verification or no sidecar exists in this harness, continue and note that exception."
}

func isOrchestratorResearchTool(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "WebSearch", "WebFetch", "web_search", "web_fetch", "google_web_search",
		"web.search_query", "web.open", "web.find", "web.click":
		return true
	default:
		return false
	}
}

func hasPriorOrchestratorResearch(counts map[string]int) bool {
	for toolName, count := range counts {
		if count > 0 && isOrchestratorResearchTool(toolName) {
			return true
		}
	}
	return false
}

func hasPriorDelegationEvidence(counts map[string]int) bool {
	// TaskStarted is intentionally excluded: in the Codex harness it is a generic
	// progress checkpoint (roborev finding 266, bug-60107613), not a sidecar spawn.
	// Counting it as delegation evidence caused the research advisory to be wrongly
	// suppressed before the first root web/docs tool call in Codex sessions.
	// Only genuine spawn events are accepted as delegation evidence.
	for _, toolName := range []string{
		"Task", "Agent", "SubagentStart", "multi_agent.spawn",
		"multi_agent_v1.spawn_agent", "multi_agent_v1.spawn_agents",
	} {
		if counts[toolName] > 0 {
			return true
		}
	}
	return false
}

func hasPriorSidecarCommand(database *sql.DB, sessionID string) bool {
	var count int
	err := database.QueryRow(`
		SELECT COUNT(*) FROM agent_events
		WHERE session_id = ?
		  AND tool_name = 'Bash'
		  AND (
			input_summary LIKE 'gemini %'
			OR input_summary LIKE 'codex exec %'
			OR input_summary LIKE 'agy %'
			OR input_summary LIKE 'copilot %'
		  )
		LIMIT 1`, sessionID).Scan(&count)
	return err == nil && count > 0
}

// fileOverlapConfig holds the two opt-in flags read from .wipnote/config.json
// via the same local os.ReadFile pattern as readTaskCompletionConfig (there is
// NO shared internal/config package). warn defaults to true, block to false —
// so the JSON pointers distinguish "absent" (use default) from "explicitly
// set" (honor the value).
type fileOverlapConfig struct {
	WarnOnFileOverlap  *bool `json:"warn_on_file_overlap"`
	BlockOnFileOverlap *bool `json:"block_on_file_overlap"`
}

// readFileOverlapConfig returns (warn, block) for the live file-overlap
// advisory. Defaults: warn=true, block=false. A missing/unreadable config or
// absent key keeps the defaults; an explicit false in config disables warning.
func readFileOverlapConfig(projectDir string) (warn, block bool) {
	warn, block = true, false
	if projectDir == "" {
		return warn, block
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "config.json"))
	if err != nil {
		return warn, block
	}
	var cfg fileOverlapConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return warn, block
	}
	if cfg.WarnOnFileOverlap != nil {
		warn = *cfg.WarnOnFileOverlap
	}
	if cfg.BlockOnFileOverlap != nil {
		block = *cfg.BlockOnFileOverlap
	}
	return warn, block
}

// checkFileOverlapAdvisory performs the single-SELECT live file-overlap probe
// for Write/Edit/MultiEdit. Returns (advisory, nil) for the non-blocking warn
// path, ("", &BlockExit2Error) for the blocking path, or ("", nil) when there
// is no overlap or the feature is disabled. ZERO writes on every path.
func checkFileOverlapAdvisory(event *CloudEvent, ctx *toolUseContext, database *sql.DB) (string, error) {
	switch event.ToolName {
	case "Write", "Edit", "MultiEdit", "apply_patch":
	default:
		return "", nil
	}
	if ctx == nil || ctx.SessionID == "" || database == nil {
		return "", nil
	}
	warn, block := readFileOverlapConfig(ctx.ProjectDir)
	if !warn && !block {
		return "", nil
	}

	rawPath := extractFilePath(event.ToolInput)
	if rawPath == "" {
		return "", nil
	}
	// feature_files stores repo-relative paths (recordEventAndAllow uses the
	// same MustNormalize call), so normalize before matching.
	target := paths.MustNormalize(rawPath, "")

	window := db.FileOverlapWindow(ctx.ProjectDir)
	liveness := db.LivenessStalenessThreshold(ctx.ProjectDir)
	overlaps, err := db.FindLiveFileOverlaps(database, target, ctx.SessionID, window, liveness)
	if err != nil || len(overlaps) == 0 {
		return "", nil
	}

	var others []string
	for _, o := range overlaps {
		desc := o.SessionID
		if o.FeatureID != "" {
			desc += " (" + o.FeatureID + ")"
		}
		others = append(others, desc)
	}
	sessions := strings.Join(others, ", ")

	if block {
		return "", &BlockExit2Error{Message: fmt.Sprintf(
			"File-overlap block: %s was touched within the last %s by another "+
				"live session: %s.\n"+
				"Recovery: coordinate with the other session, or re-run after it "+
				"completes. To proceed anyway, set block_on_file_overlap=false in "+
				".wipnote/config.json (or export WIPNOTE_GUARDS_OFF=1 for an "+
				"emergency override).",
			target, window.String(), sessions),
		}
	}
	return fmt.Sprintf(
		"⚠ wipnote: %s was also touched within the last %s by another live "+
			"session: %s. You may be editing the same file concurrently — "+
			"coordinate to avoid clobbering each other's changes.",
		target, window.String(), sessions), nil
}

// recordEventAndAllow inserts a tool_call agent_event row for observability
// and returns an allow result. Used by the plan mode bypass and the normal
// flow to avoid duplicating the event recording logic.
func recordEventAndAllow(event *CloudEvent, ctx *toolUseContext, database *sql.DB) (*HookResult, error) {
	inputSummary := SummariseInput(event.ToolName, event.ToolInput)

	var toolInputStr string
	if event.ToolInput != nil {
		if b, err := json.Marshal(event.ToolInput); err == nil {
			// Normalize path fields to repo-relative before storage so that
			// captured artifacts remain stable across machines and worktrees.
			// Bash "command" fields are deliberately excluded (see normalizeToolInputPaths).
			toolInputStr = normalizeToolInputJSON(string(b), event.ToolName)
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

	// Route the tool_call agent_events INSERT through the daemon-first enqueue
	// path (plan-2390966a slice-4). This is the hot write that stalled 5–14s
	// under a held lock; RouteInsertEvent opens NO direct writable handle when
	// the daemon is reachable and degrades to a <1s bounded fallback otherwise.
	// Best-effort/advisory like the prior db.InsertEvent — never blocks the hook.
	_ = RouteInsertEvent("pretooluse", ctx.ProjectDir, ctx.SessionID, ev, database)

	// Claim bookkeeping (bug-d792aee6 finding 2): route BOTH claim writes through
	// the daemon-first enqueue-only seam (RouteHookWrite) instead of issuing them
	// directly on the 5s-busy_timeout hook handle. Under a held external write
	// lock the direct path stalled ~5s, defeating the <1s hot-hook bound; the
	// builders return the exact (sql, args) the originals Exec, JSON-transport-safe
	// so the daemon can re-bind them. Both stay best-effort: the advisory bool is
	// ignored, and canonical NDJSON + reindex are the durability backstop.
	if ctx.FeatureID != "" {
		writePath := paths.MustNormalize(extractFilePath(event.ToolInput), "")
		hbSQL, hbArgs := db.HeartbeatClaimByWorkItemStmt(ctx.FeatureID, ctx.SessionID, writePath, 30*time.Minute)
		_ = RouteHookWrite("pretooluse", ctx.ProjectDir, ctx.SessionID, hbSQL, hbArgs...)
	}
	reapSQL, reapArgs := db.ReapExpiredClaimsStmt()
	_ = RouteHookWrite("pretooluse", ctx.ProjectDir, ctx.SessionID, reapSQL, reapArgs...)

	os.Setenv("WIPNOTE_CURRENT_EVENT_ID", ev.EventID)

	// Capture the parent UserQuery event ID at PreToolUse time (before the tool
	// executes) and persist it to CLAUDE_ENV_FILE so PostToolUse reads the same
	// parent even when a new UserQuery has been inserted while the tool ran.
	// This eliminates the race in resolveParentEventID's LatestEventByTool fallback.
	if ctx.ParentEventID != "" {
		writeParentPromptEvent(ctx.ParentEventID)
	}

	return &HookResult{}, nil
}

// writeParentPromptEvent persists parentEventID as WIPNOTE_PARENT_PROMPT_EVENT
// to CLAUDE_ENV_FILE so PostToolUse hook processes can read the correct parent
// without querying the DB (which would return the wrong UserQuery when a new
// prompt has arrived since the tool started).
//
// Falls back to os.Setenv only (no-op for PostToolUse) when CLAUDE_ENV_FILE is
// unset — the existing LatestEventByTool DB fallback remains correct in that case.
func writeParentPromptEvent(parentEventID string) {
	// Keep the in-process env var current so any same-process callers see it.
	os.Setenv("WIPNOTE_PARENT_PROMPT_EVENT", parentEventID)

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
	fmt.Fprintf(f, "export WIPNOTE_PARENT_PROMPT_EVENT=%s\n", parentEventID)
}

// checkBashCwdGuard detects Bash commands that would permanently change the
// working directory. Bare `cd dir && cmd` pollutes CWD for all subsequent
// tool calls in the session. Subshells `(cd dir && cmd)` are safe.
//
// Returns a non-empty reason string to block the command, or "" to allow.
func checkBashCwdGuard(event *CloudEvent) string {
	if !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
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

// iswipnoteWrite returns true for file-write tools targeting .wipnote/.
func iswipnoteWrite(event *CloudEvent) bool {
	switch event.ToolName {
	case "Write", "Edit", "MultiEdit", "apply_patch":
	default:
		return false
	}
	if event.ToolName == "apply_patch" {
		patch, _ := event.ToolInput["patch"].(string)
		return containsWipnoteDir(patch)
	}
	path, _ := event.ToolInput["path"].(string)
	if path == "" {
		path, _ = event.ToolInput["file_path"].(string)
	}
	return containsWipnoteDir(path)
}

func containsWipnoteDir(path string) bool {
	for i := range path {
		if path[i] == '.' && i+11 <= len(path) && path[i:i+11] == ".wipnote/" {
			return true
		}
	}
	return path == ".wipnote"
}

// isBashwipnoteWrite detects Bash commands that directly manipulate
// .wipnote/ files (rm, sed, echo/cat redirect, python -c, mv, cp, etc.).
// These bypass the structured Write/Edit tools and must be blocked.
func isBashwipnoteWrite(event *CloudEvent) bool {
	if !isShellTool(event.ToolName) {
		return false
	}
	cmd := shellCommand(event.ToolInput)
	if cmd == "" {
		return false
	}
	// Skip commands that are wipnote CLI invocations — those are allowed.
	if isWipnoteCLICommand(cmd) {
		return false
	}
	return bashwipnoteWritePattern.MatchString(cmd)
}

// isWipnoteCLICommand returns true when every shell command segment invokes the
// wipnote CLI binary. The check is intentionally anchored to the executable
// token so commands that merely mention "wipnote" cannot bypass write guards.
func isWipnoteCLICommand(cmd string) bool {
	segments := splitShellCommandSegments(cmd)
	if len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		if !segmentStartsWithWipnoteCLI(segment) {
			return false
		}
	}
	return true
}

func splitShellCommandSegments(cmd string) []string {
	var segments []string
	start := 0
	for i := 0; i < len(cmd); i++ {
		sepLen := 0
		switch cmd[i] {
		case '\n', ';', '|':
			sepLen = 1
		case '&':
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				sepLen = 2
			}
		}
		if sepLen == 0 {
			continue
		}
		part := strings.TrimSpace(cmd[start:i])
		if part != "" {
			segments = append(segments, part)
		}
		i += sepLen - 1
		start = i + 1
	}
	part := strings.TrimSpace(cmd[start:])
	if part != "" {
		segments = append(segments, part)
	}
	return segments
}

func segmentStartsWithWipnoteCLI(segment string) bool {
	fields := strings.Fields(segment)
	for len(fields) > 0 {
		first := fields[0]
		if strings.Contains(first, "=") && !strings.HasPrefix(first, "-") {
			fields = fields[1:]
			continue
		}
		return first == "wipnote" || strings.HasSuffix(first, "/wipnote")
	}
	return false
}

// bashwipnoteWritePattern matches Bash commands that write to .wipnote/.
// Covers: rm, sed -i, echo/cat/tee redirects (> or >>), mv, cp, python -c,
// touch, chmod, mkdir, and any other direct manipulation.
var bashwipnoteWritePattern = regexp.MustCompile(
	`(?:` +
		`\brm\s+.*\.wipnote/` +
		`|` +
		`\bsed\s+-i.*\.wipnote/` +
		`|` +
		`\d?>\s*\S*\.wipnote/` +
		`|` +
		`\d?>>\s*\S*\.wipnote/` +
		`|` +
		`&>>?\s*\S*\.wipnote/` +
		`|` +
		`\btee\s+\S*\.wipnote/` +
		`|` +
		`\bmv\s+.*\.wipnote/` +
		`|` +
		`\bcp\s+.*\.wipnote/` +
		`|` +
		`\btouch\s+\S*\.wipnote/` +
		`|` +
		`\bchmod\s+.*\.wipnote/` +
		`|` +
		`\bmkdir\s+.*\.wipnote/` +
		`|` +
		`\bpython[23]?\s+-c\s+.*\.wipnote/` +
		`)`,
)

// isBashFileWrite detects Bash commands that modify source files (as opposed
// to read-only commands like git status, ls, grep, etc.). Used by YOLO guards
// to extend Write/Edit/MultiEdit protections to Bash file manipulation.
func isBashFileWrite(event *CloudEvent) bool {
	if !isShellTool(event.ToolName) {
		return false
	}
	cmd := shellCommand(event.ToolInput)
	if cmd == "" {
		return false
	}
	return bashFileWritePattern.MatchString(cmd)
}

func isShellTool(toolName string) bool {
	switch toolName {
	case "Bash", "exec_command", "functions.exec_command":
		return true
	}
	return false
}

func shellCommand(input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, key := range []string{"command", "cmd"} {
		if v, ok := input[key].(string); ok {
			return v
		}
	}
	return ""
}

// bashFileWritePattern matches Bash commands that write/modify files.
// Uses a write-intent denylist: known destructive commands are matched; pure
// inspection commands (ls, cat, head, tail, stat, find, grep, etc.) are not.
//
// Redirect detection:
//   - `(?:^|\s|;|&&|\|\|)>>?\s*[^&\s]` matches plain shell output redirects
//     (> and >>) preceded by a word boundary. Handles both `cmd > file` and `cmd >file`.
//   - `1>>?\s*[^\s]` matches explicit fd-1 (stdout) redirects: `1>file`, `1>>file`.
//     We target fd 1 specifically to avoid false-positives on benign `2>/dev/null`
//     patterns (the existing exclusion for `2>/dev/null`-shape stderr redirects is
//     preserved since we don't add a generic `[0-9]+>` pattern).
//   - `&>>?\s*[^\s]` matches `&>file` and `&>>file` (stdout+stderr combined redirect).
//     Excludes fd-to-fd `>&N` because the `&` must immediately precede `>`.
//   - fd-to-fd redirects like `>&2` are excluded because the existing pattern requires
//     a non-`&`, non-whitespace character after the redirect operator.
//   - `find ... -delete` is a destructive option that removes matching files.
var bashFileWritePattern = regexp.MustCompile(
	`(?:` +
		// In-place editors
		`\bsed\s+-i` +
		`|` +
		`\bperl\s+-[pi]` +
		`|` +
		`\bawk\s+-i` +
		`|` +
		// Shell output redirects (both > and >>), handling spaces around >
		`(?:^|\s|;|&&|\|\|)>>?\s*[^&\s]` +
		`|` +
		// Explicit fd-1 (stdout) redirects: 1>file, 1>>file
		// We use fd 1 specifically to avoid matching benign 2>/dev/null patterns.
		`1>>?\s*[^\s]` +
		`|` +
		// Combined stdout+stderr redirects: &>file and &>>file
		// Excludes >&N (fd-to-fd) because that form has > after &, not & before >.
		`&>>?\s*[^\s]` +
		`|` +
		// File removal / relocation / creation
		`\brm\s` +
		`|` +
		`\bcp\s` +
		`|` +
		`\bmv\s` +
		`|` +
		`\bln\s` +
		`|` +
		`\binstall\s` +
		`|` +
		// Permission / ownership changes
		`\bchmod\s` +
		`|` +
		`\bchown\s` +
		`|` +
		// Pipe-to-file writers
		`\btee\s` +
		`|` +
		`\bdd\s` +
		`|` +
		`\bpatch\s` +
		`|` +
		// Common formatter/linter commands that rewrite files in place.
		`\bgofmt\s+-w\b` +
		`|` +
		`\bgo\s+fmt\b` +
		`|` +
		`\bprettier\b.*\s--write\b` +
		`|` +
		`\beslint\b.*\s--fix\b` +
		`|` +
		`\bruff\b.*\s--fix\b` +
		`|` +
		`\bblack\b\s` +
		`|` +
		// Git write operations (add/commit/push modify index, history, or remote)
		`\bgit\s+(?:add|commit|push|reset|rebase|merge|mv|rm|stash|tag|cherry-pick)\b` +
		`|` +
		// Python one-liners that open files for writing
		`\bpython[23]?\s+-c\s+.*(?:open|write)` +
		`|` +
		// find -delete removes matching files
		`\bfind\b.*\s-delete\b` +
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
	for _, key := range []string{"path", "file_path", "command", "cmd", "query", "prompt"} {
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
// .wipnote/ roots:
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

	// Strip the "unresolved:" sentinel that NormalizeProjectDir adds when the
	// project root is a host-path with no anchor (typical in test tempdirs).
	// ResolveProjectDir returns raw paths; comparing must happen on the same
	// shape.
	sessionProjectDir = strings.TrimPrefix(sessionProjectDir, "unresolved:")
	eventProjectDir = strings.TrimPrefix(eventProjectDir, "unresolved:")

	if eventProjectDir == sessionProjectDir {
		return nil
	}

	// Normalise both paths to eliminate symlink / trailing-slash differences.
	cleanEvent := filepath.Clean(eventProjectDir)
	cleanSession := filepath.Clean(sessionProjectDir)
	if cleanEvent == cleanSession {
		return nil
	}

	// Worktree-aware: linked worktrees and their main repo share a .git
	// common dir and therefore the same logical project. Normalising both
	// paths through ResolveViaGitCommonDir collapses worktree↔main and
	// worktreeA↔worktreeB to the same canonical root. Without this check,
	// a YOLO session that records sess.ProjectDir=<main> in SessionStart
	// then fires events with WIPNOTE_PROJECT_DIR=<worktree> sees a false
	// divergence and blocks every Write/Edit (bug-a1993e6b — wedged
	// YOLO session on feat-5ddde9d7 / 2026-05-13).
	if canonicalRepoRoot(cleanEvent) == canonicalRepoRoot(cleanSession) {
		return nil
	}

	// Scratch-space bypass: if the raw event CWD has no .wipnote/ ancestor,
	// the user has moved to an untracked directory (e.g. /tmp). That is
	// legitimate scratch-space usage — do not block. We check event.CWD
	// directly (before ResolveProjectDir's walk-up logic) so that a scratch
	// path like /tmp/notes is not mistaken for the current process's project.
	// The guard only applies when both the session and the CWD are independently
	// wipnote-tracked projects.
	if !hasWipnoteAnchor(filepath.Clean(event.CWD)) {
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
	debugLog(sessionProjectDir, "[wipnote] CWD divergence (read-only %s): session=%s event_cwd=%s",
		event.ToolName, sessionProjectDir, event.CWD)
	return nil
}

// canonicalRepoRoot returns the main repository root for any path that lives
// inside the same logical project (whether the path is the main checkout or
// a linked worktree of it). Used by checkProjectDivergence so that worktrees
// don't trigger a false-positive "different project" block.
//
// Behaviour:
//   - Relative paths are first resolved to absolute via filepath.Abs so that
//     "." doesn't depend on the hook process's CWD at invocation time.
//   - For a linked worktree, paths.ResolveViaGitCommonDir returns the main
//     repo root (parent of the shared .git dir).
//   - For the main checkout (or any non-worktree path that already has its
//     own .git), ResolveViaGitCommonDir returns "" — in that case we treat
//     the absolute path itself as the canonical root after symlink resolution.
//
// Two paths in the same logical project produce the same canonical root via
// this helper; paths in unrelated projects produce different roots.
func canonicalRepoRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	if eval, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = eval
	}
	if main := paths.ResolveViaGitCommonDir(abs); main != "" {
		if eval, evalErr := filepath.EvalSymlinks(main); evalErr == nil {
			return eval
		}
		return main
	}
	return abs
}

// hasWipnoteAnchor reports whether dir or any of its parent directories
// contains a .wipnote/ subdirectory. Used by checkProjectDivergence to
// distinguish a "different wipnote project" (should block) from an untracked
// scratch directory like /tmp (should allow).
func hasWipnoteAnchor(dir string) bool {
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, ".wipnote")); err == nil {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return false
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
	case "Write", "Edit", "MultiEdit", "apply_patch":
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
			"To unblock: wipnote feature start <id>  (or: wipnote feature create \"...\" --track <trk-id>)",
		sess, isYoloMode, isSubagent,
		feat, claim,
	)
}

// isWriteTool returns true for tools that can modify the filesystem or execute
// arbitrary code. These are blocked when the CWD drifts to a different project.
func isWriteTool(toolName string) bool {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "apply_patch", "Bash", "exec_command", "functions.exec_command", "NotebookEdit", "Agent":
		return true
	}
	return false
}
