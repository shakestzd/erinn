package hooks

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/worktree"
)

type sessionEndSnapshot struct {
	ActiveFeatureID  string
	ParentSessionID  string
	ProjectDir       string
	ExecWorktreePath string
	Branch           string
	Harness          string
	TranscriptPath   string
	LastUserQuery    string
	ExistingHandoff  string
	ExistingNext     string
	ExistingBlockers string
	ExistingContext  string
}

type sessionHandoffContext struct {
	WorkItemID       string   `json:"work_item_id,omitempty"`
	LastSessionID    string   `json:"last_session_id,omitempty"`
	ParentSessionID  string   `json:"parent_session_id,omitempty"`
	ProjectDir       string   `json:"project_dir,omitempty"`
	ExecWorktreePath string   `json:"exec_worktree_path,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	Harness          string   `json:"harness,omitempty"`
	TranscriptPath   string   `json:"transcript_path,omitempty"`
	EndReason        string   `json:"end_reason,omitempty"`
	LastUserQuery    string   `json:"last_user_query,omitempty"`
	FeaturesWorkedOn []string `json:"features_worked_on,omitempty"`
	Files            []string `json:"files,omitempty"`
	RecentActivity   []string `json:"recent_activity,omitempty"`
	SummarySources   []string `json:"summary_sources,omitempty"`
}

// SessionEnd handles the SessionEnd Claude Code hook event.
// It marks the session as completed and records the end commit.
//
// Cross-harness session-end coverage (feat-793844bd slice-4 part c):
//   - Claude Code: native SessionEnd event → this handler.
//   - Gemini CLI:  Stop event is mapped to geminiEventName "SessionEnd" with
//     geminiHandler "session-end" in packages/plugin-core/manifest.json, so
//     Gemini reaches this handler and releases claims on session exit.
//   - Codex CLI:   emits TaskComplete (its session-end-equivalent lifecycle
//     event); manifest.json wires TaskComplete → "session-end" for the codex
//     target, so Codex also reaches this handler.
//
// Honest liveness (db.SessionLivenessByHeartbeat) is the cross-harness safety
// net layered UNDERNEATH all three: even if a harness's session-end event
// never fires (crash, kill -9, network drop), a session whose newest claim
// heartbeat is stale is reported not-live and its lease is reaped — so
// liveness never depends on a session-end event arriving. This is why no
// invented Codex-specific hook is needed: TaskComplete already covers the
// graceful path, and heartbeat-recency + lease reap cover the abrupt path.
//
// Design: cheap critical writes run FIRST so that if Claude Code 2.1.156+
// cancels this handler mid-flight, the important state is already persisted.
// Steps that Stop already performs (FinalizeSessionHTML, backfillMissedUserPrompts,
// runSessionExitReconcile) are intentionally omitted here to avoid duplicated work.
func SessionEnd(event *CloudEvent, database *sql.DB, projectDir string) (*HookResult, error) {
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	endCommit := headCommit(projectDir)
	now := time.Now().UTC().Format(time.RFC3339)

	// --- CRITICAL WRITES FIRST (survive cancellation) ---

	// Mark session completed with end commit.
	_, err := database.Exec(`
		UPDATE sessions
		SET status = 'completed',
		    completed_at = ?,
		    end_commit = COALESCE(NULLIF(?, ''), end_commit)
		WHERE session_id = ?`,
		now, endCommit, sessionID,
	)
	if err != nil {
		debugLog(projectDir, "[error] handler=session-end session=%s: update sessions: %v", sessionID[:minLen(sessionID, 8)], err)
	}

	// Store transcript_path and termination reason if provided.
	if event.TranscriptPath != "" || event.Reason != "" {
		_, _ = database.Exec(`
			UPDATE sessions
			SET transcript_path = COALESCE(NULLIF(?, ''), transcript_path),
			    metadata = json_set(COALESCE(metadata, '{}'), '$.end_reason', ?)
			WHERE session_id = ?`,
			event.TranscriptPath, event.Reason, sessionID,
		)
	}

	// Release all active claims held by this session.
	if released, err := db.ReleaseAllClaimsForSession(database, sessionID); err != nil {
		debugLog(projectDir, "[error] handler=session-end session=%s: release claims: %v", sessionID[:minLen(sessionID, 8)], err)
	} else if released > 0 {
		debugLog(projectDir, "[wipnote] session-end: released %d claims for session %s", released, sessionID[:minLen(sessionID, 8)])
	}

	// --- SESSIONEND-UNIQUE STEPS (best-effort after critical writes) ---

	var featuresWorkedOn []string

	// Populate features_worked_on from distinct feature_ids in agent_events.
	if feats, fErr := db.DistinctFeatureIDs(database, sessionID); fErr == nil && len(feats) > 0 {
		featuresWorkedOn = feats
		if featsJSON, jErr := json.Marshal(feats); jErr == nil {
			database.Exec(`UPDATE sessions SET features_worked_on = ? WHERE session_id = ?`,
				string(featsJSON), sessionID)
		}
	}

	if err := persistSessionEndHandoff(database, sessionID, event, projectDir, featuresWorkedOn); err != nil {
		debugLog(projectDir, "[error] handler=session-end session=%s: persist handoff: %v", sessionID[:minLen(sessionID, 8)], err)
	}

	// Mark lineage trace complete so tree queries show accurate status.
	if err := db.CompleteLineageTrace(database, sessionID); err != nil {
		debugLog(projectDir, "[error] handler=session-end session=%s: complete lineage trace: %v", sessionID[:minLen(sessionID, 8)], err)
	}

	// Clean up the session-scoped project dir hint file now that this session is ending.
	paths.CleanupSessionHint(sessionID)

	// Signal the per-session OTel collector to drain and exit (Q3 primary layer)
	// BEFORE materializing — the indexer needs the final signals in SQLite first.
	// NOTE: Stop does NOT signal the collector; this step is SessionEnd-unique.
	signalCollector(projectDir, sessionID)

	// Wait briefly for the indexer to catch up with the final NDJSON writes.
	waitForIndexerCatchUp(projectDir, sessionID)

	// Materialize OTel rollup (no-op if no signals received for this session).
	// Non-fatal: errors are logged but do not block SessionEnd completion.
	// Injected (feat-331927fb) so core hooks don't import otel directly; a nil
	// fn means telemetry isn't wired and the rollup is simply skipped.
	if SessionMaterializeFn != nil {
		if err := SessionMaterializeFn(database, projectDir, sessionID); err != nil {
			debugLog(projectDir, "[error] handler=session-end session=%s: materialize otel: %v", sessionID[:minLen(sessionID, 8)], err)
		}
	}

	if err := cleanupEmptySpikeWorktreeOnSessionEnd(database, projectDir, sessionID); err != nil {
		debugLog(projectDir, "[error] handler=session-end session=%s: cleanup empty spike worktree: %v", sessionID[:minLen(sessionID, 8)], err)
	}

	// NOTE: FinalizeSessionHTML, backfillMissedUserPrompts, and
	// runSessionExitReconcile are intentionally absent here — Stop already
	// performs all three on every per-turn stop event, and port-drift
	// reconciliation now runs at commit-time (bug-3fb22f7e). Duplicating them
	// in SessionEnd wastes time and risks double-work when Claude Code cancels
	// this handler mid-flight.

	return &HookResult{Continue: true}, nil
}

func cleanupEmptySpikeWorktreeOnSessionEnd(database *sql.DB, projectDir, sessionID string) error {
	cfg := worktree.LoadCleanupConfig(projectDir)
	if !cfg.Enabled() {
		return nil
	}

	snapshot, err := loadSessionEndSnapshot(database, sessionID)
	if err != nil {
		return err
	}
	if inferTypeName(snapshot.ActiveFeatureID) != "spike" || snapshot.ExecWorktreePath == "" {
		return nil
	}
	// Guard: only clean up the worktree when its path actually belongs to
	// this spike. If the session ran in a shared track worktree (e.g.
	// ".claude/worktrees/trk-…"), the path will not contain the spike ID —
	// deleting it would destroy unrelated work.
	if !strings.Contains(snapshot.ExecWorktreePath, snapshot.ActiveFeatureID) {
		return nil
	}

	worktreePath := snapshot.ExecWorktreePath
	if !filepath.IsAbs(worktreePath) {
		worktreePath = filepath.Join(projectDir, filepath.FromSlash(worktreePath))
	}

	state, err := worktree.InspectCleanupState(projectDir, worktreePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if state.Locked {
		return nil
	}
	if !state.Removable() {
		_, snapErr := worktree.SnapshotPreservedWorktree(projectDir, worktreePath, snapshot.ActiveFeatureID, state)
		return snapErr
	}
	if !completeIfInProgress(snapshot.ActiveFeatureID, database) {
		return nil
	}
	return worktree.RemoveManagedWorktree(projectDir, worktreePath)
}

func persistSessionEndHandoff(database *sql.DB, sessionID string, event *CloudEvent, projectDir string, featuresWorkedOn []string) error {
	snapshot, err := loadSessionEndSnapshot(database, sessionID)
	if err != nil {
		return err
	}
	if snapshot.ActiveFeatureID == "" && len(featuresWorkedOn) > 0 {
		snapshot.ActiveFeatureID = featuresWorkedOn[0]
	}

	activities, blockers, err := recentSessionActivity(database, sessionID)
	if err != nil {
		return err
	}
	if snapshot.ActiveFeatureID == "" && snapshot.LastUserQuery == "" && len(activities) == 0 && len(blockers) == 0 {
		return nil
	}

	files, err := db.ListFilesBySession(database, sessionID)
	if err != nil {
		return err
	}
	filePointers := make([]string, 0, minLenInt(len(files), 5))
	for _, f := range files {
		if fp := normalizeSessionPointer(projectDir, snapshot.ProjectDir, f.FilePath); fp != "" {
			filePointers = append(filePointers, fp)
			if len(filePointers) == 5 {
				break
			}
		}
	}

	sources := make([]string, 0, 2)
	if snapshot.LastUserQuery != "" {
		sources = append(sources, "last_user_query")
	}
	if len(activities) > 0 {
		sources = append(sources, "agent_events")
	}

	handoffNotes := buildHandoffNotes(snapshot.ActiveFeatureID, snapshot.LastUserQuery, activities, blockers)
	recommendedNext := buildRecommendedNext(snapshot.ActiveFeatureID, snapshot.ExecWorktreePath, snapshot.Branch, snapshot.Harness, snapshot.LastUserQuery)
	context := sessionHandoffContext{
		WorkItemID:       snapshot.ActiveFeatureID,
		LastSessionID:    sessionID,
		ParentSessionID:  snapshot.ParentSessionID,
		ProjectDir:       snapshot.ProjectDir,
		ExecWorktreePath: snapshot.ExecWorktreePath,
		Branch:           snapshot.Branch,
		Harness:          snapshot.Harness,
		TranscriptPath:   snapshot.TranscriptPath,
		EndReason:        event.Reason,
		LastUserQuery:    snapshot.LastUserQuery,
		FeaturesWorkedOn: featuresWorkedOn,
		Files:            filePointers,
		RecentActivity:   activities,
		SummarySources:   sources,
	}

	var blockersJSON []byte
	if len(blockers) > 0 {
		blockersJSON, err = json.Marshal(blockers)
		if err != nil {
			return fmt.Errorf("marshal blockers: %w", err)
		}
	}

	contextJSON, err := json.Marshal(context)
	if err != nil {
		return fmt.Errorf("marshal recommended_context: %w", err)
	}
	if handoffNotes == "" && recommendedNext == "" && len(blockersJSON) == 0 && string(contextJSON) == "{}" {
		return nil
	}

	return db.UpdateSessionHandoff(database, sessionID, handoffNotes, recommendedNext, blockersJSON, contextJSON)
}

func loadSessionEndSnapshot(database *sql.DB, sessionID string) (*sessionEndSnapshot, error) {
	row := database.QueryRow(`
		SELECT
			COALESCE(active_feature_id, ''),
			COALESCE(parent_session_id, ''),
			COALESCE(project_dir, ''),
			COALESCE(exec_worktree_path, ''),
			COALESCE(branch, ''),
			COALESCE(harness, ''),
			COALESCE(transcript_path, ''),
			COALESCE(last_user_query, ''),
			COALESCE(handoff_notes, ''),
			COALESCE(recommended_next, ''),
			COALESCE(blockers, ''),
			COALESCE(recommended_context, '')
		FROM sessions
		WHERE session_id = ?`,
		sessionID,
	)

	var snapshot sessionEndSnapshot
	if err := row.Scan(
		&snapshot.ActiveFeatureID,
		&snapshot.ParentSessionID,
		&snapshot.ProjectDir,
		&snapshot.ExecWorktreePath,
		&snapshot.Branch,
		&snapshot.Harness,
		&snapshot.TranscriptPath,
		&snapshot.LastUserQuery,
		&snapshot.ExistingHandoff,
		&snapshot.ExistingNext,
		&snapshot.ExistingBlockers,
		&snapshot.ExistingContext,
	); err != nil {
		return nil, fmt.Errorf("load session handoff snapshot %s: %w", sessionID, err)
	}
	return &snapshot, nil
}

func recentSessionActivity(database *sql.DB, sessionID string) ([]string, []string, error) {
	rows, err := database.Query(`
		SELECT COALESCE(tool_name, ''),
		       COALESCE(input_summary, ''),
		       COALESCE(output_summary, ''),
		       COALESCE(status, '')
		FROM agent_events
		WHERE session_id = ?
		  AND COALESCE(tool_name, '') NOT IN ('', 'UserQuery')
		ORDER BY timestamp DESC
		LIMIT 6`,
		sessionID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("recent session activity %s: %w", sessionID, err)
	}
	defer rows.Close()

	activity := make([]string, 0, 4)
	blockers := make([]string, 0, 2)
	for rows.Next() {
		var toolName, inputSummary, outputSummary, status string
		if err := rows.Scan(&toolName, &inputSummary, &outputSummary, &status); err != nil {
			return nil, nil, fmt.Errorf("scan recent session activity %s: %w", sessionID, err)
		}
		summary := strings.TrimSpace(firstNonEmpty(inputSummary, outputSummary, toolName))
		if summary != "" {
			activity = append(activity, trimForHandoff(summary, 180))
		}
		if status == "failed" && outputSummary != "" {
			blockers = append(blockers, trimForHandoff(outputSummary, 300))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate recent session activity %s: %w", sessionID, err)
	}
	return activity, blockers, nil
}

func buildHandoffNotes(workItemID, lastUserQuery string, activities, blockers []string) string {
	parts := make([]string, 0, 4)
	if workItemID != "" {
		parts = append(parts, "Work item: "+workItemID+".")
	}
	if lastUserQuery != "" {
		parts = append(parts, "Last user request: "+trimForHandoff(lastUserQuery, 220))
	}
	if len(activities) > 0 {
		parts = append(parts, "Recent activity:\n- "+strings.Join(activities, "\n- "))
	}
	if len(blockers) > 0 {
		parts = append(parts, "Recent failure traces:\n- "+strings.Join(blockers, "\n- "))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func buildRecommendedNext(workItemID, execWorktreePath, branch, harness, lastUserQuery string) string {
	target := "Resume the last task"
	if workItemID != "" {
		target = "Resume " + workItemID
	}
	context := make([]string, 0, 3)
	if execWorktreePath != "" {
		context = append(context, execWorktreePath)
	}
	if branch != "" {
		context = append(context, "branch "+branch)
	}
	if harness != "" {
		context = append(context, "via "+harness)
	}
	if len(context) > 0 {
		target += " in " + strings.Join(context, " on ")
	}
	if lastUserQuery != "" {
		target += ". Start from: " + trimForHandoff(lastUserQuery, 180)
	}
	return target
}

func normalizeSessionPointer(projectDir, canonicalProjectDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	for _, base := range []string{projectDir, canonicalProjectDir} {
		if base == "" || !filepath.IsAbs(base) {
			continue
		}
		if rel, err := filepath.Rel(base, path); err == nil && rel != "" && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func trimForHandoff(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || len([]rune(s)) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func minLenInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// waitForIndexerCatchUp polls until .index-offset reaches events.ndjson size,
// or 2s elapses. Best-effort — if the indexer is behind, materialize will
// use whatever signals have been indexed so far.
func waitForIndexerCatchUp(projectDir, sessionID string) {
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	offsetPath := filepath.Join(sessDir, ".index-offset")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(ndjsonPath)
		if err != nil {
			return
		}
		data, err := os.ReadFile(offsetPath)
		if err == nil {
			if off, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil && off >= info.Size() {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// signalCollector reads the .collector-pid file for this session, sends SIGTERM,
// waits up to 3 seconds for a clean drain, then falls back to SIGKILL.
// All errors are silently logged — the collector PID file is best-effort.
func signalCollector(projectDir, sessionID string) {
	pidPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID, ".collector-pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		// No PID file — collector was never spawned or already cleaned up.
		return
	}
	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		debugLog(projectDir, "[session-end] collector-pid: invalid pid %q for session %s", pidStr, sessionID[:minLen(sessionID, 8)])
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		// Process not found (already exited).
		return
	}

	// Send SIGTERM to request graceful drain.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// ESRCH means process already gone — clean up PID file to prevent
		// stale PID reuse on later end/resume paths.
		_ = os.Remove(pidPath)
		return
	}
	debugLog(projectDir, "[session-end] sent SIGTERM to collector pid=%d (session=%s)", pid, sessionID[:minLen(sessionID, 8)])

	// Poll for up to 3s using kill(pid, 0) — we can't Wait() on a non-child.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			break // process exited
		}
		time.Sleep(100 * time.Millisecond)
	}
	// If still alive after 3s, escalate to SIGKILL.
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		debugLog(projectDir, "[session-end] collector drain timeout — sending SIGKILL pid=%d", pid)
		_ = proc.Signal(syscall.SIGKILL)
	}

	// Remove the PID file so future SessionEnd calls don't attempt to re-signal.
	_ = os.Remove(pidPath)
}

// SessionResume handles the SessionResume Claude Code hook event.
// It updates the session status back to active and refreshes env vars.
func SessionResume(event *CloudEvent, database *sql.DB, projectDir string) (*HookResult, error) {
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	if _, err := database.Exec(`
		UPDATE sessions
		SET status = 'active', completed_at = NULL
		WHERE session_id = ? AND status = 'completed'`,
		sessionID,
	); err != nil {
		debugLog(projectDir, "[error] handler=session-resume session=%s: update sessions: %v", sessionID[:minLen(sessionID, 8)], err)
	}

	// Re-export env vars so downstream hooks have the session ID.
	writeEnvVars(sessionID, projectDir)

	// Fetch active feature for context message.
	var featID sql.NullString
	_ = database.QueryRow(
		`SELECT active_feature_id FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&featID)

	msg := fmt.Sprintf("[wipnote] Session %s resumed.", sessionID[:minLen(sessionID, 8)])
	if featID.Valid && featID.String != "" {
		msg += fmt.Sprintf(" Active feature: %s", featID.String)
	}

	return &HookResult{Continue: true, AdditionalContext: msg}, nil
}

