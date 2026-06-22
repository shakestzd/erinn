package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// knownInjectedTagAlt is the regex alternation of harness-injected metadata tag
// names that leak into captured user-prompt text — Claude Code task-notification
// blocks, tool-use wrappers, and system reminders. Sanitization is restricted to
// THESE tags so ordinary user prompt text containing JSX/XML/HTML (e.g.
// "Fix <Button>Save</Button> alignment") is preserved rather than mangled.
const knownInjectedTagAlt = `task-notification|task-id|tool-use-id|tool-use-error|output-file|status|summary|note|result|system-reminder`

// rePairedInjectedTag matches a known injected paired tag and its entire
// content, e.g. <task-id>abc123</task-id>. The (?s) flag makes . match newlines
// so multi-line injected blocks are consumed in one pass. RE2 has no
// backreferences, so the open and close names are independent alternations of
// the known set — sufficient for the injected blocks we strip.
var rePairedInjectedTag = regexp.MustCompile(`(?s)<(?:` + knownInjectedTagAlt + `)\b[^>]*>.*?</(?:` + knownInjectedTagAlt + `)\s*>`)

// reLoneInjectedTag matches a remaining lone/unclosed known injected tag (open
// or close), e.g. a bare <task-notification> whose closing tag was truncated
// out of the captured fragment.
var reLoneInjectedTag = regexp.MustCompile(`</?(?:` + knownInjectedTagAlt + `)\b[^>]*>`)

// reWhitespaceRun matches one or more whitespace characters including newlines
// and tabs. Compiled once at package init.
var reWhitespaceRun = regexp.MustCompile(`\s+`)

// sanitizePromptLabel strips harness-injected metadata markup from a raw session
// prompt and collapses whitespace runs to a single space. Only KNOWN injected
// tags (knownInjectedTagAlt) are removed, so ordinary user prompt text — even
// when it contains XML/JSX/HTML snippets — is preserved.
// Steps: (1) remove known paired tags+content, (2) remove known lone tags,
// (3) collapse all whitespace to single space, (4) trim.
func sanitizePromptLabel(s string) string {
	s = rePairedInjectedTag.ReplaceAllString(s, "")
	s = reLoneInjectedTag.ReplaceAllString(s, "")
	s = reWhitespaceRun.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// InsertSession creates a new session row.
func InsertSession(db *sql.DB, s *models.Session) error {
	_, err := db.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, parent_session_id,
			parent_event_id, created_at, status, start_commit,
			is_subagent, model, active_feature_id, git_remote_url, project_dir,
			exec_worktree_path, branch, harness, continued_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SessionID, s.AgentAssigned, nullStr(s.ParentSessionID),
		nullStr(s.ParentEventID), s.CreatedAt.UTC().Format(time.RFC3339),
		s.Status, nullStr(s.StartCommit),
		s.IsSubagent, nullStr(s.Model), nullStr(s.ActiveFeatureID),
		nullStr(s.GitRemoteURL),
		nullStr(s.ProjectDir),
		nullStr(s.ExecWorktreePath),
		nullStr(s.Branch),
		nullStr(s.Harness),
		nullStr(s.ContinuedFrom),
	)
	if err != nil {
		return fmt.Errorf("insert session %s: %w", s.SessionID, err)
	}
	return nil
}

