package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/internal/models"
)

// InsertSession creates a new session row.
func InsertSession(db *sql.DB, s *models.Session) error {
	_, err := db.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, parent_session_id,
			parent_event_id, created_at, status, start_commit,
			is_subagent, model, active_feature_id, git_remote_url, project_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SessionID, s.AgentAssigned, nullStr(s.ParentSessionID),
		nullStr(s.ParentEventID), s.CreatedAt.UTC().Format(time.RFC3339),
		s.Status, nullStr(s.StartCommit),
		s.IsSubagent, nullStr(s.Model), nullStr(s.ActiveFeatureID),
		nullStr(s.GitRemoteURL),
		nullStr(s.ProjectDir),
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
func UpsertSession(db *sql.DB, s *models.Session) error {
	_, err := db.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, parent_session_id,
			parent_event_id, created_at, status, start_commit,
			is_subagent, model, active_feature_id, git_remote_url, project_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			agent_assigned=excluded.agent_assigned,
			parent_session_id=excluded.parent_session_id,
			parent_event_id=excluded.parent_event_id,
			created_at=excluded.created_at,
			status=excluded.status,
			start_commit=excluded.start_commit,
			is_subagent=excluded.is_subagent,
			model=excluded.model,
			active_feature_id=excluded.active_feature_id,
			git_remote_url=excluded.git_remote_url,
			project_dir=excluded.project_dir
		WHERE sessions.agent_assigned = '__hook__'`,
		s.SessionID, s.AgentAssigned, nullStr(s.ParentSessionID),
		nullStr(s.ParentEventID), s.CreatedAt.UTC().Format(time.RFC3339),
		s.Status, nullStr(s.StartCommit),
		s.IsSubagent, nullStr(s.Model), nullStr(s.ActiveFeatureID),
		nullStr(s.GitRemoteURL),
		nullStr(s.ProjectDir),
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
			status, is_subagent, model, active_feature_id, project_dir
		FROM sessions WHERE session_id = ?`, sessionID)

	s := &models.Session{}
	var parentSess, parentEvt, completedAt, model, activeFeat, projectDir sql.NullString
	var createdStr string

	err := row.Scan(
		&s.SessionID, &s.AgentAssigned, &parentSess,
		&parentEvt, &createdStr, &completedAt,
		&s.TotalEvents, &s.TotalTokensUsed, &s.ContextDrift,
		&s.Status, &s.IsSubagent, &model, &activeFeat, &projectDir,
	)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", sessionID, err)
	}

	s.ParentSessionID = parentSess.String
	s.ParentEventID = parentEvt.String
	s.Model = model.String
	s.ActiveFeatureID = activeFeat.String
	s.ProjectDir = projectDir.String
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)

	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339, completedAt.String)
		s.CompletedAt = &t
	}
	return s, nil
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
	// Claim lookup uses two paths, tried in order:
	//   1. claimed_by_agent_id = agentID  — the direct per-agent claim
	//   2. owner_session_id   = sessionID — fallback for subagent tool calls,
	//      which share the orchestrator's session_id but carry a distinct
	//      agent_id that never had its own claim row (bug-cb4918d8). The
	//      orchestrator's claim is keyed on owner_session_id, so this resolves
	//      the parent's claim for any subagent running under it.
	// Both paths are expressed as correlated subqueries so the outer row
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

