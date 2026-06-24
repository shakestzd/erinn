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

	// Refresh the session process-liveness anchor (feat-0a7db952, slice-1).
	// On resume the new launcher rewrote .launch-mode with the LIVE pid, so
	// re-resolving picks it up. Without this the resumed session would carry the
	// dead pre-resume pid and be false-reaped. Best-effort: a write failure must
	// NEVER block the hook; when NO fresh owner resolves, updateSessionPIDAnchor
	// REMOVES any stale pre-existing anchor (degrade to LIVE) rather than leaving
	// a dead pid in place.
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	updateSessionPIDAnchor(projectDir, sessDir)

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

	// ReapedSessions and ReapedCollectors are the forced-reap audit trail
	// produced by ReapStaleSessionsAndCollectors (feat-88f92f44). They are
	// deliberately report-only JSON fields with NO backing schema/DB column —
	// this struct IS the audit trail; a migration would break migrations_test.go
	// and there is no query that needs them indexed. ReapedSessions holds the
	// session ids transitioned active→completed; ReapedCollectors holds
	// "<sid>:<pid>" entries for each identity-verified orphan collector killed.
	ReapedSessions   []string `json:"reaped_sessions,omitempty"`
	ReapedCollectors []string `json:"reaped_collectors,omitempty"`
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
		(len(r.AutoCommitted) == 0 && len(r.PortDrift) == 0 && len(r.Orphaned) == 0 &&
			len(r.ReapedSessions) == 0 && len(r.ReapedCollectors) == 0)
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

// reconcileCheapClasses runs ONLY the latency-cheap reconcile classes — the
// pure-SQL orphan scan and (unless skipped) the delegated port-drift check —
// and deliberately OMITS reconcileDoneButUncommitted, whose per-item git fork
// loop (git status/add/diff/commit over every dirty terminal artifact) is the
// ~5.45s synchronous cost the Stop hot path used to pay on EVERY model response
// (feat-c08d1ba1 slice-6, profiled at ~7.2s for 200 dirty done-features).
//
// This is sound for the Stop discriminator contract: per runSessionExitReconcile,
// only ambiguous PortDrift can block (Claude) or warn (Gemini/Codex), and the
// done-but-uncommitted auto-commit "never blocks any harness" — it is purely a
// deterministic side effect. Deferring it loses nothing as long as it still runs
// eventually, which ReconcileDoneButUncommittedForProject guarantees via the
// serve-side drain loop (mirrors the bug-504095f2 orphan-drain split). On the
// per-turn Stop path skipPortDrift is also true, so this reduces to a single
// pure-SQL query.
func reconcileCheapClasses(database *sql.DB, projectDir string, skipPortDrift bool) (*ReconcileReport, error) {
	rep := &ReconcileReport{}
	if database != nil {
		rep.Orphaned = reconcileStartedButOrphaned(database, projectDir)
	}
	if !skipPortDrift {
		rep.PortDrift = reconcilePortDrift(projectDir)
	}
	sort.Strings(rep.PortDrift)
	sort.Strings(rep.Orphaned)
	return rep, nil
}

// ReconcileDoneButUncommittedForProject runs ONLY the done-but-uncommitted
// auto-commit class across the project, with NO cap. It is the deferred,
// off-hot-path counterpart to the class reconcileCheapClasses omits: the
// serve-side writer daemon calls it on a low-frequency loop (startReconcileDrainLoop)
// so the deterministic artifact auto-commit the Stop hook no longer performs
// synchronously still completes — exactly the split bug-504095f2 used for the
// orphan sweep. Best-effort: a nil DB is a no-op. Returns the list of work-item
// IDs whose artifacts were committed this pass.
//
// Finding 2 (roborev-478 round-3): the doc says "no cap" but it formerly
// delegated to reconcileDoneButUncommitted, which scans only the newest 500
// terminal items per status — so once the newest 500 are clean, an OLDER dirty
// artifact was permanently hidden on this serve-drain path. The serve drain now
// uses the PAGINATED scan (reconcileDoneButUncommittedPaged) so every dirty
// terminal artifact is eventually reconciled, regardless of how many terminal
// items exist. The hot Stop path is unchanged (it never calls this — it runs
// reconcileCheapClasses), so the "Stop hot path stays cheap" guarantee holds.
func ReconcileDoneButUncommittedForProject(database *sql.DB, projectDir string) []string {
	if database == nil {
		return nil
	}
	return reconcileDoneButUncommittedPaged(database, projectDir)
}

// reconcileTerminalArtifactPageSize is the page size used by the serve-side
// paginated drain. It matches the historical synchronous cap so per-query cost
// is unchanged; the difference is the drain keeps paging until a status is
// exhausted rather than stopping after the first (newest) page.
const reconcileTerminalArtifactPageSize = 500