func minLen(s string, n int) int {
	if len(s) < n {
		return len(s)
	}
	return n
}

// --- Session-exit reconciliation (slice-5, feat-f93fe770) ---

// ReconcileReport is the structured result of a reconcile pass. It is consumed
// by both `wipnote reconcile` (CLI) and the Stop/SessionEnd hook handlers.
//
//   - AutoCommitted: done-but-uncommitted artifacts that were auto-committed
//     during this pass (deterministic bookkeeping — never blocks).
//   - PortDrift: generator-touched-without-build-ports paths reported by
//     the injected PortDriftPathsFn (see lifecycle_injection.go).
//   - Orphaned: in-progress work items with no live owning session (reported
//     only — never auto-resolved).
//
// HasAmbiguousDrift() is the single signal the harness discriminator keys on:
// when true and harness==claude the Stop handler returns BlockExit2Error; for
// Gemini/Codex a durable warning is persisted instead.
type ReconcileReport struct {
	AutoCommitted []string `json:"auto_committed,omitempty"`
	PortDrift     []string `json:"port_drift,omitempty"`
	Orphaned      []string `json:"orphaned,omitempty"`
}

// HasAmbiguousDrift reports whether the pass found unresolved source-ambiguous
// drift that a human must reconcile (generator-touched-without-build-ports).
// done-but-uncommitted items are NOT ambiguous — reconcile fixed them
// deterministically by auto-committing the artifact, so they never gate exit.
// Orphaned items are reported but are also not exit-gating (a session ending
// is the expected time to surface them, not to block on them).
func (r *ReconcileReport) HasAmbiguousDrift() bool {
	return r != nil && len(r.PortDrift) > 0
}

