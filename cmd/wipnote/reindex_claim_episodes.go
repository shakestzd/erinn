package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shakestzd/wipnote/core/claimledger"
	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// reindexClaimEpisodes ingests the canonical claim ledger (.wipnote/claims/*.html,
// shards plus the archive) into the claim_episodes read index. Returns
// (files, upserted, errCount).
//
// This is a FULL pass every time, never incremental. Two reasons: the ledger is
// small (one file per root session, tens of rows each), and — decisively — the
// incremental path keys off git-changed files, while ledger writes are
// committed asynchronously through the commit queue. An episode written but not
// yet flushed would be invisible to a git-diff-driven pass, which is precisely
// the window in which attribution queries run.
//
// The purge before ingest is what keeps archiving honest: compaction deletes a
// shard and folds its rows into archive.html, so without a purge the read index
// would carry the episode twice under the old and new source_file.
func reindexClaimEpisodes(database *sql.DB, wipnoteDir string, verbose bool) (int, int, int) {
	store := claimledger.NewStore(wipnoteDir)
	files, err := store.Files()
	if err != nil {
		if verbose {
			fmt.Printf("reindex claims: list ledger files: %v\n", err)
		}
		return 0, 0, 1
	}

	if err := dbpkg.PurgeClaimEpisodes(database); err != nil {
		if verbose {
			fmt.Printf("reindex claims: purge: %v\n", err)
		}
		return len(files), 0, 1
	}

	upserted, errCount := 0, 0
	for _, path := range files {
		episodes, readErr := claimledger.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			errCount++
			if verbose {
				fmt.Printf("reindex claims: parse %s: %v\n", filepath.Base(path), readErr)
			}
			continue
		}
		source := store.RelPath(path)
		for _, e := range episodes {
			if upsertErr := dbpkg.UpsertClaimEpisode(database, dbpkg.ClaimEpisode{
				EpisodeID:     e.ID,
				WorkItemID:    e.WorkItemID,
				SessionID:     e.SessionID,
				RootSessionID: e.RootSessionID,
				AgentID:       e.AgentID,
				StartedAt:     e.StartedAt,
				EndedAt:       e.EndedAt,
				Outcome:       string(e.Outcome),
				SourceFile:    source,
			}); upsertErr != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex claims: upsert %s: %v\n", e.ID, upsertErr)
				}
				continue
			}
			upserted++
		}
	}
	return len(files), upserted, errCount
}

