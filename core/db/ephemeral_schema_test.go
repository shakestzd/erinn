package db

import (
	"database/sql"
	"testing"
)

// schemaObjects returns "type|name|sql" for every non-internal schema object.
func schemaObjects(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query(
		`SELECT type || '|' || name || '|' || COALESCE(sql, '')
		   FROM sqlite_master
		  WHERE name NOT LIKE 'sqlite_%'
		  ORDER BY 1`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// TestEphemeralSchemaMatchesMigrations is the guard on the DDL-snapshot
// shortcut in openEphemeralProjection: replaying the cached DDL must produce
// byte-identical schema to running the migration chain, and must leave
// user_version stamped so a later RunMigrations is a no-op.
//
// If this fails, a migration step has started doing something the snapshot
// cannot reproduce (seed rows, a conditional ALTER, an engine-version-dependent
// DDL rewrite). Fix the snapshot or drop back to openEphemeralViaMigrations —
// do not "fix" the test.
func TestEphemeralSchemaMatchesMigrations(t *testing.T) {
	migrated, err := openEphemeralViaMigrations()
	if err != nil {
		t.Fatalf("open via migrations: %v", err)
	}
	defer migrated.Close()

	cached, err := OpenEphemeralProjection()
	if err != nil {
		t.Fatalf("open via cached DDL: %v", err)
	}
	defer cached.Close()

	want := schemaObjects(t, migrated)
	got := schemaObjects(t, cached)

	if len(want) != len(got) {
		t.Fatalf("schema object count: migrated=%d cached=%d", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("schema object %d differs:\n migrated=%q\n   cached=%q", i, want[i], got[i])
		}
	}
	if len(want) == 0 {
		t.Fatal("no schema objects found — the comparison is vacuous")
	}

	wantVer, err := readUserVersion(migrated)
	if err != nil {
		t.Fatalf("read migrated user_version: %v", err)
	}
	gotVer, err := readUserVersion(cached)
	if err != nil {
		t.Fatalf("read cached user_version: %v", err)
	}
	if wantVer != gotVer {
		t.Fatalf("user_version: migrated=%d cached=%d", wantVer, gotVer)
	}
	if gotVer != currentSchemaVersion {
		t.Fatalf("user_version=%d, want currentSchemaVersion=%d", gotVer, currentSchemaVersion)
	}

	// A second migration pass over the cached-DDL handle must be a no-op.
	if err := RunMigrations(cached); err != nil {
		t.Fatalf("RunMigrations over cached-DDL handle: %v", err)
	}
	if after := schemaObjects(t, cached); len(after) != len(got) {
		t.Fatalf("RunMigrations mutated the cached-DDL schema: %d -> %d", len(got), len(after))
	}
}

// TestEphemeralTablesOnlyKeepsReadsWorking pins the contract that makes the
// tables-only handle safe for the hook read path: every table still exists, so
// reads return zero rows rather than "no such table".
func TestEphemeralTablesOnlyKeepsReadsWorking(t *testing.T) {
	full, err := OpenEphemeralProjection()
	if err != nil {
		t.Fatalf("open full: %v", err)
	}
	defer full.Close()

	lean, err := OpenEphemeralProjectionTablesOnly()
	if err != nil {
		t.Fatalf("open tables-only: %v", err)
	}
	defer lean.Close()

	rows, err := full.Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tables = append(tables, n)
	}
	if len(tables) == 0 {
		t.Fatal("no tables found — the comparison is vacuous")
	}

	for _, name := range tables {
		var n int
		if err := lean.QueryRow(`SELECT COUNT(*) FROM "` + name + `"`).Scan(&n); err != nil {
			t.Errorf("tables-only projection cannot read %s: %v", name, err)
			continue
		}
		if n != 0 {
			t.Errorf("tables-only projection: %s has %d rows, want 0", name, n)
		}
	}

	// And it really did skip the indexes — that is the whole point.
	var idx int
	if err := lean.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%'`).Scan(&idx); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if idx != 0 {
		t.Fatalf("tables-only projection created %d explicit indexes, want 0", idx)
	}
}

// TestEphemeralTablesOnlyRejectsWrites pins the ENFORCEMENT of the invariant
// that makes the index-less handle safe. Without query_only the invariant is
// only a comment: core/hooks.isReadOnlyDB reads PRAGMA query_only to decide
// whether a handle may be written through, so a handle that does not set it is
// classified writable and the hook write router will Exec against a schema
// missing the UNIQUE indexes that upserts depend on.
func TestEphemeralTablesOnlyRejectsWrites(t *testing.T) {
	lean, err := OpenEphemeralProjectionTablesOnly()
	if err != nil {
		t.Fatalf("open tables-only: %v", err)
	}
	defer lean.Close()

	var queryOnly int
	if err := lean.QueryRow(`PRAGMA query_only`).Scan(&queryOnly); err != nil {
		t.Fatalf("read query_only: %v", err)
	}
	if queryOnly == 0 {
		t.Fatal("tables-only projection has query_only=0; isReadOnlyDB would classify it writable")
	}

	if _, err := lean.Exec(`INSERT INTO features (id, title) VALUES ('feat-x', 't')`); err == nil {
		t.Fatal("write through the tables-only projection succeeded; it must be rejected")
	}

	// The fully-indexed projection must stay writable — only the lean handle
	// is locked down.
	full, err := OpenEphemeralProjection()
	if err != nil {
		t.Fatalf("open full: %v", err)
	}
	defer full.Close()
	var fullQueryOnly int
	if err := full.QueryRow(`PRAGMA query_only`).Scan(&fullQueryOnly); err != nil {
		t.Fatalf("read full query_only: %v", err)
	}
	if fullQueryOnly != 0 {
		t.Fatal("the full ephemeral projection must remain writable")
	}
}