// reconcileDoneButUncommitted finds work items in a terminal state whose
// canonical artifact (.wipnote/<type>s/<id>.html) is dirty in git, and
// auto-commits each one. It scans only the newest 500 terminal items per status
// — a BOUNDED cost suitable for the SYNCHRONOUS `wipnote reconcile` CLI /
// Reconcile() callers, where unbounded interactive latency would be worse than
// deferring an old dirty artifact to the serve drain. The deferred serve drain
// (ReconcileDoneButUncommittedForProject) uses the uncapped paginated scan so
// nothing is permanently hidden. Returns the list of committed item IDs.
func reconcileDoneButUncommitted(database *sql.DB, projectDir string) []string {
	repoRoot := reconcileRepoRoot(projectDir)
	if repoRoot == "" {
		return nil
	}
	wipnoteDir := filepath.Join(repoRoot, ".wipnote")

	var committed []string
	for _, status := range []string{"done", "ended"} {
		feats, err := db.ListFeaturesByStatus(database, status, reconcileTerminalArtifactPageSize)
		if err != nil {
			continue
		}
		committed = append(committed, commitDirtyTerminalArtifacts(repoRoot, wipnoteDir, feats)...)
	}
	return committed
}

// reconcileDoneButUncommittedPaged is the UNCAPPED serve-drain scan: it pages
// through EVERY terminal item per status (created_at DESC, id ASC) committing
// each dirty artifact, so an old dirty terminal artifact below the newest-500
// window is no longer permanently skipped (roborev-478 finding 2). Only the
// low-frequency serve drain uses it; the Stop hot path never reaches here.
func reconcileDoneButUncommittedPaged(database *sql.DB, projectDir string) []string {
	repoRoot := reconcileRepoRoot(projectDir)
	if repoRoot == "" {
		return nil
	}
	wipnoteDir := filepath.Join(repoRoot, ".wipnote")

	var committed []string
	for _, status := range []string{"done", "ended"} {
		for offset := 0; ; offset += reconcileTerminalArtifactPageSize {
			feats, err := db.ListFeaturesByStatusPaged(database, status, reconcileTerminalArtifactPageSize, offset)
			if err != nil {
				break
			}
			committed = append(committed, commitDirtyTerminalArtifacts(repoRoot, wipnoteDir, feats)...)
			if len(feats) < reconcileTerminalArtifactPageSize {
				break // last (short) page — status exhausted
			}
		}
	}
	return committed
}

// commitDirtyTerminalArtifacts auto-commits the canonical artifact of each
// terminal feature whose .wipnote/<type>s/<id>.html is dirty in git. Shared by
// the capped synchronous scan and the uncapped paginated serve drain. Returns
// the IDs whose artifacts were committed this call.
func commitDirtyTerminalArtifacts(repoRoot, wipnoteDir string, feats []db.Feature) []string {
	var committed []string
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

// --- Reaper config knobs ---

// defaultReaperSessionTTLSeconds is the heartbeat-stale cutoff (in seconds) beyond
// which a session is eligible for reaping. Distinct from liveness_staleness_threshold_seconds
// (defaultLivenessStalenessSeconds = 120s in core/db): that controls the liveness
// query used by the serve-path claim reaper; this TTL controls the reaper's own
// eligibility window (30m = 1800s).
const (
	defaultReaperSessionTTLSeconds     = 1800 // 30m heartbeat-stale cutoff for reap eligibility
	defaultReaperCollectorGraceSeconds = 10   // SIGTERM→SIGKILL grace window
)

// reaperConfig decodes ONLY the three reaper-specific fields from .wipnote/config.json.
// Everything else in config.json is ignored. This is the 3rd independent reader of
// config.json in this codebase (the others are livenessConfig in core/db/session_repo.go
// and readTaskCompletionConfig in core/hooks/task_completion_gate.go). There is no
// shared config package; each call site reads independently. Accepted debt, NOT a
// refactor target.
type reaperConfig struct {
	ReaperSessionTTLSeconds     int  `json:"reaper_session_ttl_seconds"`
	ReaperCollectorGraceSeconds int  `json:"reaper_collector_grace_seconds"`
	ReaperDaemonReportOnly      bool `json:"reaper_daemon_report_only"`
}

// readReaperConfig loads .wipnote/config.json from projectDir and decodes the
// reaper fields. Returns a zero-value reaperConfig on projectDir=="" or any
// read/parse error; callers apply their own per-field defaults.
func readReaperConfig(projectDir string) reaperConfig {
	if projectDir == "" {
		return reaperConfig{}
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "config.json"))
	if err != nil {
		return reaperConfig{}
	}
	var cfg reaperConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return reaperConfig{}
	}
	return cfg
}