// reindexActiveWorkItems projects the OPEN claim episodes into the
// active_work_items table. Returns the number of rows written.
//
// active_work_items is single-mutable-slot CURRENT state — "what does this
// agent hold right now", keyed (session_id, agent_id). The claim ledger records
// the same facts as INTERVALS, and an interval with no end is exactly a live
// claim, so current state is a projection of the ledger rather than an
// independent source. core/claimledger's package doc states that relationship
// directly; this pass is it, expressed in code.
//
// Why it has to exist: at runtime the hooks write active_work_items directly,
// and before feat-fc3cc9e0 those rows persisted in the project database. They
// no longer do — the read index is rebuilt per process from canonical artifacts
// — so without this pass the table is empty in every CLI invocation. That is
// not a cosmetic gap: sessions.active_feature_id reads flow through it, and
// GetResumableSessionForSessionAndWorkItem / ListHarnessGroupedResumableSessions
// both LEFT JOIN it, so an empty table silently turns every `wipnote continue`
// into "no resumable session metadata found".
//
// Ordering: must run after reindexClaimEpisodes, which is its input. It has no
// foreign key to sessions and registers nothing in validIDs, so its position
// relative to the session and edge passes is otherwise free.
//
// Last-open-wins on conflict. Two open episodes for one (session, agent) is a
// ledger inconsistency rather than a state to represent — the PK admits one row
// — so the most recently started claim is taken as current, which is what a
// re-claim without an explicit release looks like.
func reindexActiveWorkItems(database *sql.DB, verbose bool) int {
	if database == nil {
		return 0
	}
	if _, err := database.Exec(`DELETE FROM active_work_items`); err != nil {
		if verbose {
			fmt.Printf("reindex active work items: purge: %v\n", err)
		}
		return 0
	}

	// Latest open episode per (session, agent), selected explicitly rather than
	// by relying on INSERT OR REPLACE consuming an ORDERED SELECT in order —
	// that ordering is not guaranteed, and it silently picked the FIRST of two
	// open claims when a session started a second work item without releasing
	// the first. Ties on started_at (the ledger stores second resolution, so a
	// fast re-claim collides) break on episode_id so the result is stable.
	res, err := database.Exec(`
		INSERT OR REPLACE INTO active_work_items (session_id, agent_id, work_item_id, claimed_at)
		SELECT ce.session_id, ce.agent_id, ce.work_item_id, ce.started_at
		FROM claim_episodes ce
		WHERE ce.ended_at = ''
		  AND NOT EXISTS (
			SELECT 1 FROM claim_episodes later
			WHERE later.ended_at = ''
			  AND later.session_id = ce.session_id
			  AND later.agent_id = ce.agent_id
			  AND (later.started_at > ce.started_at
			       OR (later.started_at = ce.started_at AND later.episode_id > ce.episode_id))
		  )`)
	if err != nil {
		if verbose {
			fmt.Printf("reindex active work items: project open episodes: %v\n", err)
		}
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// applyActiveFeatureIDFromClaims sets sessions.active_feature_id from the
// projected active_work_items rows.
//
// This is the same current-state fact as active_work_items reached by a
// different key — "what is this SESSION on" rather than "what is this
// (session, agent) pair on" — and GetActiveFeatureIDForSession reads it
// directly. reindexSessionLedger does not populate the column, so without this
// it is NULL for every session in the projection.
//
// ORDERING: this MUST run after reindexSessionLedger, because it updates rows
// that pass inserts, and after reindexActiveWorkItems, which is its input.
// That places it in the edge phase even though it is conceptually node state —
// hydrateEdgePasses is the first point where both inputs exist.
//
// Which claim counts as "the session's" is not simply the root sentinel, and
// the difference is harness-shaped:
//
//   - Claude Code spawns subagents into the ROOT'S session id, distinguishing
//     them by agent_id, and the top-level owner writes AgentRootSentinel. A
//     subagent holding a different item must NOT overwrite what the session as
//     a whole is on, so the sentinel claim wins when one exists.
//   - Codex spawns subagents into their own sessions, and its launcher writes
//     its own agent id ("codex") rather than the sentinel. Keying on the
//     sentinel alone therefore leaves a Codex session's active_feature_id NULL
//     (TestRunWiSetStatus_CodexLauncherWritesLegacyColumn).
//
// The rule below is deliberately the NARROW one — sentinel only — and the Codex
// case is left failing rather than papered over. The obvious widening, "fall
// back to a claim whose session_id equals its root_session_id", does not work:
// a Claude Code SUBAGENT satisfies that too (it shares the root's session id),
// so the widening silently breaks
// TestRunWiSetStatus_SubagentsDoNotStompLegacyColumn — an attribution
// correctness guard. Verified by trying it.
//
// Nothing in the ledger separates "Codex launcher claim" from "Claude Code
// subagent claim": both are (session_id == root_session_id, non-sentinel
// agent_id). Closing this needs either the launchers to agree on writing the
// sentinel for the top-level owner, or the ledger to record which agent is the
// session's owner. That is a contract decision, not a query fix.
func applyActiveFeatureIDFromClaims(database *sql.DB, verbose bool) {
	if database == nil {
		return
	}
	if _, err := database.Exec(`
		UPDATE sessions
		SET active_feature_id = (
			SELECT work_item_id FROM active_work_items awi
			WHERE awi.session_id = sessions.session_id AND awi.agent_id = ?
		)
		WHERE EXISTS (
			SELECT 1 FROM active_work_items awi
			WHERE awi.session_id = sessions.session_id AND awi.agent_id = ?
		)`, dbpkg.AgentRootSentinel, dbpkg.AgentRootSentinel); err != nil && verbose {
		fmt.Printf("reindex active work items: set sessions.active_feature_id: %v\n", err)
	}
}
