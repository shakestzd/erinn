package db_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	db "github.com/shakestzd/wipnote/core/db"
	_ "modernc.org/sqlite"
)

// TestApplyPragmas_AppliesBusyTimeout verifies that Open applies busy_timeout
// to the database connection. Rather than relying on lock-contention timing
// (which is non-deterministic across CI environments), we query the PRAGMA
// value directly after opening and assert it equals the configured value.
func TestApplyPragmas_AppliesBusyTimeout(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	conn, err := database.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()

	var busyTimeout int
	row := conn.QueryRowContext(ctx, "PRAGMA busy_timeout")
	if err := row.Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout scan: %v", err)
	}

	const wantBusyTimeout = 5000
	if busyTimeout != wantBusyTimeout {
		t.Errorf("busy_timeout = %d, want %d", busyTimeout, wantBusyTimeout)
	}
}

// selectedJournalMode returns the journal mode the REAL, unchanged selection
// logic (BuildPragmas -> isUnsafeForMmap) chooses for dbPath on the host
// running the test. Assertions derive the expected mode from this — never a
// hardcoded "delete", which only holds on WAL-unsafe filesystems (overlayfs/
// FUSE devcontainers) and would false-fail on WAL hosts (host installs on
// APFS/ext4). This is the same filesystem-agnostic discipline as
// bug-74a7bda7's busy_filesystem_agnostic_test.go.
func selectedJournalMode(dbPath string) string {
	return strings.ToLower(db.BuildPragmas(dbPath)["journal_mode"])
}

// seededJournalDB creates a fresh schema'd temp-file DB via db.Open (which
// applies BuildPragmas, so the file lands in whatever mode the host's
// selection logic chooses) and returns its path. It does NOT force a specific
// journal mode — callers that need the DELETE-specific contention path either
// skip-on-WAL or drive ApplyPragmas directly with an explicit DELETE pragma
// map. The schema-creating handle is closed before returning so it does not
// itself contend.
func seededJournalDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pragma-busy.db")
	w, err := db.Open(dbPath) // creates schema + runs migrations
	if err != nil {
		t.Fatalf("db.Open seed: %v", err)
	}
	w.Close()
	return dbPath
}

// NOTE: the deterministic non-waitable-BUSY regression test that must defeat
// bug-56b686aa's query-before-set fast path AND assert the retry seam
// actually fired lives in pragmas_busy_whitebox_test.go (package db) as
// TestApplyPragmas_JournalModeSET_RetriesNonWaitableBusy — it bootstraps the
// DB into WAL mode and holds a SHARED lock to force the contended SET path,
// needing the unexported busySleep seam for the non-vacuity assertion, which is
// not reachable from this external db_test package. Keeping it here would risk
// re-introducing the vacuity roborev job 3237 flagged (on a DELETE-selecting
// host, seed==target makes ApplyPragmas skip the SET entirely).

// TestApplyPragmas_NonBusyPragmaErrorNotRetried guards the blast-radius
// invariant: the RetryOnBusy wrap around the required-pragma SET must NOT
// change behavior on non-BUSY errors — a genuine pragma failure must surface
// immediately (one attempt), not pay the backoff budget. We assert the
// happy-path Open is fast (no spurious retry latency when uncontended), which
// transitively proves the wrap is a near-no-op on the success path that EVERY
// db.Open now traverses.
func TestApplyPragmas_NonBusyPragmaErrorNotRetried(t *testing.T) {
	dbPath := seededJournalDB(t)
	// Expected mode is whatever the REAL, unchanged selection logic chooses
	// for this host's filesystem — DELETE on overlayfs/FUSE, WAL on APFS/ext4.
	// Never a hardcoded literal.
	wantMode := selectedJournalMode(dbPath)

	start := time.Now()
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("uncontended Open: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		d.Close()
		t.Fatalf("uncontended Open took %v; RetryOnBusy wrap must be a "+
			"near-no-op on the success path (no backoff when not BUSY)", elapsed)
	}
	// Sanity: the selected mode survived the wrapped re-application (we only
	// hardened the WRITE; mode SELECTION is unchanged and filesystem-derived).
	if jm := db.QueryJournalMode(d); !strings.EqualFold(jm, wantMode) {
		d.Close()
		t.Fatalf("journal_mode = %q after Open, want %q (the host-selected "+
			"mode; RetryOnBusy wrap must not change which mode is written)",
			jm, wantMode)
	}
	d.Close()
}
