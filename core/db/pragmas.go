// Package db provides SQLite database operations for wipnote.
//
// Uses modernc.org/sqlite (pure Go, no CGo) for maximum portability.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"maps"
	"strings"
)

// Pragmas mirrors the Python PRAGMA_SETTINGS from pragmas.py.
// journal_mode is intentionally omitted here; BuildPragmas sets it based on
// filesystem type detection (WAL on safe native fs, DELETE on unsafe/unknown).
var Pragmas = map[string]string{
	"journal_mode": "WAL",
	"synchronous":  "NORMAL",
	"foreign_keys": "1",
	"busy_timeout": "5000",
	"cache_size":   "-64000",
	"temp_store":   "MEMORY",
	"mmap_size":    "0",
}

// BuildPragmas returns a pragma map with journal_mode resolved for the given
// database path. On filesystems where WAL-mode mmap is unsafe (virtiofs, FUSE,
// 9p, overlayfs, NFS, SMB), journal_mode is set to DELETE to avoid SIGBUS
// crashes. On safelisted native filesystems it stays WAL.
func BuildPragmas(dbPath string) map[string]string {
	p := make(map[string]string, len(Pragmas))
	maps.Copy(p, Pragmas)
	if isUnsafeForMmap(dbPath) {
		p["journal_mode"] = "DELETE"
	}
	return p
}

// ApplyPragmas sets all performance PRAGMAs on a database connection.
//
// All PRAGMAs are executed on a single dedicated *sql.Conn (pinned via
// db.Conn) to prevent the connection-pool race where busy_timeout is set on
// connection A but journal_mode (or another required PRAGMA) runs on
// connection B, which has busy_timeout=0 and immediately returns SQLITE_BUSY
// if the write lock is contended.
//
// Some PRAGMAs (busy_timeout, cache_size) are best-effort and may not apply
// to all backing stores (e.g., in-memory SQLite); failures are logged at debug
// level and don't block the Open. Other PRAGMAs are required.
func ApplyPragmas(db *sql.DB, pragmas map[string]string) error {
	ctx := context.Background()

	// Acquire a dedicated connection so every PRAGMA below runs on the same
	// underlying SQLite connection. This is critical: busy_timeout is
	// per-connection state, and journal_mode=WAL must run on the same
	// connection that already has busy_timeout set — otherwise a fresh pooled
	// connection will see busy_timeout=0 and return SQLITE_BUSY immediately
	// when the database is locked.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring dedicated connection for pragmas: %w", err)
	}
	defer conn.Close()

	// Must run before journal_mode: if the write lock is held, busy_timeout
	// tells SQLite how long to wait before returning SQLITE_BUSY.
	if bt, ok := pragmas["busy_timeout"]; ok {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %s", bt)); err != nil {
			log.Printf("debug: skipping PRAGMA busy_timeout (not supported on this backing): %v", err)
		}
	}

	// PRAGMAs that are REQUIRED — fail Open if these don't apply.
	required := []string{"journal_mode", "synchronous", "foreign_keys", "temp_store", "mmap_size"}
	// PRAGMAs that are best-effort — failure is logged at debug level
	// and doesn't block Open (some drivers/backing stores reject these).
	// busy_timeout is handled above (must precede journal_mode), so omit it here.
	optional := []string{"cache_size"}

	for _, pragma := range required {
		value, ok := pragmas[pragma]
		if !ok {
			continue
		}
		// Special case: journal_mode is case-insensitive and may already be set.
		// Querying first avoids lock escalation from SHARED→EXCLUSIVE when upgrading
		// DEFERRED transactions, which returns SQLITE_BUSY immediately on contention.
		if strings.EqualFold(pragma, "journal_mode") {
			var current string
			if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&current); err == nil {
				if strings.EqualFold(current, value) {
					continue // already in desired mode — skip the lock-acquiring SET
				}
			}
			// fall through to the SET below if query failed or modes differ
		}
		// bug-dd5db2d1 (supersedes bug-56b686aa): the required-pragma SETs run
		// at EVERY db.Open, BEFORE runMigrations. busy_timeout (applied above)
		// only covers SQLite's internal lock-wait — it does NOT cover the
		// SHARED→RESERVED upgrade race, so under multi-session DELETE-journal
		// saturation `PRAGMA journal_mode = DELETE` (a header/rollback-journal
		// write) can return SQLITE_BUSY immediately and bypass busy_timeout
		// entirely. bug-56b686aa's busy_timeout-first ordering + query-before-set
		// fast path do NOT close this race (the fast path only skips the SET
		// when the mode already matches; on a contended DB that still needs the
		// SET, the unprotected ExecContext was the failure surface). Wrap the
		// write in the SHARED RetryOnBusy/DefaultBusyBackoff helper (busy_retry.go,
		// same package) so the transient BUSY is absorbed transparently for
		// EVERY caller (CLI, dashboard, hooks, gate, completion, reindex,
		// migrate) instead of each hand-rolling its own loop. PRAGMA writes are
		// fast and idempotent, so re-running an attempt is safe; RetryOnBusy
		// returns immediately on success or any non-BUSY error, so the only
		// added cost is bounded backoff latency on genuine transient contention.
		//
		// journal_mode is a result-returning pragma (it echoes the mode). Drive
		// it via QueryContext and drain+close the row set on EVERY attempt so a
		// retried attempt does not leak the rows handle from the prior attempt.
		setPragma := pragma
		setValue := value
		if err := RetryOnBusy(DefaultBusyBackoff, func() error {
			rows, qerr := conn.QueryContext(ctx, fmt.Sprintf("PRAGMA %s = %s", setPragma, setValue))
			if qerr != nil {
				return qerr
			}
			// Drain so the statement fully executes and the connection is
			// returned to a clean state before the next attempt / pragma.
			for rows.Next() {
			}
			cerr := rows.Err()
			if closeErr := rows.Close(); closeErr != nil && cerr == nil {
				cerr = closeErr
			}
			return cerr
		}); err != nil {
			return fmt.Errorf("applying PRAGMA %s: %w", setPragma, err)
		}
	}

	for _, pragma := range optional {
		value, ok := pragmas[pragma]
		if !ok {
			continue
		}
		_, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA %s = %s", pragma, value))
		if err != nil {
			// Best-effort: log at debug, continue. In-memory DBs in tests
			// may reject busy_timeout / cache_size; that's fine because
			// they aren't subject to the contention these PRAGMAs protect
			// against.
			log.Printf("debug: skipping PRAGMA %s (not supported on this backing): %v", pragma, err)
		}
	}
	return nil
}

// RunOptimize executes PRAGMA optimize for planner/statistics upkeep.
func RunOptimize(db *sql.DB) error {
	_, err := db.Exec("PRAGMA optimize")
	return err
}

// CheckIntegrity runs integrity_check and foreign_key_check.
// Returns true if the database passes both checks.
func CheckIntegrity(db *sql.DB) (bool, error) {
	row := db.QueryRow("PRAGMA integrity_check")
	var result string
	if err := row.Scan(&result); err != nil {
		return false, fmt.Errorf("integrity_check: %w", err)
	}
	if result != "ok" {
		return false, nil
	}

	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return false, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		// Any row means a violation exists.
		return false, nil
	}
	return true, rows.Err()
}

// QueryJournalMode returns the effective journal_mode of the database
// connection. Returns "unknown" if the query fails.
func QueryJournalMode(db *sql.DB) string {
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		return "unknown"
	}
	return mode
}
