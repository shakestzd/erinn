// Package indexer provides a polling NDJSON-to-SQLite indexer that tails
// per-session events.ndjson files and applies each signal line to SQLite
// via the existing Writer (through the SQLiteSink from S1).
//
// # .index-offset is a progress marker, not a resume point
//
// The indexer's own read position lives in memory (Indexer.offsets) and always
// starts at zero, because its destination — a process-local projection — always
// starts empty. The `.index-offset` file this package writes is therefore no
// longer read back by the indexer at all. It survives for OTHER processes, which
// use it to answer "has the telemetry for this session been consumed yet?"
// before doing something destructive or latency-sensitive:
//
//   - observe/otel/retention.indexerCaughtUp gates archiving and the NDJSON
//     sweep on it, so retention never removes a session's raw log first;
//   - cmd/wipnote/session_prune_archive.go gates pruning on the same condition;
//   - core/hooks/session_end.go:waitForIndexerCatchUp blocks (bounded at 2s) on
//     it before materialising a session.
//
// Those three are the only reason the file still exists. Deleting it outright
// without migrating them would make retention and prune fail closed — nothing
// would ever be archived, and NDJSON would grow unbounded — and would add a
// flat 2s to every session end. Whoever removes the last of those consumers
// should delete this file with it.
//
// One consequence of the in-memory read position: the marker is NOT monotonic
// across process restarts. A new indexer replays from zero and republishes the
// offset as it climbs, so a marker that read end-of-file can drop and rise
// again. Every consumer above compares offset against current file size and
// treats "behind" as "wait", so a replay window defers destructive work instead
// of authorising it — the safe direction, and the reason none of them needed
// changing.
package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readProgress reads the byte offset recorded in path.
// Returns 0 (and no error) when the file is missing, empty, or corrupt.
// Only returns an error for unexpected I/O failures.
//
// The indexer does not call this — see the package comment. It is the inverse
// of publishProgress, kept so the marker's contract is expressed in one place
// and testable from one place.
func readProgress(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read progress marker %s: %w", path, err)
	}

	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, nil
	}

	offset, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// Corrupted marker: report "nothing consumed". Every consumer treats
		// that as "not caught up", which is the safe direction — it defers
		// archiving and pruning rather than authorising them.
		return 0, nil
	}
	return offset, nil
}

// publishProgress records offset at path atomically via write-to-temp-then-rename,
// announcing to other processes how much of the session's NDJSON has been
// consumed. The temporary file is placed in the same directory so the rename is
// atomic and a reader never observes a half-written number.
func publishProgress(path string, offset int64) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".index-offset-tmp-")
	if err != nil {
		return fmt.Errorf("create temp progress marker: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := fmt.Fprintf(tmp, "%d", offset); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp progress marker: %w", err)
	}
	// os.CreateTemp lands at 0600. Every other file wipnote hand-writes under
	// .wipnote/ is 0644, and this one is read by other processes, so match them
	// rather than leaving one owner-only outlier in the tree.
	if err := tmp.Chmod(markerMode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp progress marker: %w", err)
	}
	// fsync the contents before the rename, and the directory after it. A
	// rename is atomic with respect to other processes but not with respect to
	// a crash: without these the marker can survive pointing at an offset whose
	// bytes were never flushed. It gates retention's decision to delete raw
	// telemetry, so a too-high marker after a crash is the expensive direction.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp progress marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp progress marker: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename progress marker: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// markerMode matches the 0644 every other hand-written .wipnote file uses.
const markerMode = 0o644
