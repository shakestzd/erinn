package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// CollaborationState describes the concurrent-claim state for a work item.
// When HasCollision is false the work item has at most one active claimant.
// When true, two or more root sessions hold concurrent active claims — the
// caller should surface attribution + timestamp for each so the user sees
// the collaboration/collision state.
type CollaborationState struct {
	WorkItemID   string
	HasCollision bool
	Claimants    []models.Claim
}

// ListClaimsForWorkItem returns all active claims for a given work item,
// ordered by created_at ASC (oldest first). Unlike GetActiveClaim, which
// returns only one row, this returns the full set — needed for parallel
// root sessions that each hold a distinct claim on the same item.
func ListClaimsForWorkItem(database *sql.DB, workItemID string) ([]models.Claim, error) {
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
		ORDER BY created_at ASC`, activeList)
	return queryClaims(database, query, workItemID)
}

// DetectCollaboration inspects the active claims for workItemID and returns a
// CollaborationState. HasCollision is true when two or more distinct sessions
// hold concurrent active claims on the same item — indicating parallel root
// CLIs or cross-harness concurrency. The caller is responsible for surfacing
// a visible warning; no hard block is applied (warn-and-allow policy).
func DetectCollaboration(database *sql.DB, workItemID string) (CollaborationState, error) {
	claims, err := ListClaimsForWorkItem(database, workItemID)
	if err != nil {
		return CollaborationState{}, fmt.Errorf("detect collaboration for %s: %w", workItemID, err)
	}

	// Deduplicate by OwnerSessionID so the same session holding multiple
	// sub-claims doesn't trip the collision detector.
	seen := map[string]bool{}
	unique := claims[:0:0] // zero-len slice sharing no backing array
	for _, c := range claims {
		if seen[c.OwnerSessionID] {
			continue
		}
		seen[c.OwnerSessionID] = true
		unique = append(unique, c)
	}

	return CollaborationState{
		WorkItemID:   workItemID,
		HasCollision: len(unique) > 1,
		Claimants:    unique,
	}, nil
}

// CollaborationSummary formats a human-readable collision/collaboration notice
// for display in `wipnote who` and session-start attribution blocks.
func CollaborationSummary(state CollaborationState) string {
	if !state.HasCollision {
		return ""
	}
	lines := make([]string, 0, len(state.Claimants)+1)
	lines = append(lines, fmt.Sprintf(
		"WARNING: %s has %d concurrent claimants (collaboration/collision detected):",
		state.WorkItemID, len(state.Claimants),
	))
	for _, c := range state.Claimants {
		ts := c.LeasedAt.UTC().Format(time.RFC3339)
		lines = append(lines, fmt.Sprintf(
			"  %s  session=%s  harness=%s  claimed=%s",
			c.ClaimID, c.OwnerSessionID, c.OwnerAgent, ts,
		))
	}
	lines = append(lines, "  (warn-and-allow: work continues; coordinate manually)")
	return strings.Join(lines, "\n")
}

// LiveCollisionState extends CollaborationState with a liveness filter.
// HasLiveCollision is true when at least one claimant OTHER than the caller's
// own session is actively live (heartbeat within the staleness threshold AND
// the session has not ended). LiveClaimants contains only the live foreign
// claimants (caller excluded).
//
// callerSessionID may be "" — in that case all live claimants are included.
// livenessThreshold is typically from LivenessStalenessThreshold(projectDir).
type LiveCollisionState struct {
	CollaborationState
	HasLiveCollision bool
	LiveClaimants    []models.Claim
}

// sessionHasEnded reports whether a session has a terminal status (completed,
// paused, failed) or a recorded end marker (end_commit or completed_at).
// A session that has ended is not a live collision — even if its last heartbeat
// is still within the staleness window — because it cannot be actively writing.
// Returns false on any query error (conservatively treats the session as live).
func sessionHasEnded(database *sql.DB, sessionID string) bool {
	if database == nil || sessionID == "" {
		return false
	}
	var status string
	var endCommit, completedAt sql.NullString
	err := database.QueryRow(
		`SELECT status, COALESCE(end_commit,''), COALESCE(completed_at,'')
		 FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&status, &endCommit, &completedAt)
	if err != nil {
		// Row missing or error — can't confirm ended; be conservative (not ended).
		return false
	}
	if status != "active" {
		return true
	}
	if endCommit.String != "" || completedAt.String != "" {
		return true
	}
	return false
}

// LiveCollision returns the live-collision state for workItemID: it runs
// DetectCollaboration, then filters claimants to those whose owner session is
// live per SessionLivenessByHeartbeat AND has not ended. Only foreign sessions
// (not equal to callerSessionID) contribute — a session reclaiming its own
// item is not a collision.
//
// A claim owned by an ended session (terminal status, non-null end_commit, or
// non-null completed_at) is never a live collision, even if the heartbeat is
// still fresh. This handles Ctrl+C kills where the heartbeat window hasn't
// yet expired but the session row records a terminal state.
func LiveCollision(database *sql.DB, workItemID, callerSessionID string, livenessThreshold time.Duration) (LiveCollisionState, error) {
	coll, err := DetectCollaboration(database, workItemID)
	if err != nil {
		return LiveCollisionState{}, err
	}
	state := LiveCollisionState{CollaborationState: coll}
	for _, c := range coll.Claimants {
		if c.OwnerSessionID == callerSessionID {
			continue // own session reclaiming — not a foreign collision
		}
		if sessionHasEnded(database, c.OwnerSessionID) {
			continue // session ended — claim is orphaned, not live
		}
		if SessionLivenessByHeartbeat(database, c.OwnerSessionID, livenessThreshold) {
			state.LiveClaimants = append(state.LiveClaimants, c)
		}
	}
	state.HasLiveCollision = len(state.LiveClaimants) > 0
	return state, nil
}

