package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// The ephemeral projection's schema is COMPILED IN, not migrated.
//
// Every ephemeral open used to run the whole migration chain to produce the
// same tables and indexes, which measured 9.7ms — paid per CLI command and per
// hook invocation, i.e. per tool call.
//
// Snapshotting the chain at RUNTIME does not help: a hook process opens the
// projection exactly once, so it would pay the chain anyway plus the replay.
// The chain's only input is the compiled-in migration slice, so the schema it
// produces is a build-time constant; ephemeral_schema_gen.go holds it and
// TestEphemeralSchemaMatchesMigrations fails if the two drift.
//
// Measured on this corpus: migration chain 9.7ms, generated DDL replay 3.5ms,
// tables-only replay 1.4ms.
func ephemeralSchemaDDL() (tables, aux []string, ok bool) {
	// A stale generated file is a build-time mistake, not a runtime error:
	// fall back to the chain so behaviour stays correct until it is regenerated.
	if generatedSchemaVersion != currentSchemaVersion || len(generatedTablesDDL) == 0 {
		return nil, nil, false
	}
	return generatedTablesDDL, generatedAuxDDL, true
}

// openBareEphemeral opens an in-memory database with the ephemeral pragmas
// applied and no schema at all.
func openBareEphemeral() (*sql.DB, error) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open ephemeral projection: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	if err := ApplyPragmas(database, EphemeralPragmas()); err != nil {
		database.Close()
		return nil, fmt.Errorf("open ephemeral projection: apply pragmas: %w", err)
	}
	return database, nil
}

// OpenEphemeralProjection opens a private, process-local compatibility
// projection. It never accepts a path and therefore never creates a project DB,
// WAL/SHM sidecar, or cache directory.
func OpenEphemeralProjection() (*sql.DB, error) {
	return openEphemeralProjection(false)
}

// OpenEphemeralProjectionTablesOnly is OpenEphemeralProjection without the 97
// indexes. It exists for read-only consumers of an EMPTY projection — the hook
// read path (see core/hooks.OpenHookDBReadOnly), where every query returns zero
// rows and an index therefore accelerates nothing.
//
// Reads behave identically to a fully-indexed projection: every table exists,
// so queries succeed and return no rows rather than failing with "no such
// table".
//
// It is NOT safe for writes — several tables rely on UNIQUE indexes for
// upsert/conflict semantics that this handle does not create — so the handle
// sets PRAGMA query_only, which makes the engine reject writes outright rather
// than leaving "nobody writes through it" as an unenforced convention. That
// also makes core/hooks.isReadOnlyDB report true for it, so the hook write
// router sends writes to a properly-schema'd handle instead of this one.
func OpenEphemeralProjectionTablesOnly() (*sql.DB, error) {
	database, err := openEphemeralProjection(true)
	if err != nil {
		return nil, err
	}
	if _, err := database.Exec("PRAGMA query_only = ON"); err != nil {
		database.Close()
		return nil, fmt.Errorf("open ephemeral projection: set query_only: %w", err)
	}
	return database, nil
}

func openEphemeralProjection(tablesOnly bool) (*sql.DB, error) {
	tables, aux, ok := ephemeralSchemaDDL()
	if !ok {
		// Fall back to the migration chain rather than failing the open: a
		// stale snapshot must not take out every hook and CLI command.
		return openEphemeralViaMigrations()
	}

	database, err := openBareEphemeral()
	if err != nil {
		return nil, err
	}
	stmts := tables
	if !tablesOnly {
		stmts = append(append(make([]string, 0, len(tables)+len(aux)), tables...), aux...)
	}
	for _, stmt := range stmts {
		if _, err := database.Exec(stmt); err != nil {
			database.Close()
			return nil, fmt.Errorf("open ephemeral projection: apply schema: %w", err)
		}
	}
	// runMigrations bumps user_version after each step; replaying the DDL
	// bypasses that, so stamp it here or a later RunMigrations on this handle
	// would re-run the whole chain against an already-built schema.
	if err := writeUserVersion(database, currentSchemaVersion); err != nil {
		database.Close()
		return nil, fmt.Errorf("open ephemeral projection: stamp schema version: %w", err)
	}
	if err := VerifyEphemeralPragmas(database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

// openEphemeralViaMigrations is the original path, retained as the fallback.
func openEphemeralViaMigrations() (*sql.DB, error) {
	database, err := openBareEphemeral()
	if err != nil {
		return nil, err
	}
	if err := RunMigrations(database); err != nil {
		database.Close()
		return nil, fmt.Errorf("open ephemeral projection: migrate schema: %w", err)
	}
	if err := VerifyEphemeralPragmas(database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func EphemeralPragmas() map[string]string {
	return map[string]string{
		"journal_mode": "MEMORY",
		"synchronous":  "OFF",
		"foreign_keys": "ON",
		"temp_store":   "MEMORY",
		"mmap_size":    "0",
	}
}

func VerifyEphemeralPragmas(db *sql.DB) error {
	checks := map[string]func(string) bool{
		"journal_mode": func(v string) bool { return strings.EqualFold(v, "memory") },
		"temp_store":   func(v string) bool { return v == "2" || strings.EqualFold(v, "memory") },
		"synchronous":  func(v string) bool { return v == "0" || strings.EqualFold(v, "off") },
		"foreign_keys": func(v string) bool { return v == "1" || strings.EqualFold(v, "on") },
		"mmap_size":    func(v string) bool { return v == "0" },
	}
	for pragma, ok := range checks {
		var got string
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			if pragma == "mmap_size" && errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return fmt.Errorf("verify ephemeral PRAGMA %s: %w", pragma, err)
		}
		if !ok(got) {
			return fmt.Errorf("verify ephemeral PRAGMA %s: got %q", pragma, got)
		}
	}
	return nil
}
