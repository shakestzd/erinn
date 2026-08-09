package main

import (
	"database/sql"
	"path/filepath"
)

// This file was lazy_reindex.go. It held the cold-clone lazy-rebuild machinery
// — lazySyncReindexHook, ensureIndexPopulated, isIndexWarm, runFullSyncReindex
// — whose whole purpose was deciding WHETHER the persistent read-index needed
// rebuilding before a read (bug-4b07fd94). With the projection now built
// in-process and in-memory (feat-fc3cc9e0) that question has one answer: it is
// always cold, because it did not exist a moment ago. The warmth check, the
// swappable hook and the "run a rebuild first" wrapper were left behind as
// husks that no production caller reached — ensureIndexPopulated's only caller
// (openReadOnlyDB) had already dropped it, and runFullSyncReindex had degenerated
// to building a projection and immediately discarding it. All four are deleted.
//
// hydrateCompatibilityDB is what survived, and it is no longer lazy or
// conditional: it is THE rebuild, run unconditionally by openDB for every
// command that asks for a projection.

// hydrateCompatibilityDB populates an ephemeral projection from the canonical
// .wipnote artifacts. It is the single rebuild path — openDB, the HTTP
// serve_child, the writer daemon and `wipnote reindex` all go through it, so
// every consumer sees the same projection built the same way.
//
// Pass ordering is load-bearing and mirrors the old full-reindex ordering:
//
//   - node passes run FIRST, because agent_events.feature_id carries a foreign
//     key to features(id);
//   - collectPlanIDs and collectSessionIDs must both run BEFORE reindexEdges,
//     which gates every edge on target validity — an unregistered plan or
//     session id silently drops each edge pointing at it (bug-d5eaf6a4,
//     bug-6ec28063);
//   - reindexSessionLedger must precede collectSessionIDs, which is what reads
//     the rows it projects.
//
// Deliberately NOT here: anything that walks git history or the whole sessions
// tree (reindexSessions, reindexCommitTrailers, reindexFeatureFiles,
// reindexArchCards). This function runs on the pre-run path of ordinary
// commands, and that is exactly the full-reindex cost blowup of bug-1f338b5b /
// bug-4e5816f4. Those passes belong to `wipnote reindex`, whose job is the full
// sweep.
func hydrateCompatibilityDB(database *sql.DB, wipnoteDir string) error {
	validIDs := make(map[string]bool)
	hydrateNodePasses(database, wipnoteDir, validIDs)
	hydrateEdgePasses(database, wipnoteDir, validIDs)
	return nil
}

// hydrateNodePasses indexes every canonical NODE and registers its id in
// validIDs. It is split from the edge phase so `wipnote reindex` can slot its
// extra session pass between the two — see hydrateEdgePasses for why that
// position is not negotiable.
func hydrateNodePasses(database *sql.DB, wipnoteDir string, validIDs map[string]bool) {
	projectDir := filepath.Dir(wipnoteDir)
	reindexTracks(database, wipnoteDir, projectDir, validIDs, false)
	for _, dir := range []string{"features", "bugs", "spikes"} {
		reindexFeatureDir(database, wipnoteDir, projectDir, dir, validIDs, false)
	}
	reindexWorkitemLedgerNodes(database, wipnoteDir, projectDir, validIDs, false)
	reindexClaimEpisodes(database, wipnoteDir, false)
	// Must follow reindexClaimEpisodes: it projects that pass's output.
	reindexActiveWorkItems(database, false)
}

// hydrateEdgePasses finishes the projection: it projects the sessions ledger,
// completes validIDs, then indexes every edge against it.
//
// Anything that writes a sessions row must already have run. Two separate
// things depend on it:
//
//   - collectSessionIDs reads the sessions table to decide which edge targets
//     are live. A session missing from it makes every work-item →
//     implemented_in → session edge fail the target-validity gate and get
//     tombstoned even though the session is right there (bug-6ec28063).
//   - reindexSessionLedger inserts a placeholder row for a session it has no
//     telemetry for, and deliberately does not overwrite a richer row that
//     already exists. Run it before the richer source and the placeholder wins
//     permanently.
func hydrateEdgePasses(database *sql.DB, wipnoteDir string, validIDs map[string]bool) {
	projectDir := filepath.Dir(wipnoteDir)
	reindexSessionLedger(database, wipnoteDir, false)
	// Needs both reindexSessionLedger (rows to update) and the node phase's
	// reindexActiveWorkItems (the value to set) — this is the first point where
	// both exist.
	applyActiveFeatureIDFromClaims(database, false)
	collectPlanIDs(wipnoteDir, validIDs)
	collectSessionIDs(database, validIDs)
	reindexEdges(database, wipnoteDir, validIDs)
	reindexWorkitemLedgerEdges(database, wipnoteDir, validIDs, false)
	fixImplementedInEdges(database)
	reindexPlanEdges(database, wipnoteDir)
	reindexPlanFeedback(database, wipnoteDir)
	reindexGateRecords(database, wipnoteDir, false)
	reindexRecaps(database, wipnoteDir, projectDir, false)
}
