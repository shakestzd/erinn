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
	// the owner of the shard, so resolve the owning shard before closing — and
	// close only THIS session's rows, never its siblings'.
	//
	// An unresolvable root REFUSES rather than guesses. The previous fallback
	// ("use sessionID") turned a failed lookup into a write against a shard path
	// derived from the wrong session, which for a subagent is a durable mis-write
	// to canonical data — strictly worse than not writing at all. If the ledger
	// holds no episode for this session there is also nothing to close, so
	// returning here loses nothing.
	root := rootSessionForClaimLedger(wipnoteDir, sessionID)
	if root == "" {
		return
	}

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
// shard by asking the CLAIM LEDGER ITSELF which shard holds an episode for this
// session. Returns "" when it cannot be resolved — callers must refuse to write
// rather than guess a shard.
//
// This replaced a lookup against agent_lineage_trace, a table the compatibility
// projection never populates (feat-fc3cc9e0). That lookup therefore always
// found nothing and always took its `return sessionID` fallback, which for a
// subagent names a shard the session does not own. Because the ledger is
// canonical git-tracked state, that was a durable mis-write, not a lost
// derived-index update — the failure mode this whole cutover has to avoid.
//
// The ledger answers the question exactly: an episode records both SessionID
// and RootSessionID, so the shard that holds this session's episodes is found
// rather than inferred. Newest-first, so a session that appears under more than
// one root resolves to its most recent.
func rootSessionForClaimLedger(wipnoteDir, sessionID string) string {
	if wipnoteDir == "" || sessionID == "" {
		return ""
	}
	episodes, err := claimledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		return ""
	}
	for i := len(episodes) - 1; i >= 0; i-- {
		e := episodes[i]
		if e.SessionID != sessionID {
			continue
		}
		if e.RootSessionID != "" {
			return e.RootSessionID
		}
		// An episode with no recorded root is its own root.
		return sessionID
	}
	return ""
}