// ReaperSessionTTL returns the duration beyond which a session with a stale
// heartbeat is eligible for reaping. Reads reaper_session_ttl_seconds from
// .wipnote/config.json under projectDir; falls back to the 30m default when
// the file is missing, unreadable, unparseable, or the value is non-positive.
// projectDir=="" → default.
func ReaperSessionTTL(projectDir string) time.Duration {
	def := time.Duration(defaultReaperSessionTTLSeconds) * time.Second
	cfg := readReaperConfig(projectDir)
	if cfg.ReaperSessionTTLSeconds <= 0 {
		return def
	}
	return time.Duration(cfg.ReaperSessionTTLSeconds) * time.Second
}

// ReaperCollectorGrace returns the SIGTERM→SIGKILL grace window for the
// per-session OTel collector process during a reaper pass. Reads
// reaper_collector_grace_seconds from .wipnote/config.json under projectDir;
// falls back to the 10s default when the file is missing, unreadable,
// unparseable, or the value is non-positive. projectDir=="" → default.
func ReaperCollectorGrace(projectDir string) time.Duration {
	def := time.Duration(defaultReaperCollectorGraceSeconds) * time.Second
	cfg := readReaperConfig(projectDir)
	if cfg.ReaperCollectorGraceSeconds <= 0 {
		return def
	}
	return time.Duration(cfg.ReaperCollectorGraceSeconds) * time.Second
}

// ReaperDaemonReportOnly returns whether the reaper daemon should run in
// report-only mode (log reap candidates without actually killing them). Reads
// reaper_daemon_report_only from .wipnote/config.json under projectDir;
// defaults to false (absence == false). projectDir=="" → false.
func ReaperDaemonReportOnly(projectDir string) bool {
	return readReaperConfig(projectDir).ReaperDaemonReportOnly
}