// UpsertSession inserts a new session row or upgrades an existing placeholder
// row (created by EnsureSession) with real metadata. Called by the applier's
// OpTypeSessionInsert path so that out-of-order arrival — where an agent_event
// op created a "__hook__" placeholder before the real session.insert arrived —
// results in the final row carrying the real metadata rather than sticking with
// the placeholder values.
//
// The WHERE guard ensures we only upgrade placeholder rows (agent_assigned='__hook__')
// and never overwrite real session rows created by other means (which may have
// progressed state like completed status). A replayed/late session.insert must not
// revert a completed session back to active, for example.
//
// CRITICAL: The upgrade-only branch (DO UPDATE) omits status and completed_at.
// These fields are lifecycle-owned state and must NEVER be reverted by a late/
// replayed session.insert. The upgrade attaches real session identity/metadata
// (agent_assigned, parent_session_id, etc.) to a pristine placeholder; lifecycle
// status is managed exclusively by session.status ops (OpTypeSessionStatus).
func UpsertSession(db *sql.DB, s *models.Session) error {
	_, err := db.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, parent_session_id,
			parent_event_id, created_at, status, start_commit,
			is_subagent, model, active_feature_id, git_remote_url, project_dir,
			exec_worktree_path, branch, harness, continued_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			agent_assigned=excluded.agent_assigned,
			parent_session_id=excluded.parent_session_id,
			parent_event_id=excluded.parent_event_id,
			created_at=excluded.created_at,
			start_commit=excluded.start_commit,
			is_subagent=excluded.is_subagent,
			model=excluded.model,
			active_feature_id=excluded.active_feature_id,
			git_remote_url=excluded.git_remote_url,
			project_dir=excluded.project_dir,
			exec_worktree_path=excluded.exec_worktree_path,
			branch=excluded.branch,
			harness=excluded.harness,
			continued_from=excluded.continued_from
		WHERE sessions.agent_assigned = '__hook__'`,
		s.SessionID, s.AgentAssigned, nullStr(s.ParentSessionID),
		nullStr(s.ParentEventID), s.CreatedAt.UTC().Format(time.RFC3339),
		s.Status, nullStr(s.StartCommit),
		s.IsSubagent, nullStr(s.Model), nullStr(s.ActiveFeatureID),
		nullStr(s.GitRemoteURL),
		nullStr(s.ProjectDir),
		nullStr(s.ExecWorktreePath),
		nullStr(s.Branch),
		nullStr(s.Harness),
		nullStr(s.ContinuedFrom),
	)
	if err != nil {
		return fmt.Errorf("upsert session %s: %w", s.SessionID, err)
	}
	return nil
}

// GetSession retrieves a session by ID.
func GetSession(db *sql.DB, sessionID string) (*models.Session, error) {
	row := db.QueryRow(`
		SELECT session_id, agent_assigned, parent_session_id,
			parent_event_id, created_at, completed_at,
			total_events, total_tokens_used, context_drift,
			status, is_subagent, model, active_feature_id, project_dir,
			last_user_query_at, last_user_query, handoff_notes, recommended_next,
			blockers, recommended_context, continued_from,
			exec_worktree_path, branch, harness
		FROM sessions WHERE session_id = ?`, sessionID)

	s := &models.Session{}
	var parentSess, parentEvt, completedAt, model, activeFeat, projectDir sql.NullString
	var lastUserQueryAt, lastUserQuery, handoffNotes, recommendedNext sql.NullString
	var blockers, recommendedContext, continuedFrom sql.NullString
	var execWorktreePath, branch, harness sql.NullString
	var createdStr string

	err := row.Scan(
		&s.SessionID, &s.AgentAssigned, &parentSess,
		&parentEvt, &createdStr, &completedAt,
		&s.TotalEvents, &s.TotalTokensUsed, &s.ContextDrift,
		&s.Status, &s.IsSubagent, &model, &activeFeat, &projectDir,
		&lastUserQueryAt, &lastUserQuery, &handoffNotes, &recommendedNext,
		&blockers, &recommendedContext, &continuedFrom,
		&execWorktreePath, &branch, &harness,
	)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", sessionID, err)
	}

	s.ParentSessionID = parentSess.String
	s.ParentEventID = parentEvt.String
	s.Model = model.String
	s.ActiveFeatureID = activeFeat.String
	s.ProjectDir = projectDir.String
	s.LastUserQueryAt = lastUserQueryAt.String
	s.LastUserQuery = lastUserQuery.String
	s.HandoffNotes = handoffNotes.String
	s.RecommendedNext = recommendedNext.String
	if blockers.Valid && blockers.String != "" {
		s.Blockers = json.RawMessage(blockers.String)
	}
	if recommendedContext.Valid && recommendedContext.String != "" {
		s.RecommendedContext = json.RawMessage(recommendedContext.String)
	}
	s.ContinuedFrom = continuedFrom.String
	s.ExecWorktreePath = execWorktreePath.String
	s.Branch = branch.String
	s.Harness = harness.String
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)

	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339, completedAt.String)
		s.CompletedAt = &t
	}
	return s, nil
}

// UpdateSessionHandoff writes deterministic SessionEnd handoff fields onto a
// session row without clobbering existing non-empty values with empty input.
// Callers may pass empty strings for any field they do not want to change.
func UpdateSessionHandoff(db *sql.DB, sessionID, handoffNotes, recommendedNext string, blockersJSON, recommendedContextJSON []byte) error {
	if sessionID == "" {
		return nil
	}

	var blockersArg any
	if len(blockersJSON) > 0 {
		blockersArg = string(blockersJSON)
	}
	var recommendedContextArg any
	if len(recommendedContextJSON) > 0 {
		recommendedContextArg = string(recommendedContextJSON)
	}

	_, err := db.Exec(`
		UPDATE sessions
		SET handoff_notes = COALESCE(NULLIF(?, ''), handoff_notes),
		    recommended_next = COALESCE(NULLIF(?, ''), recommended_next),
		    blockers = COALESCE(NULLIF(?, ''), blockers),
		    recommended_context = COALESCE(NULLIF(?, ''), recommended_context)
		WHERE session_id = ?`,
		handoffNotes, recommendedNext, blockersArg, recommendedContextArg, sessionID,
	)
	if err != nil {
		return fmt.Errorf("update session handoff %s: %w", sessionID, err)
	}
	return nil
}

// UpdateSessionStatus sets the status and optionally the completed_at timestamp.
func UpdateSessionStatus(db *sql.DB, sessionID, status string) error {
	var completedAt *string
	if status == "completed" || status == "failed" {
		now := time.Now().UTC().Format(time.RFC3339)
		completedAt = &now
	}
	_, err := db.Exec(`
		UPDATE sessions SET status = ?, completed_at = COALESCE(?, completed_at)
		WHERE session_id = ?`,
		status, completedAt, sessionID,
	)
	return err
}

// ListSessions returns sessions ordered by created_at DESC with an optional
// active-only filter and row limit.
func ListSessions(db *sql.DB, activeOnly bool, limit int) ([]*models.Session, error) {
	if limit <= 0 {
		limit = 10
	}

	query := `
		SELECT session_id, agent_assigned, created_at, completed_at, status, model
		FROM sessions`
	if activeOnly {
		query += " WHERE status = 'active'"
	}
	query += " ORDER BY created_at DESC LIMIT ?"

	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*models.Session
	for rows.Next() {
		s := &models.Session{}
		var completedAt, model sql.NullString
		var createdStr string

		if err := rows.Scan(
			&s.SessionID, &s.AgentAssigned, &createdStr,
			&completedAt, &s.Status, &model,
		); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		s.Model = model.String
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339, completedAt.String)
			s.CompletedAt = &t
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// MostRecentActiveSession returns the session_id of the latest active session,
// or ("", nil) if none exists.
func MostRecentActiveSession(db *sql.DB) (string, error) {
	row := db.QueryRow(`
		SELECT session_id FROM sessions
		WHERE status = 'active'
		ORDER BY created_at DESC LIMIT 1`)
	var id string
	if err := row.Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("most recent active session: %w", err)
	}
	return id, nil
}

// GetSessionProjectDir returns the project_dir for a session, or empty string
// if the session does not exist or has no project_dir set.
func GetSessionProjectDir(database *sql.DB, sessionID string) string {
	var projectDir sql.NullString
	row := database.QueryRow(
		`SELECT project_dir FROM sessions WHERE session_id = ?`, sessionID,
	)
	_ = row.Scan(&projectDir)
	return projectDir.String
}

// ToolUseContextRow holds the batch-fetched session + claim fields used by
// resolveToolUseContext. Replaces three separate queries (GetSession,
// GetActiveFeatureID, HasActiveClaimByAgent) with a single SQL join.
type ToolUseContextRow struct {
	SessionID       string
	ActiveFeatureID string
	ParentSessionID string
	IsSubagent      bool
	CreatedAt       time.Time
	// ClaimedItem is the work_item_id of the agent's active claim, or "".
	ClaimedItem string
}

// GetToolUseContext fetches the session and active claim for agentID in a
// single query, replacing three separate reads on the PreToolUse hot path.
// Returns nil when the session does not exist.
//
// active_feature_id is only returned when the referenced feature is actually
// in-progress — a stale pointer to a completed feature is treated as empty,
// so guards correctly block edits without an active work item.
func GetToolUseContext(db *sql.DB, sessionID, agentID string) (*ToolUseContextRow, error) {
	// Claim lookup uses three paths, tried in order:
	//   1. claimed_by_agent_id = agentID  — the direct per-agent claim
	//   2. owner_session_id   = sessionID — fallback for subagent tool calls,
	//      which share the orchestrator's session_id but carry a distinct
	//      agent_id that never had its own claim row (bug-cb4918d8). The
	//      orchestrator's claim is keyed on owner_session_id, so this resolves
	//      the parent's claim for any subagent running under it.
	//   3. session_family_id sibling — fallback for harnesses that invoke hooks
	//      with a sibling/root session token while CLI attribution landed on
	//      another member of the same logical session family.
	// All claim paths are expressed as correlated subqueries so the outer row
	// remains a single sessions row (LIMIT 1 stays exact) and the primary
	// agent-id match wins over the session-id fallback via COALESCE ordering.
	row := db.QueryRow(`
		SELECT s.session_id,
		       COALESCE(
		         CASE WHEN f.status = 'in-progress' THEN s.active_feature_id ELSE '' END,
		         ''
		       ) AS active_feature_id,
		       COALESCE(s.parent_session_id, '') AS parent_session_id,
		       s.is_subagent,
		       s.created_at,
		       COALESCE(
		         (SELECT c.work_item_id FROM claims c
		           WHERE c.claimed_by_agent_id = ?
		             AND c.owner_session_id = ?
		             AND c.status IN ('proposed','claimed','in_progress','blocked','handoff_pending')
		           ORDER BY c.leased_at DESC
		           LIMIT 1),
		         (SELECT c.work_item_id FROM claims c
		           WHERE c.owner_session_id = ?
		             AND c.status IN ('proposed','claimed','in_progress','blocked','handoff_pending')
		           ORDER BY c.leased_at DESC
		           LIMIT 1),
		         (SELECT c.work_item_id FROM claims c
		           JOIN sessions cs ON cs.session_id = c.owner_session_id
		           WHERE c.owner_session_id != s.session_id
		             AND cs.status = 'active'
		             AND COALESCE(NULLIF(cs.session_family_id, ''), cs.session_id) = COALESCE(NULLIF(s.session_family_id, ''), s.session_id)
		             AND c.status IN ('proposed','claimed','in_progress','blocked','handoff_pending')
		           ORDER BY c.leased_at DESC
		           LIMIT 1),
		         ''
		       ) AS claimed_item
		FROM sessions s
		LEFT JOIN features f ON f.id = s.active_feature_id
		WHERE s.session_id = ?
		LIMIT 1`,
		agentID, sessionID, sessionID, sessionID,
	)

	r := &ToolUseContextRow{}
	var createdStr string
	err := row.Scan(
		&r.SessionID,
		&r.ActiveFeatureID,
		&r.ParentSessionID,
		&r.IsSubagent,
		&createdStr,
		&r.ClaimedItem,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tool use context %s: %w", sessionID, err)
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	return r, nil
}

// GetActiveFeatureIDForSession returns the active_feature_id for sessionID, or
// "" when the session has none. Lightweight single-column lookup used by the
// parent-session fallback in autoCompleteFromCommit.
func GetActiveFeatureIDForSession(db *sql.DB, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	var id sql.NullString
	db.QueryRow(
		`SELECT active_feature_id FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&id)
	return id.String
}

