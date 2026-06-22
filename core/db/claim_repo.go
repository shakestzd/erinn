package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// activeStatusList returns the active claim statuses as a SQL IN-list literal.
// Example: "'proposed','claimed','in_progress','blocked','handoff_pending'"
func activeStatusList() string {
	quoted := make([]string, len(models.ActiveClaimStatuses))
	for i, s := range models.ActiveClaimStatuses {
		quoted[i] = "'" + string(s) + "'"
	}
	return strings.Join(quoted, ",")
}

// ClaimItem creates a new claim for a work item. It first reaps expired claims,
// then attempts an atomic INSERT. If the work item already has an active claim
// by another session, returns an error describing the conflict.
func ClaimItem(db *sql.DB, claim *models.Claim, leaseDuration time.Duration) error {
	if _, err := ReapExpiredClaims(db); err != nil {
		return fmt.Errorf("reap before claim: %w", err)
	}

	now := time.Now().UTC()
	claim.LeasedAt = now
	claim.LeaseExpiresAt = now.Add(leaseDuration)
	claim.LastHeartbeatAt = now
	claim.CreatedAt = now
	claim.UpdatedAt = now

	if claim.Status == "" {
		claim.Status = models.ClaimProposed
	}
	if claim.OwnerAgent == "" {
		claim.OwnerAgent = "claude-code"
	}

	// Ensure FK-referenced rows exist. HTML is canonical; SQLite is a read
	// index that may not have the row yet (e.g. workitem tests, CLI-only usage).
	ensureFeatureRow(db, claim.WorkItemID)
	ensureSessionRow(db, claim.OwnerSessionID, claim.OwnerAgent)

	activeList := activeStatusList()
	// Allow multiple agents to claim the same work item by scoping the
	// conflict check to (work_item_id, claimed_by_agent_id). This enables
	// per-agent attribution: the orchestrator and each subagent can
	// independently claim the same feature.
	query := fmt.Sprintf(`
		INSERT INTO claims (
			claim_id, work_item_id, track_id, owner_session_id, owner_agent,
			claimed_by_agent_id,
			status, intended_output, write_scope,
			leased_at, lease_expires_at, last_heartbeat_at,
			dependencies, progress_notes, blocker_reason,
			created_at, updated_at
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM claims
			WHERE work_item_id = ?
			  AND claimed_by_agent_id = ?
			  AND status IN (%s)
		)`, activeList)

	result, err := db.Exec(query,
		claim.ClaimID, claim.WorkItemID, nullStr(claim.TrackID),
		claim.OwnerSessionID, claim.OwnerAgent,
		claim.ClaimedByAgentID,
		string(claim.Status), nullStr(claim.IntendedOutput),
		nullJSON(claim.WriteScope),
		now.Format(time.RFC3339), claim.LeaseExpiresAt.Format(time.RFC3339),
		now.Format(time.RFC3339),
		nullJSON(claim.Dependencies), nullStr(claim.ProgressNotes),
		nullStr(claim.BlockerReason),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
		claim.WorkItemID, claim.ClaimedByAgentID,
	)
	if err != nil {
		return fmt.Errorf("insert claim %s: %w", claim.ClaimID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("claim rows affected: %w", err)
	}
	if rows == 0 {
		existing, qErr := GetActiveClaim(db, claim.WorkItemID)
		if qErr != nil {
			return fmt.Errorf("work item %s already has an active claim (lookup failed: %w)", claim.WorkItemID, qErr)
		}
		return fmt.Errorf("work item %s already claimed by session %s (claim %s, status %s)\nClaims expire after 30 minutes. Run 'wipnote claim list' to check status.",
			claim.WorkItemID, existing.OwnerSessionID, existing.ClaimID, existing.Status)
	}
	return nil
}

// ClaimItemOrRenew creates a new claim or renews the lease on an existing one.
// The renewal identity is (work_item_id, claimed_by_agent_id, owner_session_id):
// an active claim is refreshed in-place only when the SAME session re-claims.
// A DIFFERENT session inserts a new claimant row even when it shares
// claimed_by_agent_id (e.g. two root sessions from the same harness, where
// claimed_by_agent_id is identically empty) — without owner_session_id in the
// identity the second root would silently renew the first's row and
// DetectCollaboration would never see the "two root sessions, same work item"
// collision. This remains idempotent: calling it N times from one session on
// the same item/agent always yields exactly one live row with a fresh lease.
func ClaimItemOrRenew(db *sql.DB, claim *models.Claim, leaseDuration time.Duration) error {
	if _, err := ReapExpiredClaims(db); err != nil {
		return fmt.Errorf("reap before claim: %w", err)
	}

	now := time.Now().UTC()
	newExpiry := now.Add(leaseDuration)
	activeList := activeStatusList()

	// Try to renew an existing active claim first. The renewal must match the
	// owning session too, so a second root session (same harness, same empty
	// claimed_by_agent_id) does not renew the first session's row.
	renewQuery := fmt.Sprintf(`
		UPDATE claims
		SET last_heartbeat_at = ?, lease_expires_at = ?, updated_at = ?
		WHERE work_item_id = ?
		  AND claimed_by_agent_id = ?
		  AND owner_session_id = ?
		  AND status IN (%s)`, activeList)

	result, err := db.Exec(renewQuery,
		now.Format(time.RFC3339), newExpiry.Format(time.RFC3339),
		now.Format(time.RFC3339),
		claim.WorkItemID, claim.ClaimedByAgentID, claim.OwnerSessionID,
	)
	if err != nil {
		return fmt.Errorf("renew claim for %s/%s/%s: %w", claim.WorkItemID, claim.ClaimedByAgentID, claim.OwnerSessionID, err)
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		// Existing live claim for THIS session refreshed — done.
		return nil
	}

	// No live claim exists — insert a fresh one.
	claim.LeasedAt = now
	claim.LeaseExpiresAt = newExpiry
	claim.LastHeartbeatAt = now
	claim.CreatedAt = now
	claim.UpdatedAt = now

	if claim.Status == "" {
		claim.Status = models.ClaimProposed
	}
	if claim.OwnerAgent == "" {
		claim.OwnerAgent = "claude-code"
	}

	ensureFeatureRow(db, claim.WorkItemID)
	ensureSessionRow(db, claim.OwnerSessionID, claim.OwnerAgent)

	_, err = db.Exec(`
		INSERT INTO claims (
			claim_id, work_item_id, track_id, owner_session_id, owner_agent,
			claimed_by_agent_id,
			status, intended_output, write_scope,
			leased_at, lease_expires_at, last_heartbeat_at,
			dependencies, progress_notes, blocker_reason,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		claim.ClaimID, claim.WorkItemID, nullStr(claim.TrackID),
		claim.OwnerSessionID, claim.OwnerAgent,
		claim.ClaimedByAgentID,
		string(claim.Status), nullStr(claim.IntendedOutput),
		nullJSON(claim.WriteScope),
		now.Format(time.RFC3339), newExpiry.Format(time.RFC3339),
		now.Format(time.RFC3339),
		nullJSON(claim.Dependencies), nullStr(claim.ProgressNotes),
		nullStr(claim.BlockerReason),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert claim %s: %w", claim.ClaimID, err)
	}
	return nil
}

// HeartbeatClaim renews the lease on an active claim owned by sessionID.
// Updates last_heartbeat_at and extends lease_expires_at by leaseDuration.
func HeartbeatClaim(db *sql.DB, claimID, sessionID string, leaseDuration time.Duration) error {
	now := time.Now().UTC()
	newExpiry := now.Add(leaseDuration)
	activeList := activeStatusList()

	query := fmt.Sprintf(`
		UPDATE claims
		SET last_heartbeat_at = ?, lease_expires_at = ?, updated_at = ?
		WHERE claim_id = ?
		  AND owner_session_id = ?
		  AND status IN (%s)`, activeList)

	result, err := db.Exec(query,
		now.Format(time.RFC3339), newExpiry.Format(time.RFC3339),
		now.Format(time.RFC3339), claimID, sessionID,
	)
	if err != nil {
		return fmt.Errorf("heartbeat claim %s: %w", claimID, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("claim %s not found, not owned by session %s, or not active", claimID, sessionID)
	}
	return nil
}

// writeScopePathsCap is the maximum number of paths stored in write_scope.paths.
// Oldest paths are dropped when this cap is reached (FIFO eviction).
const writeScopePathsCap = 200

// HeartbeatClaimByWorkItem renews the lease on the active claim for a work item
// and appends writePath (if non-empty) to write_scope.paths with dedup + FIFO cap.
//
// This is the main hook entry point — hooks know the work item ID, not the claim ID.
//
// Ephemeral/reindex contract: write_scope is ephemeral, stored only in SQLite.
// It is NOT persisted to canonical HTML. On reindex, claims are rebuilt from HTML
// with empty write_scope.paths; paths re-accumulate as the session fires heartbeats.
// No extra reset logic is needed.
func HeartbeatClaimByWorkItem(db *sql.DB, workItemID, sessionID, writePath string, leaseDuration time.Duration) error {
	query, args := HeartbeatClaimByWorkItemStmt(workItemID, sessionID, writePath, leaseDuration)
	result, err := db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("heartbeat claim for work item %s: %w", workItemID, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no active claim for work item %s owned by session %s", workItemID, sessionID)
	}
	return nil
}

// HeartbeatClaimByWorkItemStmt builds the parameterized UPDATE that
// HeartbeatClaimByWorkItem Execs, WITHOUT executing it, so the hot-path
// PreToolUse hook can route the exact same statement through the daemon's
// enqueue-only seam instead of blocking a direct writable handle under a held
// external write lock (bug-d792aee6 finding 2). The returned (sql, args) is
// byte-for-byte equivalent to what HeartbeatClaimByWorkItem binds today.
//
// JSON-TRANSPORT SAFETY: the daemon JSON-encodes args over the wire
// (core/daemon/apply.DerivedOp), so every arg here MUST be a transport-safe
// primitive — string / number / nil. Times are rendered as RFC3339 STRINGS
// (never time.Time), and there are no sql.NullString args. A plain string binds
// identically on the daemon's ExecContext and the direct fallback Exec.
//
// The write_scope SET expression is a single CASE: no-op when writePath is
// empty (?4 = ''), otherwise merges + dedupes + FIFO-caps to writeScopePathsCap.
//
// Implementation note: modernc.org/sqlite rejects a correlated reference to
// the UPDATE target column (write_scope) inside a subquery used as an OFFSET
// expression. The workaround is ORDER BY mo DESC LIMIT cap (keeps the newest
// 'cap' entries) followed by an outer ORDER BY mo ASC to restore FIFO order.
// This avoids the second correlated json_each call that would have been needed
// to compute the COUNT for the OFFSET. All other correlated json_each refs to
// write_scope (inside the CASE ELSE body, not inside OFFSET) are accepted.
//
// Dedup: GROUP BY value with MIN(key) preserves the original FIFO slot for a
// re-appended existing path; a new path gets sentinel key 1000000000 so it
// always sorts last. ORDER BY mo DESC LIMIT writeScopePathsCap drops the
// oldest entries (smallest mo). The outer ORDER BY mo ASC restores FIFO order.
// json_set preserves sibling keys (branch, worktree, directories).
func HeartbeatClaimByWorkItemStmt(workItemID, sessionID, writePath string, leaseDuration time.Duration) (string, []any) {
	now := time.Now().UTC()
	newExpiry := now.Add(leaseDuration)
	activeList := activeStatusList()

	query := fmt.Sprintf(`
		UPDATE claims
		SET last_heartbeat_at = ?1,
		    lease_expires_at  = ?2,
		    updated_at        = ?3,
		    write_scope = CASE WHEN ?4 = '' THEN write_scope ELSE
		      json_set(
		        COALESCE(write_scope,'{}'),
		        '$.paths',
		        (SELECT json_group_array(p) FROM
		          (SELECT p FROM
		            (SELECT value AS p, MIN(key) AS mo
		             FROM (
		               SELECT value, key
		               FROM json_each(COALESCE(json_extract(write_scope,'$.paths'),'[]'))
		               UNION ALL SELECT ?4 AS value, 1000000000 AS key
		             )
		             GROUP BY value
		             ORDER BY mo DESC LIMIT %d
		            )
		            ORDER BY mo ASC
		          )
		        )
		      )
		    END
		WHERE work_item_id = ?5
		  AND owner_session_id = ?6
		  AND status IN (%s)`,
		writeScopePathsCap, activeList)

	args := []any{
		now.Format(time.RFC3339), newExpiry.Format(time.RFC3339),
		now.Format(time.RFC3339), writePath,
		workItemID, sessionID,
	}
	return query, args
}

// TransitionClaim moves a claim to a new status, enforcing the state machine.
// Returns error if the transition is invalid per ValidClaimTransitions.
func TransitionClaim(db *sql.DB, claimID string, toStatus models.ClaimStatus) error {
	claim, err := GetClaim(db, claimID)
	if err != nil {
		return fmt.Errorf("get claim for transition: %w", err)
	}
	if !claim.CanTransitionTo(toStatus) {
		return fmt.Errorf("invalid transition %s -> %s for claim %s", claim.Status, toStatus, claimID)
	}
	now := time.Now().UTC()
	_, err = db.Exec(`
		UPDATE claims SET status = ?, updated_at = ? WHERE claim_id = ?`,
		string(toStatus), now.Format(time.RFC3339), claimID,
	)
	if err != nil {
		return fmt.Errorf("transition claim %s: %w", claimID, err)
	}
	return nil
}

// ReleaseClaim sets a claim to completed or abandoned.
// terminalStatus must be ClaimCompleted or ClaimAbandoned.
func ReleaseClaim(db *sql.DB, claimID, sessionID string, terminalStatus models.ClaimStatus) error {
	if terminalStatus != models.ClaimCompleted && terminalStatus != models.ClaimAbandoned {
		return fmt.Errorf("terminalStatus must be completed or abandoned, got %s", terminalStatus)
	}
	now := time.Now().UTC()
	result, err := db.Exec(`
		UPDATE claims SET status = ?, updated_at = ?
		WHERE claim_id = ? AND owner_session_id = ?`,
		string(terminalStatus), now.Format(time.RFC3339), claimID, sessionID,
	)
	if err != nil {
		return fmt.Errorf("release claim %s: %w", claimID, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("claim %s not found or not owned by session %s", claimID, sessionID)
	}
	return nil
}

// ReleaseAllClaimsForSession marks all active claims for a session as abandoned.
// Called on session end. Returns the number of claims released.
func ReleaseAllClaimsForSession(db *sql.DB, sessionID string) (int, error) {
	now := time.Now().UTC()
	activeList := activeStatusList()

	query := fmt.Sprintf(`
		UPDATE claims SET status = 'abandoned', write_scope = NULL, updated_at = ?
		WHERE owner_session_id = ?
		  AND status IN (%s)`, activeList)

	result, err := db.Exec(query, now.Format(time.RFC3339), sessionID)
	if err != nil {
		return 0, fmt.Errorf("release all claims for session %s: %w", sessionID, err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// ReapExpiredClaims transitions all expired-lease active claims to ClaimExpired.
// Returns the number of claims reaped.
func ReapExpiredClaims(db *sql.DB) (int, error) {
	query, args := ReapExpiredClaimsStmt()
	result, err := db.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("reap expired claims: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// ReapExpiredClaimsStmt builds the parameterized UPDATE that ReapExpiredClaims
// Execs, WITHOUT executing it, so the hot-path PreToolUse hook can route the
// exact same statement through the daemon's enqueue-only seam instead of
// blocking a direct writable handle under a held external write lock
// (bug-d792aee6 finding 2). The returned (sql, args) is byte-for-byte
// equivalent to what ReapExpiredClaims binds today.
//
// JSON-TRANSPORT SAFETY: both args are the SAME RFC3339 time STRING (now), a
// transport-safe primitive the daemon can JSON-encode and re-bind identically.
// No sql.NullString / time.Time crosses the wire.
func ReapExpiredClaimsStmt() (string, []any) {
	now := time.Now().UTC()
	activeList := activeStatusList()

	query := fmt.Sprintf(`
		UPDATE claims SET status = 'expired', write_scope = NULL, updated_at = ?
		WHERE lease_expires_at < ?
		  AND status IN (%s)`, activeList)

	nowStr := now.Format(time.RFC3339)
	return query, []any{nowStr, nowStr}
}

// GetActiveClaim returns the active claim for a work item, or nil if none.
func GetActiveClaim(db *sql.DB, workItemID string) (*models.Claim, error) {
	activeList := activeStatusList()
	query := fmt.Sprintf(`
		SELECT claim_id, work_item_id, track_id, owner_session_id, owner_agent,
		       claimed_by_agent_id,
		       status, intended_output, write_scope,
		       leased_at, lease_expires_at, last_heartbeat_at,
		       dependencies, progress_notes, blocker_reason,
		       created_at, updated_at
		FROM claims
		WHERE work_item_id = ? AND status IN (%s)
		LIMIT 1`, activeList)

	row := db.QueryRow(query, workItemID)
	c, err := scanClaim(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active claim for %s: %w", workItemID, err)
	}
	return c, nil
}

// HasActiveClaimByAgent returns true if the given agentID has any active claim
// (status IN active statuses). For the orchestrator (agentID=""), returns true
// if any claim has claimed_by_agent_id="" with active status.
func HasActiveClaimByAgent(db *sql.DB, agentID string) bool {
	activeList := activeStatusList()
	query := fmt.Sprintf(`
		SELECT 1 FROM claims
		WHERE claimed_by_agent_id = ?
		  AND status IN (%s)
		LIMIT 1`, activeList)

	var one int
	err := db.QueryRow(query, agentID).Scan(&one)
	return err == nil
}

// GetClaim returns a claim by ID.
func GetClaim(db *sql.DB, claimID string) (*models.Claim, error) {
	row := db.QueryRow(`
		SELECT claim_id, work_item_id, track_id, owner_session_id, owner_agent,
		       claimed_by_agent_id,
		       status, intended_output, write_scope,
		       leased_at, lease_expires_at, last_heartbeat_at,
		       dependencies, progress_notes, blocker_reason,
		       created_at, updated_at
		FROM claims WHERE claim_id = ?`, claimID)

	c, err := scanClaim(row)
	if err != nil {
		return nil, fmt.Errorf("get claim %s: %w", claimID, err)
	}
	return c, nil
}

// ListActiveClaimsBySession returns all active claims for a session.
func ListActiveClaimsBySession(db *sql.DB, sessionID string) ([]models.Claim, error) {
	activeList := activeStatusList()
	query := fmt.Sprintf(`
		SELECT claim_id, work_item_id, track_id, owner_session_id, owner_agent,
		       claimed_by_agent_id,
		       status, intended_output, write_scope,
		       leased_at, lease_expires_at, last_heartbeat_at,
		       dependencies, progress_notes, blocker_reason,
		       created_at, updated_at
		FROM claims
		WHERE owner_session_id = ? AND status IN (%s)
		ORDER BY created_at DESC`, activeList)

	return queryClaims(db, query, sessionID)
}

// ListClaims returns claims matching the given filters.
// If sessionID is empty, returns all. If statusFilter is empty, returns all statuses.
func ListClaims(db *sql.DB, sessionID, statusFilter string, limit int) ([]models.Claim, error) {
	if limit <= 0 {
		limit = 100
	}

	var conditions []string
	var args []any

	if sessionID != "" {
		conditions = append(conditions, "owner_session_id = ?")
		args = append(args, sessionID)
	}
	if statusFilter != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, statusFilter)
	}

	query := `
		SELECT claim_id, work_item_id, track_id, owner_session_id, owner_agent,
		       claimed_by_agent_id,
		       status, intended_output, write_scope,
		       leased_at, lease_expires_at, last_heartbeat_at,
		       dependencies, progress_notes, blocker_reason,
		       created_at, updated_at
		FROM claims`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	return queryClaims(db, query, args...)
}

// scanClaim scans a single claim row from a *sql.Row.
// The SELECT must include claimed_by_agent_id between owner_agent and status.
func scanClaim(row *sql.Row) (*models.Claim, error) {
	c := &models.Claim{}
	var (
		trackID, intendedOutput, progressNotes, blockerReason sql.NullString
		writeScope, dependencies                              sql.NullString
		leasedStr, expiresStr, heartbeatStr                   string
		createdStr, updatedStr                                string
	)
	err := row.Scan(
		&c.ClaimID, &c.WorkItemID, &trackID, &c.OwnerSessionID, &c.OwnerAgent,
		&c.ClaimedByAgentID,
		&c.Status, &intendedOutput, &writeScope,
		&leasedStr, &expiresStr, &heartbeatStr,
		&dependencies, &progressNotes, &blockerReason,
		&createdStr, &updatedStr,
	)
	if err != nil {
		return nil, err
	}
	c.TrackID = trackID.String
	c.IntendedOutput = intendedOutput.String
	c.ProgressNotes = progressNotes.String
	c.BlockerReason = blockerReason.String
	if writeScope.Valid && writeScope.String != "" {
		c.WriteScope = json.RawMessage(writeScope.String)
	}
	if dependencies.Valid && dependencies.String != "" {
		c.Dependencies = json.RawMessage(dependencies.String)
	}
	c.LeasedAt, _ = time.Parse(time.RFC3339, leasedStr)
	c.LeaseExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
	c.LastHeartbeatAt, _ = time.Parse(time.RFC3339, heartbeatStr)
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	c.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return c, nil
}

// scanClaimRow scans a claim from a *sql.Rows (multi-row cursor).
// The SELECT must include claimed_by_agent_id between owner_agent and status.
func scanClaimRow(rows *sql.Rows) (models.Claim, error) {
	c := models.Claim{}
	var (
		trackID, intendedOutput, progressNotes, blockerReason sql.NullString
		writeScope, dependencies                              sql.NullString
		leasedStr, expiresStr, heartbeatStr                   string
		createdStr, updatedStr                                string
	)
	err := rows.Scan(
		&c.ClaimID, &c.WorkItemID, &trackID, &c.OwnerSessionID, &c.OwnerAgent,
		&c.ClaimedByAgentID,
		&c.Status, &intendedOutput, &writeScope,
		&leasedStr, &expiresStr, &heartbeatStr,
		&dependencies, &progressNotes, &blockerReason,
		&createdStr, &updatedStr,
	)
	if err != nil {
		return c, err
	}
	c.TrackID = trackID.String
	c.IntendedOutput = intendedOutput.String
	c.ProgressNotes = progressNotes.String
	c.BlockerReason = blockerReason.String
	if writeScope.Valid && writeScope.String != "" {
		c.WriteScope = json.RawMessage(writeScope.String)
	}
	if dependencies.Valid && dependencies.String != "" {
		c.Dependencies = json.RawMessage(dependencies.String)
	}
	c.LeasedAt, _ = time.Parse(time.RFC3339, leasedStr)
	c.LeaseExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
	c.LastHeartbeatAt, _ = time.Parse(time.RFC3339, heartbeatStr)
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	c.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return c, nil
}

// queryClaims executes a query and returns a slice of Claim.
func queryClaims(db *sql.DB, query string, args ...any) ([]models.Claim, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query claims: %w", err)
	}
	defer rows.Close()

	var claims []models.Claim
	for rows.Next() {
		c, err := scanClaimRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan claim row: %w", err)
		}
		claims = append(claims, c)
	}
	return claims, rows.Err()
}

// nullJSON returns sql.NullString for a JSON field — empty if nil or zero-length.
func nullJSON(raw json.RawMessage) sql.NullString {
	if len(raw) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(raw), Valid: true}
}

// UpdateClaimAgentID sets claimed_by_agent_id on the most recent active claim
// for the given work item. This is called by PostToolUse when a subagent runs
// "wipnote feature start <id>" — the hook knows the agent_id from the
// CloudEvent but the CLI doesn't have it in its environment.
func UpdateClaimAgentID(database *sql.DB, workItemID, agentID string) error {
	_, err := database.Exec(
		`UPDATE claims SET claimed_by_agent_id = ?, updated_at = ?
		 WHERE claim_id = (
		     SELECT claim_id FROM claims
		     WHERE work_item_id = ? AND status IN ('proposed','claimed','in_progress')
		     AND (claimed_by_agent_id = '' OR claimed_by_agent_id IS NULL)
		     ORDER BY created_at DESC LIMIT 1
		 )`,
		agentID, time.Now().UTC().Format(time.RFC3339), workItemID,
	)
	return err
}

// ensureFeatureRow creates a placeholder feature row if it doesn't exist.
// This handles the case where a feature is created via HTML but not yet indexed
// into the database, or when tests create features without database indexing.
// Best-effort: errors are logged but not returned, since HTML is canonical.
//
// The placeholder title is intentionally empty (not the feature ID) — callers
// that need a display name fall back to the HTML file or render an "untitled"
// label. Using the ID as a placeholder would corrupt the title column if the
// canonical insert never runs in this DB (bug-7f4a1a9c).
func ensureFeatureRow(db *sql.DB, featureID string) {
	if featureID == "" {
		return
	}
	itemType := "feature"
	switch {
	case strings.HasPrefix(featureID, "bug-"):
		itemType = "bug"
	case strings.HasPrefix(featureID, "spk-"):
		itemType = "spike"
	}
	now := time.Now().UTC()
	_, _ = db.Exec(`
		INSERT OR IGNORE INTO features (id, type, title, status, priority, created_at, updated_at)
		VALUES (?, ?, '', 'in-progress', 'medium', ?, ?)`,
		featureID, itemType, now.Format(time.RFC3339), now.Format(time.RFC3339))
}

// ensureSessionRow creates a placeholder session row if it doesn't exist.
// This handles the case where a session is referenced before it's been created,
// or when tests create claims without proper session setup.
// Best-effort: errors are ignored since the session row is not authoritative.
func ensureSessionRow(db *sql.DB, sessionID, agent string) {
	if agent == "" {
		agent = "claude-code"
	}
	now := time.Now().UTC()
	_, _ = db.Exec(`
		INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status, created_at)
		VALUES (?, ?, 'active', ?)`,
		sessionID, agent, now.Format(time.RFC3339))
}