// Empty reports whether the pass found nothing actionable at all.
func (r *ReconcileReport) Empty() bool {
	return r == nil ||
		(len(r.AutoCommitted) == 0 && len(r.PortDrift) == 0 && len(r.Orphaned) == 0)
}

// reconcileArtifactCommitFn is the injection seam for the deterministic
// artifact auto-commit. Production wires it to a git-add+commit of the single
// work-item HTML path. Tests override it to assert the call without mutating a
// real repo. It returns true when a new commit was actually created.
var reconcileArtifactCommitFn = defaultReconcileArtifactCommit

// Reconcile runs a full session-exit reconciliation pass against projectDir.
//
// strict only affects the CLI surface (exit code); the detection itself is
// identical. The three classes are:
//
//  1. done-but-uncommitted → auto-commit the artifact (deterministic
//     bookkeeping) and record it under AutoCommitted. This CANNOT strand a
//     later `wipnote feature complete`: slice-4 gate records are session-local
//     and re-checked at complete, and the complete path's own strict-commit is
//     idempotent on an already-committed unchanged artifact (returns no-op,
//     HEAD must-not-advance branch) — so a reconcile pre-commit is forward
//     compatible.
//  2. generator-touched-without-build-ports → delegated to the injected
//     PortDriftPathsFn (see lifecycle_injection.go; feat-29195f33).
//     Skipped when skipPortDrift is true (per-turn Stop path).
//  3. started-but-orphaned → reported only.
func Reconcile(database *sql.DB, projectDir string, strict bool, skipPortDrift ...bool) (*ReconcileReport, error) {
	_ = strict // detection is identical; strict only changes CLI exit semantics
	rep := &ReconcileReport{}

	if database != nil {
		rep.AutoCommitted = reconcileDoneButUncommitted(database, projectDir)
		rep.Orphaned = reconcileStartedButOrphaned(database, projectDir)
	}
	if len(skipPortDrift) == 0 || !skipPortDrift[0] {
		rep.PortDrift = reconcilePortDrift(projectDir)
	}

	sort.Strings(rep.AutoCommitted)
	sort.Strings(rep.PortDrift)
	sort.Strings(rep.Orphaned)
	return rep, nil
}

