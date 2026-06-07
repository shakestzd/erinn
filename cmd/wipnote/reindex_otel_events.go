package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/otel/indexer"
	otelreceiver "github.com/shakestzd/wipnote/internal/otel/receiver"
	otelsqlite "github.com/shakestzd/wipnote/internal/otel/sink/sqlite"
)

// reindexOtelEvents replays every .wipnote/sessions/<id>/events.ndjson file
// into the otel_signals table. It exists so deleting the SQLite cache and
// running `wipnote reindex` can fully rebuild the dashboard's OTel-derived
// event surface from canonical NDJSON.
//
// IMPLEMENTATION: rather than duplicating the indexer's parse + write logic
// here, this function:
//
//  1. Resets every per-session checkpoint file (.index-offset) to 0 so the
//     indexer treats each NDJSON file as new on its next pass.
//  2. Opens the sqlite Writer directly and wraps it as a SignalSink.
//  3. Constructs an indexer instance attached to that sink and a read-only
//     DB handle for prompt_id bridging.
//  4. Calls Indexer.runOnce in a loop until every session is fully drained.
//     The 4 MiB per-tick cap (bug-faf8e395) means very large files require
//     several iterations; the loop terminates when no progress is made.
//
// IDEMPOTENCY: sqlite.Writer.WriteBatch uses INSERT OR IGNORE keyed on
// signal_id, so resetting the checkpoints and replaying is safe even when
// otel_signals already contains rows.
//
// dbPath is the canonical SQLite path. wipnoteDir is .wipnote/. The function
// owns its own DB handle (no shared *sql.DB needs to be passed in) so the
// caller can reindex OTel signals after closing the main reindex pool.
//
// Returns (sessions processed, indexer-loop iterations, errors).
func reindexOtelEvents(dbPath, wipnoteDir string) (int, int, int) {
	sessionsDir := filepath.Join(wipnoteDir, "sessions")

	// Reset every checkpoint so the indexer treats each session as fresh.
	sessions, err := resetOtelCheckpoints(sessionsDir)
	if err != nil {
		log.Printf("reindex otel: reset checkpoints: %v", err)
		return 0, 0, 1
	}
	if len(sessions) == 0 {
		return 0, 0, 0
	}

	writer, werr := otelreceiver.NewWriter(dbPath)
	if werr != nil {
		log.Printf("reindex otel: open writer: %v", werr)
		return 0, 0, 1
	}
	defer writer.Close()
	sink := otelsqlite.New(writer)

	// Bridge handle: the indexer uses *sql.DB for two reads — orphan
	// filtering (filterSessionsByDB) and prompt_id bridging
	// (maybeSetPromptID). We give it the same writable handle the
	// sqlite Writer is bound to: this avoids the dual-writer contention
	// pattern slice 6 is designed to prevent (a second dbpkg.Open here
	// would acquire a separate writable handle on the same DB file). The
	// indexer never writes through this handle, so sharing it is safe.
	//
	// Open through dbpkg.Open (not OpenReadOnly) because the bridge does
	// use SetPromptID which issues an UPDATE on agent_events. The bridge
	// handle is the bridge's writer; the sqlite.Writer is the OTel
	// signals writer. Both operate on disjoint tables, so they do not
	// race for the same rows.
	bridgeDB, bridgeErr := dbpkg.Open(dbPath)
	if bridgeErr != nil {
		log.Printf("reindex otel: open bridge DB: %v", bridgeErr)
		bridgeDB = nil
	} else {
		defer bridgeDB.Close()
	}

	idxr := indexer.New(wipnoteDir, sink)
	if bridgeDB != nil {
		idxr = idxr.WithDB(bridgeDB)
	}

	// Drain in a bounded loop. Each runOnce reads at most 4 MiB per session;
	// large files need several iterations. We stop when no new bytes are
	// reported across two consecutive passes for every session, capped at
	// maxIterations to guarantee termination on pathological inputs.
	const maxIterations = 256
	ctx := context.Background()
	iterations := 0
	stableTicks := 0
	prevLag := indexerLagSum(idxr)
	for iterations < maxIterations {
		idxr.RunOnce(ctx)
		iterations++
		curLag := indexerLagSum(idxr)
		if curLag == prevLag {
			stableTicks++
			// Two consecutive identical lag reads → drained.
			if stableTicks >= 2 {
				break
			}
		} else {
			stableTicks = 0
		}
		prevLag = curLag
		// Yield briefly so the kernel can flush rename'd checkpoints to disk.
		time.Sleep(time.Millisecond)
	}

	return len(sessions), iterations, 0
}

// resetOtelCheckpoints walks every session directory in sessionsDir and
// removes .index-offset files so the next indexer run starts from byte 0.
// Returns the list of session IDs that had an events.ndjson file (i.e. the
// candidates the indexer will iterate).
func resetOtelCheckpoints(sessionsDir string) ([]string, error) {
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessDir := filepath.Join(sessionsDir, e.Name())
		if _, err := os.Stat(filepath.Join(sessDir, "events.ndjson")); err != nil {
			continue
		}
		_ = os.Remove(filepath.Join(sessDir, ".index-offset"))
		sids = append(sids, e.Name())
	}
	return sids, nil
}

// indexerLagSum returns the total LagBytes across every session known to
// the indexer's Status snapshot. It is used by the reindex drain loop to
// decide when every NDJSON file has been fully processed.
func indexerLagSum(idxr *indexer.Indexer) int64 {
	var total int64
	for _, fi := range idxr.Status() {
		total += fi.LagBytes
	}
	return total
}

