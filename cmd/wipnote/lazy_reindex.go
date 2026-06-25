package main

import (
	"database/sql"
	"fmt"
	"path/filepath"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/storage"
)

// lazySyncReindexHook is the function called by ensureIndexPopulated when the
// index is missing or empty. In production it points at runFullSyncReindex.
// Tests may swap it to a stub to observe/count rebuild invocations without
// actually running a reindex.
var lazySyncReindexHook = runFullSyncReindex

// ensureIndexPopulated checks whether the SQLite read-index for the given
// project is empty (cold-clone scenario) and, if so, runs a synchronous full
// reindex before the caller performs any reads.
//
// Staleness check: COUNT(*) on features; if 0 also check graph_edges. When
// both are 0 the index is treated as cold and a synchronous reindex runs.
// When either table has rows the function returns immediately — hot path costs
// one SELECT round-trip.
//
// Interrupt safety: runFullSyncReindex uses INSERT OR REPLACE per row so a
// killed process leaves the DB consistent. Re-opening triggers a fresh build.
//
// Contention: if another writer holds the lock, dbpkg.Open returns a "database
// is locked" error surfaced to the caller with a clear message.
func ensureIndexPopulated(wipnoteDir string) error {
	projectDir := filepath.Dir(wipnoteDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return fmt.Errorf("lazy reindex: resolve db path: %w", err)
	}

	// Open (or create+migrate) the DB briefly to check warmth. This is the
	// same bootstrap open that OpenReadOnlyMigrated does; we close it before
	// launching the full reindex which opens its own writable handle.
	db, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("lazy reindex: open db for staleness check: %w", err)
	}
	warm := isIndexWarm(db)
	db.Close()

	if warm {
		return nil
	}

	// Index is cold. Print a single informational line (first-run UX) and run
	// the synchronous full reindex. Subsequent opens will be warm.
	fmt.Printf("[wipnote] first-run: building SQLite read-index from .wipnote/ HTML…\n")
	return lazySyncReindexHook(wipnoteDir)
}

// isIndexWarm returns true when the features OR graph_edges table contains at
// least one row, indicating the read-index has been populated from canonical
// HTML. Both being zero is the cold-clone condition.
func isIndexWarm(db *sql.DB) bool {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM features`).Scan(&n); err != nil {
		return false
	}
	if n > 0 {
		return true
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM graph_edges`).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// runFullSyncReindex runs the full HTML→SQLite reindex for the project
// identified by wipnoteDir synchronously and in-process. It calls the same
// reindex primitives as `wipnote reindex --full` so there is no duplication of
// index-build logic (DRY constraint).
//
// The caller MUST close any open handle on the project DB before calling this
// function — it opens its own writable handle.
func runFullSyncReindex(wipnoteDir string) error {
	projectDir := filepath.Dir(wipnoteDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return fmt.Errorf("lazy reindex: resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return fmt.Errorf("lazy reindex: ensure db dir: %w", err)
	}

	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("lazy reindex: open database: %w", err)
	}
	dbClosed := false
	closeDB := func() {
		if !dbClosed {
			_ = database.Close()
			dbClosed = true
		}
	}
	defer closeDB()

	validIDs := make(map[string]bool)

	reindexTracks(database, wipnoteDir, projectDir, validIDs, false)
	for _, dir := range []string{"features", "bugs", "spikes"} {
		reindexFeatureDir(database, wipnoteDir, projectDir, dir, validIDs, false)
	}
	// Archived work items (compacted into .wipnote/archive/*.html ledgers) are
	// still canonical — index them too, or a cold rebuild (which runs on every
	// container restart in this repo) silently drops every archived item from
	// the read index. Mirrors the full-reindex ordering in runReindex.
	reindexWorkitemLedgerNodes(database, wipnoteDir, projectDir, validIDs, false)
	collectSessionIDs(database, validIDs)
	reindexEdges(database, wipnoteDir, validIDs)
	reindexWorkitemLedgerEdges(database, wipnoteDir, validIDs, false)
	fixImplementedInEdges(database)

	closeDB()

	// Plan edges are a secondary canonical source. Errors are non-fatal —
	// the primary work-item lineage works without them.
	rdb, rdbErr := dbpkg.Open(dbPath)
	if rdbErr == nil {
		reindexPlanEdges(rdb, wipnoteDir)
		rdb.Close()
	}

	return nil
}