// reconcileDoneButUncommitted finds work items in a terminal "done" state whose
// canonical artifact (.wipnote/<type>s/<id>.html) is dirty in git, and
// auto-commits each one. The auto-commit is deterministic bookkeeping: the
// "done" decision was already made by the agent; we are only persisting the
// durable record it forgot to commit. Returns the list of committed item IDs.
func reconcileDoneButUncommitted(database *sql.DB, projectDir string) []string {
	repoRoot := reconcileRepoRoot(projectDir)
	if repoRoot == "" {
		return nil
	}
	wipnoteDir := filepath.Join(repoRoot, ".wipnote")

	var committed []string
	for _, status := range []string{"done", "ended"} {
		feats, err := db.ListFeaturesByStatus(database, status, 500)
		if err != nil {
			continue
		}
		for _, f := range feats {
			sub := f.Type + "s"
			rel := filepath.Join(".wipnote", sub, f.ID+".html")
			abs := filepath.Join(wipnoteDir, sub, f.ID+".html")
			if !reconcilePathDirty(repoRoot, abs) {
				continue
			}
			if reconcileArtifactCommitFn(repoRoot, abs, rel, f.ID) {
				committed = append(committed, f.ID)
			}
		}
	}
	return committed
}

// reconcileStartedButOrphaned reports in-progress work items whose owning
// session is no longer active (the agent started the item, then the session
// ended without completing it). Reported only — never auto-resolved, because
// silently re-opening or completing an in-flight item would corrupt state.
func reconcileStartedButOrphaned(database *sql.DB, _ string) []string {
	rows, err := database.Query(`
		SELECT f.id
		FROM features f
		WHERE f.status IN ('in-progress', 'active')
		  AND NOT EXISTS (
		      SELECT 1 FROM sessions s
		      WHERE s.active_feature_id = f.id
		        AND s.status = 'active'
		  )
		ORDER BY f.id`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var orphaned []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			orphaned = append(orphaned, id)
		}
	}
	return orphaned
}

