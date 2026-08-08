package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/observe/otel/indexer"
	otelreceiver "github.com/shakestzd/wipnote/observe/otel/receiver"
	otelsqlite "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
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
//     indexer treats each NDJSON file as new on its next pass, and snapshots
//     each session's events.ndjson byte size at that same moment.
//  2. Opens the sqlite Writer directly and wraps it as a SignalSink.
//  3. Constructs an indexer instance attached to that sink and a read-only
//     DB handle for prompt_id bridging.
//  4. Calls Indexer.RunOnce in a loop until every session has been drained
//     up to its snapshotted size. The 4 MiB per-tick cap (bug-faf8e395)
//     means very large files require several iterations.
//
// BOUNDED DRAIN (bug-b2471635): the loop targets the byte size recorded in
// step 1, a fixed point in time, rather than "keep going until the system
// looks quiet." An earlier version compared LagBytes across two consecutive
// ticks and stopped once it stabilized — correct when nothing else is
// writing, but in a multi-agent session other live sessions keep appending
// to their OWN events.ndjson the whole time, so lag never stabilizes and
// the loop ran until its 256-iteration hard cap, measured at 9-42+ minutes
// depending on how much concurrent traffic happened to be running. Bytes
// appended to a file during the drain are simply left for the next reindex
// (or the live dashboard daemon's own independent poll loop, which is
// already the thing responsible for keeping up with an actively-growing
// file — see indexer.go's package doc) rather than chased indefinitely.
// This matches the indexer's own contract: RunOnce always re-stats the live
// file size and caps every tick regardless, so it was never designed to
// promise "caught up to right now," only "one bounded pass." Verified with
// the team before implementing that nothing treats a completed reindex as
// an atomic point-in-time snapshot of otel_signals: reindexOtelEvents has
// exactly one caller, nothing gates on its completion, and no code reads
// otel_signals immediately after a reindex expecting mid-run writes to be
// visible.
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

	// Reset every checkpoint so the indexer treats each session as fresh,
	// and record each session's size at this same moment as the drain target.
	sessions, snapshot, err := resetOtelCheckpoints(sessionsDir)
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

	// Drain in a bounded loop, targeting the byte-size snapshot taken in
	// resetOtelCheckpoints rather than the live (possibly still-growing)
	// file size. Each RunOnce reads at most 4 MiB per session; large files
	// need several iterations. maxIterations remains as a hard backstop for
	// pathological inputs (e.g. a session the indexer can never make
	// progress on for some other reason), but the snapshot target means the
	// common case terminates in exactly the number of ticks the data
	// actually requires, independent of concurrent writers.
	const maxIterations = 256
	ctx := context.Background()
	iterations := 0
	for iterations < maxIterations {
		idxr.RunOnce(ctx)
		iterations++
		status := idxr.Status()
		if iterations == 1 {
			// Drop any snapshotted session the indexer never picked up at
			// all (e.g. an orphan directory with no corresponding sessions
			// row, filtered out by discoverSessions) -- it will never gain
			// a status entry no matter how many ticks run, and isn't part
			// of the indexer's actual work set.
			for sid := range snapshot {
				if _, ok := status[sid]; !ok {
					delete(snapshot, sid)
				}
			}
		}
		if drainedToSnapshot(status, snapshot) {
			break
		}
		// Yield briefly so the kernel can flush rename'd checkpoints to disk.
		time.Sleep(time.Millisecond)
	}

	return len(sessions), iterations, 0
}

// drainedToSnapshot reports whether every session in snapshot has been
// processed at least up to its snapshotted byte size, per the indexer's
// current status. FileInfo.LastOffset reflects the durable checkpoint
// position after the most recent tick (see indexer.go's updateSize /
// recordSuccess), so this is a point-in-time comparison, not a live one.
func drainedToSnapshot(status map[string]indexer.FileInfo, snapshot map[string]int64) bool {
	for sid, want := range snapshot {
		fi, ok := status[sid]
		if !ok || fi.LastOffset < want {
			return false
		}
	}
	return true
}

// resetOtelCheckpoints walks every session directory in sessionsDir and
// removes .index-offset files so the next indexer run starts from byte 0.
// It also snapshots each session's events.ndjson size at this same moment
// (bug-b2471635): the caller's drain loop targets this fixed point rather
// than the live file size, which can keep growing for the whole duration of
// a reindex in a session with other concurrent agent activity.
// Returns the list of session IDs that had an events.ndjson file (i.e. the
// candidates the indexer will iterate) and each one's size at that instant.
func resetOtelCheckpoints(sessionsDir string) ([]string, map[string]int64, error) {
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var sids []string
	snapshot := make(map[string]int64)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessDir := filepath.Join(sessionsDir, e.Name())
		info, statErr := os.Stat(filepath.Join(sessDir, "events.ndjson"))
		if statErr != nil {
			continue
		}
		_ = os.Remove(filepath.Join(sessDir, ".index-offset"))
		sids = append(sids, e.Name())
		snapshot[e.Name()] = info.Size()
	}
	return sids, snapshot, nil
}