// nullStr converts an empty string to sql.NullString.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// SetSessionFamilyID sets the session_family_id for the given session. If the
// family ID is empty, the session's own ID is used (self-as-family backfill).
func SetSessionFamilyID(db *sql.DB, sessionID, familyID string) error {
	if familyID == "" {
		familyID = sessionID
	}
	_, err := db.Exec(
		`UPDATE sessions SET session_family_id = ? WHERE session_id = ?`,
		familyID, sessionID,
	)
	if err != nil {
		return fmt.Errorf("set session_family_id %s: %w", sessionID, err)
	}
	return nil
}

// SessionFile holds a file path and its access metadata for a given session.
type SessionFile struct {
	FilePath  string `json:"file_path"`
	Operation string `json:"operation"`
	LastSeen  string `json:"last_seen"`
}

// ListFilesBySession returns all file paths recorded for the given session,
// reusing the feature_files.session_id column (schema ~301-311, nullable).
// Results are ordered by last_seen DESC. Returns nil (not an error) when the
// session has no recorded files.
func ListFilesBySession(db *sql.DB, sessionID string) ([]SessionFile, error) {
	if sessionID == "" {
		return nil, nil
	}
	rows, err := db.Query(`
		SELECT file_path, COALESCE(operation, ''), last_seen
		FROM feature_files
		WHERE session_id = ?
		ORDER BY last_seen DESC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list files for session %s: %w", sessionID, err)
	}
	defer rows.Close()
	var out []SessionFile
	for rows.Next() {
		var sf SessionFile
		if err := rows.Scan(&sf.FilePath, &sf.Operation, &sf.LastSeen); err != nil {
			continue
		}
		out = append(out, sf)
	}
	return out, rows.Err()
}

// GetSessionsByFamily returns all session_ids that belong to the given family.
// Results are ordered by created_at DESC so the most recent session is first.
func GetSessionsByFamily(db *sql.DB, familyID string) ([]string, error) {
	rows, err := db.Query(
		`SELECT session_id FROM sessions WHERE session_family_id = ? ORDER BY created_at DESC`,
		familyID,
	)
	if err != nil {
		return nil, fmt.Errorf("get sessions by family %s: %w", familyID, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan session id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// claimHeartbeatInterval is the PreToolUse claim-heartbeat cadence
// (pretooluse.go heartbeats with a 30m lease but fires on every tool call;
// the meaningful liveness signal is the heartbeat *recency*, not the lease).
// A session is considered live only when its most recent claim heartbeat is
// younger than the staleness threshold. The default threshold is 2× this
// interval (2 minutes), tunable via .wipnote/config.json.
const claimHeartbeatInterval = 60 * time.Second

// defaultLivenessStalenessSeconds is 2× claimHeartbeatInterval. A session
// whose newest claim heartbeat is older than this is NOT live, regardless of
// sessions.status (folds bug-6c3e8252: stale status='active' ghost rows).
const defaultLivenessStalenessSeconds = 120

// livenessConfig mirrors the local os.ReadFile(.wipnote/config.json) pattern
// used by readTaskCompletionConfig in internal/hooks/task_completion_gate.go
// (there is no shared internal/config package). Only the one tunable field is
// decoded; everything else in config.json is ignored.
type livenessConfig struct {
	LivenessStalenessSeconds int `json:"liveness_staleness_seconds"`
}

// LivenessStalenessThreshold returns the heartbeat-age cutoff beyond which a
// session is considered not-live. Reads .wipnote/config.json under projectDir;
// falls back to the 2×interval default when the file is missing, unreadable,
// the key is absent, or the value is non-positive. projectDir may be "" (e.g.
// CLI contexts without a resolved project) — the default is returned then.
func LivenessStalenessThreshold(projectDir string) time.Duration {
	def := time.Duration(defaultLivenessStalenessSeconds) * time.Second
	if projectDir == "" {
		return def
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "config.json"))
	if err != nil {
		return def
	}
	var cfg livenessConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return def
	}
	if cfg.LivenessStalenessSeconds <= 0 {
		return def
	}
	return time.Duration(cfg.LivenessStalenessSeconds) * time.Second
}

// SessionLivenessByHeartbeat reports whether a session is *honestly* live,
// derived from claim-heartbeat recency rather than sessions.status. This is the
// cross-harness liveness primitive: it works for every harness with zero
// dependency on a session-end event firing (folds bug-6c3e8252 — stale
// status='active' rows whose last heartbeat is ancient are correctly reported
// not-live).
//
// A session is live iff it has at least one claim whose last_heartbeat_at is
// within `threshold` of now. Sessions with no claims at all are not live (we
// have no liveness signal for them — honest absence of evidence). The query is
// a single indexed lookup on claims(owner_session_id); it never writes.
func SessionLivenessByHeartbeat(db *sql.DB, sessionID string, threshold time.Duration) bool {
	if db == nil || sessionID == "" {
		return false
	}
	var hb sql.NullString
	err := db.QueryRow(`
		SELECT MAX(last_heartbeat_at) FROM claims
		WHERE owner_session_id = ?`, sessionID).Scan(&hb)
	if err != nil || !hb.Valid || hb.String == "" {
		return false
	}
	t, perr := time.Parse(time.RFC3339, hb.String)
	if perr != nil {
		return false
	}
	return time.Since(t) <= threshold
}

type ResumableSession struct {
	WorkItemID       string `json:"work_item_id"`
	Title            string `json:"title"`
	Type             string `json:"type"`
	PromptLabel      string `json:"prompt_label"`
	Branch           string `json:"branch"`
	ExecWorktreePath string `json:"exec_worktree_path"`
	Harness          string `json:"harness"`
	LastActivity     string `json:"last_activity"`
	LastSessionID    string `json:"last_session_id"`
	Live             bool   `json:"live"`
}

type HarnessGroupedResumableSessions struct {
	// Current, when non-nil, is the session the user is launching from (resolved
	// from the harness/launcher env + its session family). It is surfaced as a
	// first-class "Resume this session" slot at the top of the chooser and
	// deliberately bypasses the work_item_id and item_status gates the grouped
	// listings apply, so a current session with no (or completed) work item is
	// still offered. Nil when no current session can be resolved.
	Current      *ResumableSession  `json:"current,omitempty"`
	SameHarness  []ResumableSession `json:"same_harness"`
	CrossHarness []ResumableSession `json:"cross_harness"`
}

// ListResumableSessions returns the most-recent session per incomplete work
// item, ranked by last activity descending.
func ListResumableSessions(db *sql.DB, threshold time.Duration) ([]ResumableSession, error) {
	if db == nil {
		return nil, nil
	}
	cutoff := time.Now().UTC().Add(-threshold).Format(time.RFC3339)
	rows, err := db.Query(`
WITH session_work AS (
	-- One row per (session, work_item): JOIN against active_work_items so
	-- parallel agents in the same session each get their own row. Fall back
	-- to active_feature_id when no active_work_items row exists (legacy sessions).
	SELECT
		s.session_id,
		s.created_at,
		COALESCE(s.exec_worktree_path, '') AS exec_worktree_path,
		COALESCE(s.branch, '') AS branch,
		COALESCE(s.harness, '') AS harness,
		COALESCE(s.agent_assigned, '') AS agent_assigned,
		COALESCE(awi.work_item_id, s.active_feature_id, '') AS work_item_id
	FROM sessions s
	LEFT JOIN active_work_items awi ON awi.session_id = s.session_id
),
enriched AS (
	SELECT
		sw.session_id,
		sw.work_item_id,
		COALESCE(f.title, t.title, '') AS title,
		COALESCE(NULLIF(f.type, ''), CASE WHEN t.id IS NOT NULL THEN 'track' ELSE '' END) AS type,
		COALESCE(f.status, t.status, '') AS item_status,
		sw.branch,
		sw.exec_worktree_path,
		sw.harness,
		sw.agent_assigned,
		COALESCE((
			SELECT MAX(ts)
			FROM (
				SELECT MAX(ae.timestamp) AS ts FROM agent_events ae WHERE ae.session_id = sw.session_id
				UNION ALL
				SELECT MAX(m.timestamp) AS ts FROM messages m WHERE m.session_id = sw.session_id
				UNION ALL
				SELECT MAX(c.last_heartbeat_at) AS ts FROM claims c WHERE c.owner_session_id = sw.session_id
				UNION ALL
				SELECT sw.created_at AS ts
			)
		), sw.created_at) AS last_activity,
		(SELECT MAX(c.last_heartbeat_at) FROM claims c WHERE c.owner_session_id = sw.session_id) AS last_heartbeat_at
	FROM session_work sw
	LEFT JOIN features f ON f.id = sw.work_item_id
	LEFT JOIN tracks t ON t.id = sw.work_item_id
	WHERE sw.work_item_id <> ''
),
ranked AS (
	SELECT
		*,
		ROW_NUMBER() OVER (
			PARTITION BY work_item_id
			ORDER BY last_activity DESC, session_id DESC
		) AS row_num
	FROM enriched
	WHERE item_status NOT IN ('done', 'completed')
)
SELECT
	work_item_id,
	title,
	type,
	branch,
	exec_worktree_path,
	harness,
	last_activity,
	session_id,
	CASE
		WHEN last_heartbeat_at IS NOT NULL AND last_heartbeat_at >= ? THEN 1
		ELSE 0
	END AS live,
	agent_assigned
FROM ranked
WHERE row_num = 1
ORDER BY last_activity DESC, work_item_id`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list resumable sessions: %w", err)
	}
	defer rows.Close()

	var out []ResumableSession
	for rows.Next() {
		var item ResumableSession
		var live int
		var agentAssigned string
		if err := rows.Scan(
			&item.WorkItemID,
			&item.Title,
			&item.Type,
			&item.Branch,
			&item.ExecWorktreePath,
			&item.Harness,
			&item.LastActivity,
			&item.LastSessionID,
			&live,
			&agentAssigned,
		); err != nil {
			return nil, fmt.Errorf("scan resumable session: %w", err)
		}
		item.Live = live != 0
		item.Harness = normalizeSessionHarness(item.Harness, agentAssigned)
		item.PromptLabel = SessionPromptLabel(db, item.LastSessionID)
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListHarnessGroupedResumableSessions returns resumable sessions split into
// current-harness and cross-harness buckets. Same-harness rows keep the most
// recent session for each work item on that harness, even when a newer session
// from another harness exists for the same item.
func ListHarnessGroupedResumableSessions(db *sql.DB, threshold time.Duration, harness string) (HarnessGroupedResumableSessions, error) {
	var grouped HarnessGroupedResumableSessions
	if db == nil {
		return grouped, nil
	}
	cutoff := time.Now().UTC().Add(-threshold).Format(time.RFC3339)
	targetHarness := strings.TrimSpace(strings.ToLower(harness))
	rows, err := db.Query(`
WITH session_work AS (
	SELECT
		s.session_id,
		s.created_at,
		COALESCE(s.exec_worktree_path, '') AS exec_worktree_path,
		COALESCE(s.branch, '') AS branch,
		COALESCE(s.harness, '') AS harness,
		COALESCE(s.agent_assigned, '') AS agent_assigned,
		COALESCE(awi.work_item_id, s.active_feature_id, '') AS work_item_id
	FROM sessions s
	LEFT JOIN active_work_items awi ON awi.session_id = s.session_id
),
enriched AS (
	SELECT
		sw.session_id,
		sw.work_item_id,
		COALESCE(f.title, t.title, '') AS title,
		COALESCE(NULLIF(f.type, ''), CASE WHEN t.id IS NOT NULL THEN 'track' ELSE '' END) AS type,
		COALESCE(f.status, t.status, '') AS item_status,
		sw.branch,
		sw.exec_worktree_path,
		sw.harness,
		sw.agent_assigned,
		COALESCE((
			SELECT MAX(ts)
			FROM (
				SELECT MAX(ae.timestamp) AS ts FROM agent_events ae WHERE ae.session_id = sw.session_id
				UNION ALL
				SELECT MAX(m.timestamp) AS ts FROM messages m WHERE m.session_id = sw.session_id
				UNION ALL
				SELECT MAX(c.last_heartbeat_at) AS ts FROM claims c WHERE c.owner_session_id = sw.session_id
				UNION ALL
				SELECT sw.created_at AS ts
			)
		), sw.created_at) AS last_activity,
		(SELECT MAX(c.last_heartbeat_at) FROM claims c WHERE c.owner_session_id = sw.session_id) AS last_heartbeat_at
	FROM session_work sw
	LEFT JOIN features f ON f.id = sw.work_item_id
	LEFT JOIN tracks t ON t.id = sw.work_item_id
	WHERE sw.work_item_id <> ''
),
normalized AS (
	SELECT
		session_id,
		work_item_id,
		title,
		type,
		item_status,
		branch,
		exec_worktree_path,
		CASE
			WHEN TRIM(harness) <> '' THEN LOWER(TRIM(harness))
			WHEN LOWER(TRIM(agent_assigned)) LIKE '%antigravity%' THEN 'antigravity'
			WHEN LOWER(TRIM(agent_assigned)) LIKE '%gemini%' THEN 'gemini'
			WHEN LOWER(TRIM(agent_assigned)) LIKE '%codex%' THEN 'codex'
			WHEN LOWER(TRIM(agent_assigned)) LIKE '%claude%' THEN 'claude'
			ELSE ''
		END AS normalized_harness,
		agent_assigned,
		last_activity,
		last_heartbeat_at
	FROM enriched
	WHERE item_status NOT IN ('done', 'completed')
),
ranked AS (
	SELECT
		*,
		CASE
			WHEN normalized_harness = ? THEN 0
			ELSE 1
		END AS harness_group,
		ROW_NUMBER() OVER (
			PARTITION BY work_item_id,
				CASE WHEN normalized_harness = ? THEN 0 ELSE 1 END
			ORDER BY last_activity DESC, session_id DESC
		) AS row_num
	FROM normalized
)
SELECT
	work_item_id,
	title,
	type,
	branch,
	exec_worktree_path,
	normalized_harness,
	last_activity,
	session_id,
	CASE
		WHEN last_heartbeat_at IS NOT NULL AND last_heartbeat_at >= ? THEN 1
		ELSE 0
	END AS live,
	agent_assigned,
	harness_group
FROM ranked
WHERE row_num = 1
ORDER BY harness_group ASC, last_activity DESC, work_item_id`, targetHarness, targetHarness, cutoff)
	if err != nil {
		return grouped, fmt.Errorf("list grouped resumable sessions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item ResumableSession
		var live int
		var agentAssigned string
		var harnessGroup int
		if err := rows.Scan(
			&item.WorkItemID,
			&item.Title,
			&item.Type,
			&item.Branch,
			&item.ExecWorktreePath,
			&item.Harness,
			&item.LastActivity,
			&item.LastSessionID,
			&live,
			&agentAssigned,
			&harnessGroup,
		); err != nil {
			return grouped, fmt.Errorf("scan grouped resumable session: %w", err)
		}
		item.Live = live != 0
		item.Harness = normalizeSessionHarness(item.Harness, agentAssigned)
		item.PromptLabel = SessionPromptLabel(db, item.LastSessionID)
		if harnessGroup == 0 {
			grouped.SameHarness = append(grouped.SameHarness, item)
		} else {
			grouped.CrossHarness = append(grouped.CrossHarness, item)
		}
	}
	return grouped, rows.Err()
}

// GetResumableSessionForSessionAndWorkItem resolves one resumable-session row
// for the exact (session_id, work_item_id) pair using the same active_work_items
// + legacy active_feature_id model as the resumable listings.
func GetResumableSessionForSessionAndWorkItem(db *sql.DB, threshold time.Duration, sessionID, workItemID string) (*ResumableSession, error) {
	if db == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	workItemID = strings.TrimSpace(workItemID)
	if sessionID == "" || workItemID == "" {
		return nil, nil
	}
	cutoff := time.Now().UTC().Add(-threshold).Format(time.RFC3339)
	row := db.QueryRow(`
WITH session_work AS (
	SELECT
		s.session_id,
		s.created_at,
		COALESCE(s.exec_worktree_path, '') AS exec_worktree_path,
		COALESCE(s.branch, '') AS branch,
		COALESCE(s.harness, '') AS harness,
		COALESCE(s.agent_assigned, '') AS agent_assigned,
		COALESCE(awi.work_item_id, s.active_feature_id, '') AS work_item_id
	FROM sessions s
	LEFT JOIN active_work_items awi ON awi.session_id = s.session_id
	WHERE s.session_id = ?
),
enriched AS (
	SELECT
		sw.session_id,
		sw.work_item_id,
		COALESCE(f.title, t.title, '') AS title,
		COALESCE(NULLIF(f.type, ''), CASE WHEN t.id IS NOT NULL THEN 'track' ELSE '' END) AS type,
		COALESCE(f.status, t.status, '') AS item_status,
		sw.branch,
		sw.exec_worktree_path,
		sw.harness,
		sw.agent_assigned,
		COALESCE((
			SELECT MAX(ts)
			FROM (
				SELECT MAX(ae.timestamp) AS ts FROM agent_events ae WHERE ae.session_id = sw.session_id
				UNION ALL
				SELECT MAX(m.timestamp) AS ts FROM messages m WHERE m.session_id = sw.session_id
				UNION ALL
				SELECT MAX(c.last_heartbeat_at) AS ts FROM claims c WHERE c.owner_session_id = sw.session_id
				UNION ALL
				SELECT sw.created_at AS ts
			)
		), sw.created_at) AS last_activity,
		(SELECT MAX(c.last_heartbeat_at) FROM claims c WHERE c.owner_session_id = sw.session_id) AS last_heartbeat_at
	FROM session_work sw
	LEFT JOIN features f ON f.id = sw.work_item_id
	LEFT JOIN tracks t ON t.id = sw.work_item_id
	WHERE sw.work_item_id = ?
)
SELECT
	work_item_id,
	title,
	type,
	branch,
	exec_worktree_path,
	harness,
	last_activity,
	session_id,
	CASE
		WHEN last_heartbeat_at IS NOT NULL AND last_heartbeat_at >= ? THEN 1
		ELSE 0
	END AS live,
	agent_assigned
FROM enriched
WHERE item_status NOT IN ('done', 'completed')
ORDER BY last_activity DESC, session_id DESC
LIMIT 1`, sessionID, workItemID, cutoff)

	var item ResumableSession
	var live int
	var agentAssigned string
	if err := row.Scan(
		&item.WorkItemID,
		&item.Title,
		&item.Type,
		&item.Branch,
		&item.ExecWorktreePath,
		&item.Harness,
		&item.LastActivity,
		&item.LastSessionID,
		&live,
		&agentAssigned,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get resumable session for %s/%s: %w", sessionID, workItemID, err)
	}
	item.Live = live != 0
	item.Harness = normalizeSessionHarness(item.Harness, agentAssigned)
	item.PromptLabel = SessionPromptLabel(db, item.LastSessionID)
	return &item, nil
}

// SessionPromptLabel returns a human session label suitable for resume choosers.
// Prefer explicit session handoff metadata, then the latest user transcript
// message, and finally the first user transcript message. Empty means no prompt
// label is available and callers should fall back to work-item metadata.
func SessionPromptLabel(db *sql.DB, sessionID string) string {
	label, _ := sessionPromptLabel(db, sessionID)
	return label
}

// SessionPromptLabelAt returns the timestamp/order key of the prompt selected
// by SessionPromptLabel. It is intended for comparing prompt-bearing sessions
// in chooser ranking. Empty means no prompt label is available.
func SessionPromptLabelAt(db *sql.DB, sessionID string) string {
	_, at := sessionPromptLabel(db, sessionID)
	return at
}

func sessionPromptLabel(db *sql.DB, sessionID string) (string, string) {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return "", ""
	}
	rows, err := db.Query(`
WITH candidates AS (
	SELECT
		NULLIF(TRIM(last_user_query), '') AS label,
		COALESCE(last_user_query_at, '') AS label_at,
		0 AS source_rank
	FROM sessions
	WHERE session_id = ? AND TRIM(COALESCE(last_user_query, '')) <> ''
	UNION ALL
	SELECT
		NULLIF(TRIM(content), '') AS label,
		COALESCE(timestamp, '') AS label_at,
		1 AS source_rank
	FROM messages
	WHERE session_id = ? AND role = 'user' AND TRIM(content) <> ''
	UNION ALL
	SELECT
		NULLIF(TRIM(COALESCE(
			json_extract(attrs_json, '$.prompt'),
			json_extract(attrs_json, '$.text'),
			json_extract(attrs_json, '$.user_prompt')
		)), '') AS label,
		COALESCE(
			json_extract(attrs_json, '$."event.timestamp"'),
			CAST(ts_micros AS TEXT),
			''
		) AS label_at,
		2 AS source_rank
	FROM otel_signals
	WHERE session_id = ?
	  AND canonical = 'user_prompt'
	  AND TRIM(COALESCE(
		json_extract(attrs_json, '$.prompt'),
		json_extract(attrs_json, '$.text'),
		json_extract(attrs_json, '$.user_prompt'),
		''
	  )) <> ''
)
SELECT COALESCE(label, ''), COALESCE(label_at, '')
FROM candidates
WHERE label IS NOT NULL AND label <> ''
ORDER BY label_at DESC, source_rank ASC
LIMIT 10`, sessionID, sessionID, sessionID)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	for rows.Next() {
		var label, labelAt string
		if err := rows.Scan(&label, &labelAt); err != nil {
			continue
		}
		if clean := sanitizePromptLabel(label); clean != "" {
			return clean, labelAt
		}
	}
	return "", ""
}

func normalizeSessionHarness(raw, agentAssigned string) string {
	if raw != "" {
		return raw
	}
	v := strings.ToLower(strings.TrimSpace(agentAssigned))
	switch {
	case strings.Contains(v, "antigravity"):
		return "antigravity"
	case strings.Contains(v, "gemini"):
		return "gemini"
	case strings.Contains(v, "codex"):
		return "codex"
	case strings.Contains(v, "claude"):
		return "claude"
	default:
		return ""
	}
}

// sessionFilePathHash returns an 8-char hex digest of a file path, used to
// build deterministic primary keys for session_files rows so an upsert keyed
// on (session_id,file_path) stays a single statement.
func sessionFilePathHash(filePath string) string {
	h := sha256.Sum256([]byte(filePath))
	return fmt.Sprintf("%x", h[:4])
}

// UpsertSessionFile records a claimless file touch (no active claim/feature) in
// the session_files ledger. Idempotent on (session_id,file_path): a repeat
// touch updates operation + last_seen in place. This is the ONLY new derived
// write on the PostToolUse path for claimless edits — exactly one statement,
// preserving the feat-156e0a1a zero-SQLITE_BUSY hot-path guarantee.
func UpsertSessionFile(db *sql.DB, sessionID, filePath, operation string) error {
	if sessionID == "" || filePath == "" {
		return nil
	}
	if operation == "" {
		operation = "unknown"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := sessionID + "-" + sessionFilePathHash(filePath)
	_, err := db.Exec(`
		INSERT INTO session_files (id, session_id, file_path, operation, first_seen, last_seen, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, file_path) DO UPDATE SET
			operation = excluded.operation,
			last_seen = excluded.last_seen`,
		id, sessionID, filePath, operation, now, now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert session_file %s/%s: %w", sessionID, filePath, err)
	}
	return nil
}

// ListClaimlessFilesBySession returns claimless touches from the session_files
// ledger for the given session, newest first. Distinct from
// ListFilesBySession (which reads feature_files for *claimed* touches).
func ListClaimlessFilesBySession(db *sql.DB, sessionID string) ([]SessionFile, error) {
	if sessionID == "" {
		return nil, nil
	}
	rows, err := db.Query(`
		SELECT file_path, COALESCE(operation, ''), last_seen
		FROM session_files
		WHERE session_id = ?
		ORDER BY last_seen DESC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list claimless files for session %s: %w", sessionID, err)
	}
	defer rows.Close()
	var out []SessionFile
	for rows.Next() {
		var sf SessionFile
		if err := rows.Scan(&sf.FilePath, &sf.Operation, &sf.LastSeen); err != nil {
			continue
		}
		out = append(out, sf)
	}
	return out, rows.Err()
}
