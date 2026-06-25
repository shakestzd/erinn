package dbtest

import (
	"testing"
)

// TestOpenForTest asserts the helper returns a fully-migrated in-memory SQLite
// database constrained to a single connection, with cleanup registered.
func TestOpenForTest(t *testing.T) {
	database := OpenForTest(t)

	// 1. Migrated schema is present — write+read a known table from the
	//    registry. A round-trip against `features` succeeds only if
	//    CreateAllTables ran (i.e. migrations applied).
	if _, err := database.Exec(`INSERT INTO features (id, type, title) VALUES ('feat-x', 'feature', 't')`); err != nil {
		t.Fatalf("insert into migrated features table: %v", err)
	}
	var got string
	if err := database.QueryRow(`SELECT title FROM features WHERE id = 'feat-x'`).Scan(&got); err != nil {
		t.Fatalf("read back from features: %v", err)
	}
	if got != "t" {
		t.Fatalf("features title = %q, want %q", got, "t")
	}

	// 2. user_version reflects a fully-migrated DB (non-zero). A brand-new
	//    in-memory DB with no migrations would report 0.
	var userVersion int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if userVersion == 0 {
		t.Fatalf("user_version = 0, expected migrated (non-zero) schema")
	}

	// 3. Exactly one connection backs the handle. With SetMaxOpenConns(1) the
	//    single connection holds the one consistent :memory: database.
	if oc := database.Stats().MaxOpenConnections; oc != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", oc)
	}
	if oc := database.Stats().OpenConnections; oc > 1 {
		t.Fatalf("OpenConnections = %d, want <= 1", oc)
	}

	// 4. Isolation per call — a second handle does not see the first's writes.
	other := OpenForTest(t)
	var n int
	if err := other.QueryRow(`SELECT COUNT(*) FROM features`).Scan(&n); err != nil {
		t.Fatalf("count features on isolated handle: %v", err)
	}
	if n != 0 {
		t.Fatalf("second handle saw %d rows, want 0 (handles must be isolated)", n)
	}
}

// TestOpenForTestCleanupClosesHandle verifies t.Cleanup closes the handle so
// callers never leak an open in-memory DB.
func TestOpenForTestCleanupClosesHandle(t *testing.T) {
	// Use a subtest so its registered Cleanup fires (and the handle closes)
	// before we probe the handle from the parent test.
	var dbRef interface{ Ping() error }
	t.Run("inner", func(t *testing.T) {
		dbRef = OpenForTest(t)
		if err := dbRef.Ping(); err != nil {
			t.Fatalf("handle not usable inside subtest: %v", err)
		}
	})
	// After the subtest returns, its registered Cleanup has run and closed the DB.
	if err := dbRef.Ping(); err == nil {
		t.Fatal("expected Ping to fail after cleanup closed the handle")
	}
}
