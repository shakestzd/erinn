package indexer

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// EnsureReingest clears every per-shard checkpoint when the database carries a
// pending re-ingest marker, so the next indexer passes replay the canonical
// NDJSON from byte zero.
//
// This exists because a schema fix and a data fix are different things. When a
// defect in the write path drops rows, repairing the schema stops the loss but
// recovers nothing: `.index-offset` already points at end-of-file for every
// shard, so the indexer skips exactly the lines whose rows are missing. Every
// established install would stay holed while a fresh one looked correct — the
// failure mode that makes this class of bug look fixed when it is not.
//
// Returns the number of checkpoints cleared. Zero with a nil error is the
// normal case: no marker, nothing to do.
//
// Safe to call concurrently with an in-flight indexer pass and safe to call
// from more than one process. Clearing a checkpoint can only cause lines to be
// re-read, and re-reading is idempotent — the writer keys inserts on
// signal_id, so replayed rows collapse onto the rows already stored.
func EnsureReingest(wipnoteDir string, database *sql.DB) (int, error) {
	if database == nil {
		return 0, nil
	}
	pending, reason, err := dbpkg.OtelReingestPending(database)
	if err != nil {
		return 0, fmt.Errorf("check otel re-ingest marker: %w", err)
	}
	if !pending {
		return 0, nil
	}
	sessionsDir := filepath.Join(wipnoteDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		// No shards to replay. Disarm rather than leave the marker set
		// forever, re-announcing a rebuild that has nothing to rebuild on
		// every process start.
		return 0, dbpkg.ClearOtelReingestRequired(database)
	}
	if err != nil {
		return 0, fmt.Errorf("read sessions dir: %w", err)
	}

	// Name the cause: a full replay is expensive and otherwise looks like the
	// indexer spontaneously deciding to redo everything.
	log.Printf("indexer: full NDJSON re-ingest requested by %s", reason)

	cleared := 0
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		checkpoint := filepath.Join(sessionsDir, e.Name(), ".index-offset")
		if err := os.Remove(checkpoint); err != nil {
			if !os.IsNotExist(err) && firstErr == nil {
				firstErr = fmt.Errorf("clear checkpoint %s: %w", checkpoint, err)
			}
			continue
		}
		cleared++
	}
	if firstErr != nil {
		// Leave the marker armed: the replay is incomplete, and the next
		// start should try the rest rather than assume it happened.
		return cleared, firstErr
	}

	// Checkpoints are gone, so the replay is now guaranteed to occur on this
	// pass and every later one until it finishes. Only now is it safe to
	// disarm.
	if err := dbpkg.ClearOtelReingestRequired(database); err != nil {
		return cleared, err
	}
	return cleared, nil
}
