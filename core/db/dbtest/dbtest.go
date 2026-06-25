// Package dbtest provides test-only helpers for opening wipnote's SQLite
// database in tests, without the on-disk fsync cost that file-backed databases
// incur on overlay/virtiofs/FUSE filesystems (the dominant cost in the
// cmd/wipnote suite running under a devcontainer overlayfs).
//
// This is a DEDICATED test-utility package. Importing the standard "testing"
// package from a non-_test.go file is intentional and follows the same pattern
// as the standard library's net/http/httptest: the helper must be importable by
// OTHER packages' test binaries (e.g. cmd/wipnote), which a _test.go-defined
// helper cannot be. Production packages (core/db itself) MUST NOT import this
// package or "testing"; only test binaries link it.
package dbtest

import (
	"database/sql"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// OpenForTest returns a fully-migrated, in-memory wipnote SQLite database
// suitable for use in tests. The returned *sql.DB is automatically closed when
// the test (and any registered cleanups) complete via t.Cleanup, so callers do
// not manage the handle's lifecycle.
//
// In-memory semantics (the whole point of this helper):
//
//   - The DSN is the literal ":memory:". modernc.org/sqlite routes a DSN
//     containing the substring ":memory:" to a transient in-memory database, and
//     dbpkg.Open's in-memory detection matches the same literal substring, so the
//     file-backed code path (directory creation, file pragmas, WAL/DELETE
//     journal selection) is skipped. Do NOT substitute a file:...?mode=memory
//     URI: dbpkg.Open would misroute it through the file path.
//
//   - SetMaxOpenConns(1) + SetConnMaxLifetime(0) pin the handle to exactly ONE
//     physical connection that never recycles. This is REQUIRED for in-memory
//     correctness: each new connection to ":memory:" gets its OWN private,
//     empty database. Allowing the pool to open a second connection (or recycle
//     the first) would expose an unmigrated empty database to some queries and
//     drop the migrated one. With a single permanent connection, migrations,
//     writes, and reads all observe one consistent in-memory database.
//
// Migrations run through the real dbpkg.Open machinery (CreateAllTables +
// CreateAllIndexes + every registered migration step), so the schema returned
// here is identical to a production database at currentSchemaVersion. We never
// reimplement migrations.
//
// Each call returns an isolated database; two OpenForTest handles never share
// rows.
//
// LIMITATION — single connection means NO nested concurrent queries:
//
//	SetMaxOpenConns(1) makes this helper UNSAFE for any code path that needs a
//	SECOND connection while the first is still checked out. The canonical trap
//	is issuing a query while an outer *sql.Rows is still open (database/sql
//	checks out a fresh connection for the inner query). With only one permitted
//	connection the inner query blocks forever waiting for the outer to release
//	it — a DEADLOCK, not an error. This is exactly what forced the slice-2
//	revert (commit 47fcd4c90): the api_*_test.go DBs were migrated to this
//	helper, then deadlocked buildEventTree, which queries inside a rows.Next()
//	loop. For those nested-query paths use a shared-cache in-memory DSN instead
//	— dbpkg.Open("file::memory:?cache=shared") permits a second connection
//	because every connection shares ONE in-memory DB (see openGraphTestDB /
//	openTreeTestDB in cmd/wipnote). Reach for OpenForTest only when the code
//	under test issues strictly sequential queries (no overlapping result sets).
func OpenForTest(t testing.TB) *sql.DB {
	t.Helper()

	// dbpkg.Open opens at most one physical connection internally (ApplyPragmas
	// pins one dedicated connection, releases it back to the idle pool, and
	// runMigrations reuses that idle connection). Constraining the pool to a
	// single connection immediately after Open ensures the one connection that
	// holds the migrated :memory: database is the only connection that will ever
	// be used — no second empty in-memory database can be created, and the
	// migrated one is never recycled away.
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("dbtest.OpenForTest: open in-memory database: %v", err)
	}
	database.SetMaxOpenConns(1)
	database.SetConnMaxLifetime(0)

	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("dbtest.OpenForTest: closing in-memory database: %v", cerr)
		}
	})

	return database
}
