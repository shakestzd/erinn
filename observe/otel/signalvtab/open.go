package signalvtab

import (
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite" // registers the "sqlite" driver and the vtab bridge
	"modernc.org/sqlite/vtab"
)

// ModuleName is the SQLite module name the shared registration uses.
const ModuleName = "wipnote_signals"

// DefaultTableName is the table Open creates.
const DefaultTableName = "signals"

var (
	// registerMu serialises registration. The driver's module map is a plain
	// map written without a lock, so concurrent RegisterModule calls would
	// race; and registration is keyed by name process-wide, so a second
	// attempt with the same name is an error rather than a no-op.
	registerMu sync.Mutex

	sharedOnce sync.Once
	sharedMod  *Module
	sharedErr  error

	isolatedSeq atomic.Int64
)

// Register installs the shared module on the driver, once per process, and
// returns it. Every table created from it takes its shard directory from the
// CREATE VIRTUAL TABLE argument, so one registration serves any number of
// projects.
//
// Registration only affects connections opened after this call. Call it
// immediately after sql.Open and before anything — including Ping — touches
// the pool, or the connection that gets checked out will not have the module
// and CREATE VIRTUAL TABLE will fail with "no such module".
//
// The db argument is unused by the driver; it is accepted to make the
// ordering requirement legible at the call site.
func Register(db *sql.DB) (*Module, error) {
	sharedOnce.Do(func() {
		m := NewModule("")
		registerMu.Lock()
		defer registerMu.Unlock()
		if err := vtab.RegisterModule(db, ModuleName, m); err != nil {
			sharedErr = fmt.Errorf("signalvtab: register module %q: %w", ModuleName, err)
			return
		}
		sharedMod = m
	})
	return sharedMod, sharedErr
}

// RegisterAs installs m under an explicit name. Modules cannot be
// unregistered, so each distinct name costs one permanent map entry on the
// driver; use Register for production paths and reserve this for tests that
// need isolated Stats or an injected opener.
func RegisterAs(db *sql.DB, name string, m *Module) error {
	registerMu.Lock()
	defer registerMu.Unlock()
	if err := vtab.RegisterModule(db, name, m); err != nil {
		return fmt.Errorf("signalvtab: register module %q: %w", name, err)
	}
	return nil
}

// Open returns an in-memory database with a read-only virtual table named
// DefaultTableName over the shards in sessionsDir, plus the shared module so
// callers can read its Stats.
//
// The connection pool is capped at one connection on purpose. An in-memory
// SQLite database is per-connection: a second pooled connection would open a
// second, empty database that has never seen the CREATE VIRTUAL TABLE, and
// queries would fail with "no such table" depending on which connection the
// pool happened to hand out.
func Open(sessionsDir string) (*sql.DB, *Module, error) {
	return openWith(sessionsDir, "", nil)
}

// OpenIsolated is Open with a freshly-named module, so the returned Module's
// Stats and opener belong to this call alone. Intended for tests and
// benchmarks; see RegisterAs for the cost of each new name.
func OpenIsolated(sessionsDir string) (*sql.DB, *Module, error) {
	name := fmt.Sprintf("%s_iso_%d", ModuleName, isolatedSeq.Add(1))
	return openWith(sessionsDir, name, NewModule(""))
}

func openWith(sessionsDir, name string, m *Module) (*sql.DB, *Module, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, nil, fmt.Errorf("signalvtab: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	// Register before any statement runs. Nothing above this line has
	// touched the pool, so the connection opened by the Exec below will
	// carry the module.
	if name == "" {
		m, err = Register(db)
	} else {
		err = RegisterAs(db, name, m)
	}
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	if name == "" {
		name = ModuleName
	}

	// %q quotes the path. SQLite hands module arguments to Create as raw
	// text with those quotes still attached; unquoteArg strips them.
	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s USING %s(%q)", DefaultTableName, name, sessionsDir)
	if _, err := db.Exec(stmt); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("signalvtab: create virtual table: %w", err)
	}
	return db, m, nil
}
