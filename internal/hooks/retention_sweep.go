package hooks

import (
	"path/filepath"

	"github.com/shakestzd/wipnote/internal/otel/retention"
)

// runRetentionSweep performs a disk-retention pass for the project: rotate
// oversized logs and archive+prune raw events.ndjson for inactive,
// fully-ingested sessions. It is invoked fire-and-forget from session-start so
// disk reclamation happens at a natural lifecycle point even when `wipnote
// serve` is not running.
//
// The DB handle is intentionally nil: the ndjson coverage sweep relies on the
// per-session .index-offset checkpoint (durable-ingest proof), not the DB, and
// log rotation needs no DB at all. Skipping the DB open keeps the hot hook path
// cheap and avoids contending for the SQLite write lock. activeSessionID is the
// current session, which the sweep never prunes. All errors are swallowed —
// retention must never affect session start.
func runRetentionSweep(projectDir, activeSessionID string) {
	if projectDir == "" {
		return
	}
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	_, _ = retention.Sweep(nil, wipnoteDir, activeSessionID, false)
}
