package hooks

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

// stubRouteSQLAsync overrides the package-level daemon enqueue seam for the
// duration of a test and restores it on cleanup. ret is what the stub returns;
// the returned *bool reports whether the stub was actually invoked (so a test
// can prove RouteHookWrite tried the daemon path first).
func stubRouteSQLAsync(t *testing.T, ret bool) *bool {
	t.Helper()
	called := false
	prev := routeSQLAsync
	routeSQLAsync = func(projectRoot, sqlStmt string, args ...any) bool {
		called = true
		return ret
	}
	t.Cleanup(func() { routeSQLAsync = prev })
	return &called
}

// fileExists reports whether path exists, without creating it.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestRouteHookWrite_DaemonPath asserts that when the enqueue-only daemon route
// succeeds, RouteHookWrite returns success and opens NO direct writable handle.
// Proof of "no direct open": WIPNOTE_DB_PATH points at a path that does not yet
// exist; the bounded fallback (OpenHookDBWithBusyTimeout) would create+migrate
// that file, so its continued ABSENCE after the call means the direct path was
// never taken.
func TestRouteHookWrite_DaemonPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "should-not-be-created.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	called := stubRouteSQLAsync(t, true) // daemon enqueue succeeds

	ok := RouteHookWrite("posttooluse", dir, "sess-daemon",
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"s1", "agent-1", "active")

	if !ok {
		t.Fatal("RouteHookWrite returned false on a successful daemon enqueue")
	}
	if !*called {
		t.Fatal("RouteHookWrite did not attempt the daemon enqueue path first")
	}
	// The direct fallback would have created this file; its absence proves no
	// direct writable handle was opened.
	if fileExists(dbPath) {
		t.Fatalf("direct DB handle was opened (%s exists) despite successful daemon enqueue", dbPath)
	}
}

// TestRouteHookWrite_FallbackBounded asserts that when the daemon enqueue fails
// AND an external connection holds a write lock, RouteHookWrite falls back to
// the bounded direct path and returns in WELL under 1s (the SessionStartBusyTimeout
// bound), without surfacing an error. The pre-migrated DB makes the reopen warm
// (zero DDL), so the only contention is the held RESERVED write lock.
func TestRouteHookWrite_FallbackBounded(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1") // belt-and-suspenders: no real daemon
	stubRouteSQLAsync(t, false)             // force the fallback path

	// Pre-create + migrate so RouteHookWrite's reopen is a warm fast path
	// (no migration DDL competing for the lock).
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	seed.Close()

	// Hold a RESERVED write lock on a SEPARATE connection for the whole call.
	locker, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("locker open: %v", err)
	}
	defer locker.Close()
	tx, err := locker.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// A write inside the tx upgrades to RESERVED, taking the write lock that
	// will make RouteHookWrite's Exec wait on its busy_timeout then degrade.
	if _, err := tx.Exec(`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"holder", "agent-lock", "active"); err != nil {
		t.Fatalf("acquire write lock: %v", err)
	}
	defer tx.Rollback()

	start := time.Now()
	ok := RouteHookWrite("sessionstart", dir, "sess-bounded",
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"contended", "agent-1", "active")
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("RouteHookWrite returned false under a held write lock (must always succeed)")
	}
	if elapsed >= time.Second {
		t.Fatalf("RouteHookWrite blocked %v under a held write lock; bound is <1s (SessionStartBusyTimeout)", elapsed)
	}
}

// TestRouteHookWrite_AllFail_StillSucceeds asserts the canonical-first
// contract: when BOTH the daemon enqueue fails AND the bounded direct open
// fails, RouteHookWrite still returns success (never errors, never blocks).
// The open is forced to fail by pointing WIPNOTE_DB_PATH at a path whose parent
// is a regular FILE, so MkdirAll/open cannot succeed (ENOTDIR).
func TestRouteHookWrite_AllFail_StillSucceeds(t *testing.T) {
	dir := t.TempDir()
	// Create a regular file, then demand a DB path *inside* it — MkdirAll on
	// "<file>/sub" fails with ENOTDIR, so the direct open cannot succeed.
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("create blocker file: %v", err)
	}
	unopenable := filepath.Join(blocker, "sub", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", unopenable)
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1")
	stubRouteSQLAsync(t, false) // daemon enqueue fails

	start := time.Now()
	ok := RouteHookWrite("posttooluse", dir, "sess-allfail",
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"x", "agent-1", "active")
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("RouteHookWrite returned false when both daemon AND direct open failed (canonical-first requires success)")
	}
	if elapsed >= time.Second {
		t.Fatalf("RouteHookWrite blocked %v on the all-fail path; must degrade fast", elapsed)
	}
}
