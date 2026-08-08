package hooks

import (
	"database/sql"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/core/claimledger"
)

// closeClaimEpisodesForSession gives an END to every claim episode a dying
// session still holds, mirroring db.ReleaseAllClaimsForSession on the durable
// side.
//
// This is the answer to "what happens to an episode whose session dies without
// releasing". An interval with no end is not queryable as an interval, so every
// path that can observe a session ending has to close its episodes:
//
//   - SessionEnd → OutcomeAbandoned (the session ended and told us)
//   - the stale-session reaper → OutcomeExpired (it died and did not)
//   - `wipnote claims reconcile` → OutcomeExpired (backstop for the rest)
//
// A hard kill fires none of the first two, which is why the reconcile backstop
// exists and why readers treat a still-open episode as open-ended rather than
// as an error.
//
// Best-effort and silent: the claim ledger is observability, and a session must
// never fail to end because history could not be written. Errors go to the
// debug log only — Claude Code treats any hook stderr as a hook failure.
func closeClaimEpisodesForSession(database *sql.DB, projectDir, sessionID string, outcome claimledger.Outcome) {
	if projectDir == "" || sessionID == "" {
		return
	}
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	store := claimledger.NewStore(wipnoteDir)

	// The shard is keyed by ROOT session. A subagent session that ends is not
	// the owner of the shard, so resolve up the lineage before closing — and
	// close only THIS session's rows, never its siblings'.
	root := rootSessionForClaimLedger(database, sessionID)

	// One read-modify-write for the whole shard, scoped to THIS session's rows so
	// a child session ending does not close its parent's or its siblings'.
	closed, err := store.CloseAllForSession(root, sessionID, outcome, time.Now().UTC())
	if err != nil {
		debugLog(projectDir, "[claim-ledger] close episodes for %s: %v", sessionID[:minLen(sessionID, 8)], err)
		return
	}
	if closed > 0 {
		debugLog(projectDir, "[claim-ledger] closed %d episode(s) as %s for session %s",
			closed, outcome, sessionID[:minLen(sessionID, 8)])
	}
}

// rootSessionForClaimLedger resolves the root session that owns sessionID's
// shard, falling back to sessionID when no lineage row exists.
func rootSessionForClaimLedger(database *sql.DB, sessionID string) string {
	if database == nil || sessionID == "" {
		return sessionID
	}
	var root string
	err := database.QueryRow(
		`SELECT COALESCE(root_session_id, '') FROM agent_lineage_trace
		 WHERE session_id = ? AND root_session_id != '' LIMIT 1`, sessionID,
	).Scan(&root)
	if err != nil || root == "" {
		return sessionID
	}
	return root
}