// LiveCollisionMessage formats a human-readable refusal message for a live
// collision. Returns "" when state.HasLiveCollision is false.
func LiveCollisionMessage(state LiveCollisionState) string {
	if !state.HasLiveCollision {
		return ""
	}
	lines := make([]string, 0, len(state.LiveClaimants)+2)
	for _, c := range state.LiveClaimants {
		ts := c.LeasedAt.UTC().Format(time.RFC3339)
		lines = append(lines, fmt.Sprintf(
			"%s is actively claimed by session %s (agent %s, since %s).\n"+
				"Coordinate, or re-run with --force to claim anyway.",
			state.WorkItemID, c.OwnerSessionID, c.OwnerAgent, ts,
		))
	}
	return strings.Join(lines, "\n")
}

// BackfillParentSession sets parent_session_id on a child session row when
// the parent record arrives after the child was already written (out-of-order).
// This is idempotent: if parent_session_id is already set to the same value
// the UPDATE is a no-op.
func BackfillParentSession(database *sql.DB, childSessionID, parentSessionID string) error {
	query, args := BackfillParentSessionStmt(childSessionID, parentSessionID)
	if _, err := database.Exec(query, args...); err != nil {
		return fmt.Errorf("backfill parent session %s->%s: %w", childSessionID, parentSessionID, err)
	}
	return nil
}

// BackfillParentSessionStmt builds the parameterized UPDATE that
// BackfillParentSession Execs, WITHOUT executing it, so the hot-path
// subagent-start hook can route the exact same statement through the daemon's
// enqueue-only seam instead of blocking a direct writable handle under a held
// external write lock (bug-c9ec25a4). The returned (sql, args) is byte-for-byte
// equivalent to what BackfillParentSession binds today.
//
// JSON-TRANSPORT SAFETY: both args are plain strings — transport-safe
// primitives the daemon can JSON-encode and re-bind identically. No
// sql.NullString / time.Time crosses the wire.
func BackfillParentSessionStmt(childSessionID, parentSessionID string) (string, []any) {
	return `UPDATE sessions SET parent_session_id = ? WHERE session_id = ?`,
		[]any{parentSessionID, childSessionID}
}

// GetClaimIdentity returns the claim owner, session family, harness, work item,
// and execution root for a session — the identity fields needed by `wipnote who`,
// /api/features, and dashboard panels. Returns nil when the session has no active claim.
func GetClaimIdentity(database *sql.DB, sessionID string) (*ClaimIdentity, error) {
	activeList := activeStatusList()
	query := fmt.Sprintf(`
		SELECT c.claim_id, c.work_item_id, c.owner_session_id, c.owner_agent,
		       c.claimed_by_agent_id, c.status, c.leased_at,
		       COALESCE(s.session_family_id, s.session_id) AS family_id,
		       COALESCE(s.is_subagent, 0) AS is_subagent,
		       COALESCE(s.parent_session_id, '') AS parent_session_id
		FROM claims c
		LEFT JOIN sessions s ON s.session_id = c.owner_session_id
		WHERE c.owner_session_id = ? AND c.status IN (%s)
		ORDER BY c.created_at DESC
		LIMIT 1`, activeList)

	row := database.QueryRow(query, sessionID)
	id := &ClaimIdentity{}
	var leasedStr string
	var isSubagent int
	err := row.Scan(
		&id.ClaimID, &id.WorkItemID, &id.OwnerSessionID, &id.Harness,
		&id.AgentID, &id.ClaimStatus, &leasedStr,
		&id.SessionFamilyID, &isSubagent, &id.ExecutionRoot,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get claim identity for session %s: %w", sessionID, err)
	}
	id.LeasedAt, _ = time.Parse(time.RFC3339, leasedStr)
	id.IsSubagent = isSubagent == 1
	// ExecutionRoot is COALESCE(parent_session_id, '') from the query: for a
	// subagent claim it should be the parent/root session, NOT the child's own
	// session. Only fall back to OwnerSessionID when no parent is recorded
	// (root sessions, or subagents whose parent linkage was never written) —
	// the old `|| id.IsSubagent` clause clobbered a valid parent_session_id
	// with the child session, making the execution root point at the subagent
	// itself instead of its parent/root.
	if id.ExecutionRoot == "" {
		id.ExecutionRoot = id.OwnerSessionID
	}
	return id, nil
}

// ClaimIdentity holds the identity/attribution fields for a session's active claim.
// These fields are exported for `wipnote who`, /api/features, and dashboard panels.
type ClaimIdentity struct {
	ClaimID         string
	WorkItemID      string
	OwnerSessionID  string
	Harness         string // owner_agent field (claude-code, codex-cli, gemini-cli)
	AgentID         string // claimed_by_agent_id (subagent discriminator)
	ClaimStatus     models.ClaimStatus
	LeasedAt        time.Time
	SessionFamilyID string
	ExecutionRoot   string
	IsSubagent      bool
}
