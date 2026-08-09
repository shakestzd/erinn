package hooks

import (
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/sessionledger"
)

// recordSessionLedgerOpen writes the canonical row that says this session
// EXISTED, at the moment it starts.
//
// Start-time creation is the whole point. An implemented_in edge written when a
// work item completes then names a target that was already durable — there is
// no window in which canonical HTML points at something undurable — and a
// session that dies without ever archiving still leaves a row behind. The old
// alternative, writing the row at archive time, made durability depend on a
// retention timer that most sessions never reach (bug-10e166d8).
//
// ROOT SESSIONS ONLY. Subagents get synthetic sessions rows keyed by agent id
// (subagent_start.go); those ids are lineage bookkeeping, are never edge
// targets, and one row each would bury the real sessions in stubs.
//
// COST: exactly one constant-time tail append per session — SessionStart fires
// once, and Open is a no-op on the second call for the same id (resume,
// replayed hook). It must never move onto a per-tool-call path.
//
// Best-effort and silent, like every other canonical-artifact write on this
// hook: a session must not fail to start because history could not be written,
// and Claude Code treats any hook stderr as a hook failure.
func recordSessionLedgerOpen(projectDir string, s *models.Session) {
	if projectDir == "" || s == nil || s.SessionID == "" || s.IsSubagent {
		return
	}
	store := sessionledger.NewStore(filepath.Join(projectDir, ".wipnote"))
	written, err := store.Open(sessionledger.Record{
		SessionID:  s.SessionID,
		Harness:    s.Harness,
		ProjectDir: s.ProjectDir,
		StartedAt:  s.CreatedAt,
	})
	if err != nil {
		debugLog(projectDir, "[session-ledger] open %s: %v", s.SessionID[:minLen(s.SessionID, 8)], err)
		return
	}
	if written {
		debugLog(projectDir, "[session-ledger] recorded session %s", s.SessionID[:minLen(s.SessionID, 8)])
	}
}

// recordSessionLedgerClose stamps the end time on a session's canonical row.
//
// A missing row is not an error: the session predates the ledger, or it is a
// subagent session that deliberately never got one. Both are expected, so
// ErrNoRow is swallowed rather than logged as a failure.
func recordSessionLedgerClose(projectDir, sessionID string, endedAt time.Time) {
	if projectDir == "" || sessionID == "" {
		return
	}
	store := sessionledger.NewStore(filepath.Join(projectDir, ".wipnote"))
	if err := store.Close(sessionID, endedAt); err != nil && err != sessionledger.ErrNoRow {
		debugLog(projectDir, "[session-ledger] close %s: %v", sessionID[:minLen(sessionID, 8)], err)
	}
}
