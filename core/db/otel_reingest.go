package db

import (
	"database/sql"
	"fmt"
)

// OtelReingestMetadataKey is the metadata key that marks the derived OTel
// index as needing a full replay from the canonical NDJSON shards.
//
// A schema migration can repair the shape of otel_signals, but it cannot
// recover rows the old shape discarded: the NDJSON indexer keeps a byte offset
// per shard, and on any established install those offsets already sit at
// end-of-file, so the indexer would never re-read the lines whose rows are
// missing. The marker bridges that gap — the migration sets it, and the
// component that owns the shard directory (indexer.EnsureReingest) consumes it
// by clearing the checkpoints exactly once.
//
// It lives in the database rather than on disk on purpose. The database is the
// thing whose contents are suspect, so a marker stored beside it survives
// exactly as long as the data it describes: delete the derived cache and a
// fresh database re-runs the migration and re-arms the marker, which is the
// correct outcome, whereas a file in the project tree would claim the replay
// had already happened.
const OtelReingestMetadataKey = "otel_reingest_required"

// SetOtelReingestRequired arms the re-ingest marker. reason identifies what
// armed it (a migration step name) and is carried through to the log line the
// consumer emits, so an unexpected full replay is traceable to its cause.
func SetOtelReingestRequired(db *sql.DB, reason string) error {
	return SetMetadata(db, OtelReingestMetadataKey, reason)
}

// OtelReingestPending reports whether a re-ingest is armed, and the reason it
// was armed with. It does not clear anything.
//
// Reading and clearing are separate calls on purpose. The consumer must do the
// work first and clear second: if it cleared on read and then died before
// finishing, the marker would be gone and the replay would never happen — the
// recovery would be silently skipped, which is the same failure mode as the
// defect it exists to repair. Doing the work first means a crash leaves the
// marker armed and the next start simply tries again, at worst repeating an
// idempotent replay.
func OtelReingestPending(db *sql.DB) (bool, string, error) {
	reason, err := GetMetadata(db, OtelReingestMetadataKey)
	if err != nil {
		return false, "", err
	}
	return reason != "", reason, nil
}

// ClearOtelReingestRequired disarms the marker. Call it only after the replay
// it requested has actually been set up.
func ClearOtelReingestRequired(db *sql.DB) error {
	if _, err := db.Exec(`DELETE FROM metadata WHERE key = ?`, OtelReingestMetadataKey); err != nil {
		return fmt.Errorf("clear %s: %w", OtelReingestMetadataKey, err)
	}
	return nil
}