// reconcilePortDrift delegates to the injected PortDriftPathsFn
// (feat-29195f33). The checker owns the manifest-presence gate and the
// regenerate-and-compare; core never reimplements port diffing. Returns
// the drifted paths, or nil when in sync / not a plugin-core repo.
func reconcilePortDrift(projectDir string) []string {
	// Delegated to the injected PortDriftPathsFn so this core reconcile path
	// does not import port-generation tooling. A nil fn (tooling not wired)
	// means no drift to reconcile.
	if PortDriftPathsFn == nil {
		return nil
	}
	return PortDriftPathsFn(reconcileRepoRoot(projectDir))
}

// reconcileRepoRoot walks up from projectDir to the directory containing a
// .wipnote/ store, treating that as the repo root the artifacts live under.
func reconcileRepoRoot(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	for d := abs; ; {
		if fi, err := os.Stat(filepath.Join(d, ".wipnote")); err == nil && fi.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// reconcilePathDirty reports whether the single path has uncommitted or
// untracked changes (`git status --porcelain -- <path>` non-empty).
func reconcilePathDirty(repoRoot, absPath string) bool {
	out, err := exec.Command(
		"git", "-C", repoRoot, "status", "--porcelain", "--", absPath,
	).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// defaultReconcileArtifactCommit stages and commits exactly the one artifact
// path. Mirrors cmd/wipnote/workitem_commit.go's non-fatal contract: a failure
// to commit is logged and treated as "not committed" — reconcile never makes
// session exit depend on git succeeding. Returns true iff a new commit landed.
func defaultReconcileArtifactCommit(repoRoot, absPath, relPath, id string) bool {
	if out, err := exec.Command("git", "-C", repoRoot, "add", "--", absPath).CombinedOutput(); err != nil {
		debugLog(repoRoot, "[reconcile] git add %s failed: %s", relPath, strings.TrimSpace(string(out)))
		return false
	}
	// Nothing staged → already committed and unchanged: idempotent no-op.
	if err := exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet", "--", absPath).Run(); err == nil {
		return false
	}
	msg := "wipnote: reconcile " + id
	if out, err := exec.Command(
		"git", "-C", repoRoot, "commit", "-m", msg, "--", absPath,
	).CombinedOutput(); err != nil {
		o := string(out)
		if strings.Contains(o, "nothing to commit") || strings.Contains(o, "no changes added") {
			return false
		}
		debugLog(repoRoot, "[reconcile] git commit %s failed: %s", id, strings.TrimSpace(o))
		return false
	}
	return true
}

// --- Durable warnings (Gemini/Codex non-blocking surface) ---

// reconcileWarning is one persisted warning record. Persisted to
// .wipnote/.reconcile-warnings.jsonl so the user-never-returns case is still
// recorded, then rendered (and consumed) at the next SessionStart.
type reconcileWarning struct {
	Timestamp string   `json:"timestamp"`
	Harness   string   `json:"harness"`
	SessionID string   `json:"session_id,omitempty"`
	PortDrift []string `json:"port_drift,omitempty"`
	Orphaned  []string `json:"orphaned,omitempty"`
}

// reconcileWarningsPath is the durable warnings log under .wipnote/.
func reconcileWarningsPath(projectDir string) string {
	return filepath.Join(projectDir, ".wipnote", ".reconcile-warnings.jsonl")
}

// persistReconcileWarning appends a durable warning for the Gemini/Codex path.
// It is append-only JSONL so concurrent sessions never corrupt each other and
// a warning survives even if the user never returns to this session.
func persistReconcileWarning(projectDir, harness, sessionID string, rep *ReconcileReport) error {
	if rep == nil || (!rep.HasAmbiguousDrift() && len(rep.Orphaned) == 0) {
		return nil
	}
	w := reconcileWarning{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Harness:   harness,
		SessionID: sessionID,
		PortDrift: rep.PortDrift,
		Orphaned:  rep.Orphaned,
	}
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(reconcileWarningsPath(projectDir),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// DrainReconcileWarnings reads and removes the durable warnings log, returning
// a human-readable block to surface at SessionStart, or "" when there are
// none. Consuming (deleting) the log makes the surface idempotent: the next
// session does not re-show stale warnings the user has already seen.
func DrainReconcileWarnings(projectDir string) string {
	path := reconcileWarningsPath(projectDir)
	data, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return ""
	}
	_ = os.Remove(path)

	var lines []string
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var w reconcileWarning
		if err := json.Unmarshal([]byte(ln), &w); err != nil {
			continue
		}
		if len(w.PortDrift) > 0 {
			lines = append(lines, fmt.Sprintf(
				"  - [%s] generator drift not reconciled: %s — run `wipnote plugin build-ports` and commit",
				w.Harness, strings.Join(w.PortDrift, ", ")))
		}
		if len(w.Orphaned) > 0 {
			lines = append(lines, fmt.Sprintf(
				"  - [%s] orphaned in-progress items from a prior session: %s",
				w.Harness, strings.Join(w.Orphaned, ", ")))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Unreconciled work from a previous session\n\n" +
		"A prior session ended with reconciliation drift that was recorded but not blocked:\n\n" +
		strings.Join(lines, "\n")
}

// runSessionExitReconcile is the shared Stop/SessionEnd entry point. It runs a
// reconcile pass and applies the harness discriminator:
//
//   - harness=="claude" AND ambiguous drift → return BlockExit2Error (exit-2).
//     This deliberately AMENDS missing_events.go's historical no-block contract
//     for the Stop handler; the amendment is intended and scoped to ambiguous
//     generator drift only.
//   - Gemini/Codex → never block. Persist a DURABLE warning (so the
//     user-never-returns case is still recorded) for SessionStart to surface.
//
// done-but-uncommitted auto-commits and orphan reports never block any harness.
//
// When skipPortDrift is true, the expensive port-drift check is skipped
// entirely — used by the per-turn Stop path so it does not pay 8-22s of full
// regeneration on every model response. The commit-time
// checkPortDriftCommitGuard (commit_portdrift_guard.go) enforces port-drift
// correctness instead, firing only when generator-input files are staged.
func runSessionExitReconcile(database *sql.DB, projectDir, harness, sessionID string, skipPortDrift bool) error {
	rep, err := Reconcile(database, projectDir, false, skipPortDrift)
	if err != nil || rep.Empty() {
		return nil
	}
	return discriminateReconcile(rep, harness, projectDir, sessionID)
}

// discriminateReconcile applies the harness discriminator to an already-
// computed report. Extracted from runSessionExitReconcile so it is unit-
// testable with a synthetic report (no git/DB needed).
//
//   - harness=="claude" + ambiguous drift → BlockExit2Error (exit-2). This is
//     the intentional, narrowly-scoped amendment to the Stop handler's
//     historical no-block contract.
//   - Gemini/Codex → never block; persist a DURABLE warning so the
//     user-never-returns case is still recorded and SessionStart can surface it.
func discriminateReconcile(rep *ReconcileReport, harness, projectDir, sessionID string) error {
	if harness == "claude" {
		if rep.HasAmbiguousDrift() {
			return &BlockExit2Error{Message: fmt.Sprintf(
				"session-exit reconcile: generator-touched files committed without "+
					"regenerating ports (%s). Run `wipnote plugin build-ports` and "+
					"commit the regenerated trees before exiting.",
				strings.Join(rep.PortDrift, ", "))}
		}
		return nil
	}

	// Gemini / Codex: durable, non-blocking.
	if err := persistReconcileWarning(projectDir, harness, sessionID, rep); err != nil {
		debugLog(projectDir, "[reconcile] persist warning failed: %v", err)
	}
	return nil
}