// ReapStaleSessionsAndCollectors is the idempotent, identity-verified reaper
// pass (feat-88f92f44). It transitions crashed/abandoned sessions to completed
// and (optionally) terminates orphaned per-session OTel collectors whose owning
// session is provably dead. It NEVER touches the current session and NEVER
// reaps a session it cannot positively prove dead — the safe-degrade direction
// is always "leave active".
//
// (a) Sessions: a status='active' row is reaped only when its heartbeat is
// stale (no claim heartbeat within ttl) AND its owning process is provably not
// alive (SessionReapEligible, which folds the .session-pid kill(pid,0)+
// start-time anchor). A long-IDLE but LIVE session (process alive) and a legacy
// session with no .session-pid anchor both safe-degrade to LIVE and are left
// active. Idempotency is the UPDATE's own `status='active'` guard plus a
// RowsAffected()==1 check — a concurrent pass that already reaped a row sees 0.
//
// (b) Collectors (only when includeCollectors): for every session dir carrying
// a .collector-pid, the OWNER-liveness gate fires first (never kill a live
// session's collector): skip when the owner is heartbeat-fresh OR its process
// is alive. Otherwise delegate to the injected ReapCollectorFn (observe's
// ReapCollector), which itself gates the kill on IsCollectorAlive — so a
// reused/dead collector pid is cleared without ever being signalled. A nil
// ReapCollectorFn (telemetry not wired) degrades the whole collector phase to a
// no-op.
//
// reportOnly drives the slice-3 reaper_daemon_report_only dry-run knob: when
// true, the FULL detection still runs and rep.ReapedSessions/ReapedCollectors
// are populated with the would-reap set, but NO side effects occur — the
// session UPDATE is skipped and ReapCollectorFn is never called. The config
// flag itself is NOT read here; slice 4's daemon passes the resolved bool.
//
// currentSessionID is always excluded from both phases.
//
// maxSessions caps the SESSION-reap phase: when > 0, the loop stops once that
// many sessions have been reaped (counting both real reaps and reportOnly
// would-reaps), so the synchronous hook path cannot stall behind a large stale
// backlog under write contention — the daemon catches the remainder within one
// interval. maxSessions <= 0 ⇒ unbounded (daemon callers pass 0). The cap
// applies ONLY to the session phase; the collector phase is daemon-only.
func ReapStaleSessionsAndCollectors(database *sql.DB, projectDir, currentSessionID string, includeCollectors bool, reportOnly bool, maxSessions int) *ReconcileReport {
	rep := &ReconcileReport{}
	if database == nil {
		return rep
	}
	ttl := ReaperSessionTTL(projectDir)
	now := time.Now().UTC().Format(time.RFC3339)

	// --- (a) reap stale sessions ---
	sessions, err := db.ListSessions(database, true /*activeOnly*/, 1_000_000)
	if err != nil {
		debugLog(projectDir, "[reaper] list active sessions: %v", err)
		sessions = nil
	}
	reapedCount := 0
	for _, s := range sessions {
		if maxSessions > 0 && reapedCount >= maxSessions {
			// Hook-path cap reached: leave the remainder for the daemon.
			break
		}
		sid := s.SessionID
		if sid == "" || sid == currentSessionID {
			continue
		}
		heartbeatStale := !db.SessionLivenessByHeartbeat(database, sid, ttl)
		sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sid)
		if !SessionReapEligible(heartbeatStale, sessDir) {
			continue
		}
		if reportOnly {
			// Dry-run: record the would-reap set; perform no UPDATE.
			rep.ReapedSessions = append(rep.ReapedSessions, sid)
			reapedCount++
			debugLog(projectDir, "[reaper] report-only: would reap stale session %s (heartbeat stale, owner process dead)", sid[:minLen(sid, 8)])
			continue
		}
		res, execErr := database.Exec(
			`UPDATE sessions SET status='completed', completed_at=? WHERE session_id=? AND status='active'`,
			now, sid,
		)
		if execErr != nil {
			debugLog(projectDir, "[reaper] reap session %s: %v", sid[:minLen(sid, 8)], execErr)
			continue
		}
		if n, _ := res.RowsAffected(); n == 1 {
			rep.ReapedSessions = append(rep.ReapedSessions, sid)
			reapedCount++
			debugLog(projectDir, "[reaper] reaped stale session %s → completed (heartbeat stale, owner process dead)", sid[:minLen(sid, 8)])
			// Release any active claims the reaped session still held. Normal
			// SessionEnd does this (db.ReleaseAllClaimsForSession); the reaper
			// must mirror it or a crashed session's claims block claim paths
			// forever. Only on a REAL reap — never in the reportOnly branch.
			if released, relErr := db.ReleaseAllClaimsForSession(database, sid); relErr != nil {
				debugLog(projectDir, "[reaper] release claims for reaped session %s: %v", sid[:minLen(sid, 8)], relErr)
			} else if released > 0 {
				debugLog(projectDir, "[reaper] released %d claims for reaped session %s", released, sid[:minLen(sid, 8)])
			}
		}
	}

	// --- (b) reap orphaned collectors ---
	if includeCollectors && ReapCollectorFn != nil {
		grace := ReaperCollectorGrace(projectDir)
		// os.ReadDir (not filepath.Glob) so session ids / paths containing glob
		// metacharacters ([ * ?) are scanned correctly — Glob would silently
		// skip or mis-match them.
		sessionsDir := filepath.Join(projectDir, ".wipnote", "sessions")
		entries, _ := os.ReadDir(sessionsDir)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sid := e.Name()
			sessDir := filepath.Join(sessionsDir, sid)
			if sid == "" || sid == currentSessionID {
				continue
			}
			if _, statErr := os.Stat(filepath.Join(sessDir, ".collector-pid")); statErr != nil {
				continue // no collector record for this session
			}
			// Owner-liveness gate: never kill a live session's collector.
			heartbeatStale := !db.SessionLivenessByHeartbeat(database, sid, ttl)
			if !heartbeatStale || IsSessionProcessAlive(sessDir) {
				continue
			}
			if reportOnly {
				rep.ReapedCollectors = append(rep.ReapedCollectors, sid)
				debugLog(projectDir, "[reaper] report-only: would reap orphan collector for session %s", sid[:minLen(sid, 8)])
				continue
			}
			pid, reaped := ReapCollectorFn(sessDir, grace)
			if reaped {
				rep.ReapedCollectors = append(rep.ReapedCollectors, fmt.Sprintf("%s:%d", sid, pid))
				debugLog(projectDir, "[reaper] reaped orphan collector pid=%d for session %s", pid, sid[:minLen(sid, 8)])
			}
			// !reaped ⇒ ReapCollector cleared a dead/reused record; not a kill.
		}
	}

	return rep
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
//
// HOT-PATH SPLIT (feat-c08d1ba1 slice-6): this entry point runs only
// reconcileCheapClasses — the pure-SQL orphan scan plus (unless skipped) the
// delegated port-drift check. It NO LONGER runs the done-but-uncommitted
// auto-commit, whose per-item git fork loop was the ~5.45s synchronous cost the
// Stop hook paid on every model response. That class is now drained off the hot
// path by the serve-side writer daemon (startReconcileDrainLoop →
// ReconcileDoneButUncommittedForProject), so the deterministic artifact
// auto-commit still completes — just not on the interactive path. This is sound
// because only ambiguous PortDrift gates the discriminator below; the
// auto-commit class never blocked any harness.
func runSessionExitReconcile(database *sql.DB, projectDir, harness, sessionID string, skipPortDrift bool) error {
	rep, err := reconcileCheapClasses(database, projectDir, skipPortDrift)
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
