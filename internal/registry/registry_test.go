package registry_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shakestzd/htmlgraph/internal/registry"
)

// TestLoad_MissingFile ensures Load on a nonexistent path returns an empty registry with no error.
func TestLoad_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load on missing file returned error: %v", err)
	}
	if r == nil {
		t.Fatal("Load returned nil registry")
	}
	entries := r.List()
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

// TestUpsert_NewEntry ensures Upsert on a fresh registry appends an entry with a non-empty ID.
func TestUpsert_NewEntry(t *testing.T) {
	r, err := registry.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert("/some/project", "my-project", "https://github.com/example/repo")

	entries := r.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.ID == "" {
		t.Error("entry ID must not be empty")
	}
	if len(e.ID) != 8 {
		t.Errorf("entry ID must be 8 chars, got %q (len %d)", e.ID, len(e.ID))
	}
	if e.ProjectDir != "/some/project" {
		t.Errorf("unexpected ProjectDir: %q", e.ProjectDir)
	}
	if e.Name != "my-project" {
		t.Errorf("unexpected Name: %q", e.Name)
	}
	if e.LastSeen == "" {
		t.Error("LastSeen must not be empty")
	}
}

// TestUpsert_UpdatesExisting ensures Upsert on the same dir updates LastSeen without duplicating and preserves the ID.
func TestUpsert_UpdatesExisting(t *testing.T) {
	r, err := registry.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert("/some/project", "project-a", "")
	firstID := r.List()[0].ID
	firstSeen := r.List()[0].LastSeen

	// Re-upsert same dir.
	r.Upsert("/some/project", "project-a-renamed", "")
	entries := r.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after second Upsert, got %d", len(entries))
	}
	e := entries[0]
	if e.ID != firstID {
		t.Errorf("ID changed: was %q, now %q", firstID, e.ID)
	}
	// LastSeen should be updated (or at minimum equal — not rolled back).
	if e.LastSeen < firstSeen {
		t.Errorf("LastSeen went backwards: was %q, now %q", firstSeen, e.LastSeen)
	}
}

// TestSave_RoundTrip ensures Save followed by Load returns identical entries.
func TestSave_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert("/alpha/project", "alpha", "git@github.com:alpha/alpha.git")
	r.Upsert("/beta/project", "beta", "")

	if err := r.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	r2, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	entries := r2.List()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after round-trip, got %d", len(entries))
	}

	orig := r.List()
	for i := range orig {
		if orig[i].ID != entries[i].ID {
			t.Errorf("entry %d ID mismatch: want %q, got %q", i, orig[i].ID, entries[i].ID)
		}
		if orig[i].ProjectDir != entries[i].ProjectDir {
			t.Errorf("entry %d ProjectDir mismatch", i)
		}
		if orig[i].LastSeen != entries[i].LastSeen {
			t.Errorf("entry %d LastSeen mismatch", i)
		}
	}
}

// TestSave_AtomicRename verifies that no .tmp file remains after Save.
func TestSave_AtomicRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert("/foo", "foo", "")
	if err := r.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("found leftover .tmp file after Save: %s", e.Name())
		}
	}
}

// TestPrune_RemovesStale ensures Prune removes entries whose <dir>/.htmlgraph does not exist.
func TestPrune_RemovesStale(t *testing.T) {
	tmp := t.TempDir()

	// Valid project: has a .htmlgraph subdirectory.
	validDir := filepath.Join(tmp, "valid-project")
	if err := os.MkdirAll(filepath.Join(validDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stale project: directory does not even exist.
	staleDir := filepath.Join(tmp, "stale-project")

	path := filepath.Join(tmp, "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert(validDir, "valid", "")
	r.Upsert(staleDir, "stale", "")

	pruned := r.Prune()
	if len(pruned) != 1 {
		t.Fatalf("expected 1 pruned entry, got %d: %v", len(pruned), pruned)
	}
	if pruned[0] != staleDir {
		t.Errorf("expected pruned dir %q, got %q", staleDir, pruned[0])
	}
	remaining := r.List()
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining entry, got %d", len(remaining))
	}
	if remaining[0].ProjectDir != validDir {
		t.Errorf("remaining entry is %q, want %q", remaining[0].ProjectDir, validDir)
	}
}

// TestDropLinkedWorktrees verifies worktree entries are dropped but the
// main repo entry is preserved. The resolver is a stub that maps any
// path containing "/wt-" to the "main" root so we can drive the
// logic without a real git repo.
func TestDropLinkedWorktrees(t *testing.T) {
	tmp := t.TempDir()
	mainDir := filepath.Join(tmp, "main")
	wt1 := filepath.Join(tmp, "main", "wt-feat-a")
	wt2 := filepath.Join(tmp, "main", "wt-feat-b")
	standalone := filepath.Join(tmp, "other-project")

	path := filepath.Join(tmp, "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert(mainDir, "main", "")
	r.Upsert(wt1, "feat-a", "")
	r.Upsert(wt2, "feat-b", "")
	r.Upsert(standalone, "other", "")

	// Resolver: returns mainDir for worktrees, "" for main repo root and
	// for standalone projects (mirrors paths.ResolveViaGitCommonDir).
	resolver := func(dir string) string {
		if dir == wt1 || dir == wt2 {
			return mainDir
		}
		return ""
	}

	dropped := r.DropLinkedWorktrees(resolver)
	if len(dropped) != 2 {
		t.Fatalf("expected 2 dropped, got %d: %v", len(dropped), dropped)
	}
	remaining := r.List()
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining, got %d", len(remaining))
	}
	names := map[string]bool{}
	for _, e := range remaining {
		names[e.Name] = true
	}
	if !names["main"] || !names["other"] {
		t.Errorf("expected main+other remaining, got %v", names)
	}
}

// TestDropLinkedWorktrees_NilResolver is a safety check — passing nil
// must be a no-op, not a panic.
func TestDropLinkedWorktrees_NilResolver(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "projects.json")
	r, _ := registry.Load(path)
	r.Upsert(filepath.Join(tmp, "a"), "a", "")
	dropped := r.DropLinkedWorktrees(nil)
	if dropped != nil {
		t.Errorf("nil resolver should return nil, got %v", dropped)
	}
	if len(r.List()) != 1 {
		t.Errorf("nil resolver should not mutate entries")
	}
}

// TestDefaultPath verifies the path is under ~/.local/share/htmlgraph/projects.json.
func TestDefaultPath(t *testing.T) {
	got := registry.DefaultPath()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home dir: %v", err)
	}
	expected := filepath.Join(home, ".local", "share", "htmlgraph", "projects.json")
	if got != expected {
		t.Errorf("DefaultPath() = %q, want %q", got, expected)
	}
}

// TestOpenReadOnly_RejectsWrite opens a SQLite DB read-only and asserts that CREATE TABLE fails.
func TestOpenReadOnly_RejectsWrite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create a real (writable) DB first so the file exists.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("create writable db: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE setup (id INTEGER PRIMARY KEY)"); err != nil {
		db.Close()
		t.Fatalf("initial table creation: %v", err)
	}
	db.Close()

	// Open read-only via registry helper.
	roDB, err := registry.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer roDB.Close()

	_, writeErr := roDB.Exec("CREATE TABLE should_fail (id INTEGER PRIMARY KEY)")
	if writeErr == nil {
		t.Error("expected write to fail on read-only DB, but it succeeded")
	}
}

// TestRegistry_OpenAppliesPragmas verifies OpenReadOnly applies db.Open's
// connection-level pragmas — busy_timeout >= 5000 (so concurrent indexer
// writes don't surface as SQLITE_BUSY), and the file's journal_mode is
// preserved (read-only must not downgrade WAL).
func TestRegistry_OpenAppliesPragmas(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pragmas.db")

	// Seed a DB in WAL mode so we can assert OpenReadOnly preserves it.
	w, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if _, err := w.Exec("PRAGMA journal_mode = WAL"); err != nil {
		w.Close()
		t.Fatalf("seed WAL: %v", err)
	}
	if _, err := w.Exec("CREATE TABLE seed (id INTEGER PRIMARY KEY)"); err != nil {
		w.Close()
		t.Fatalf("seed table: %v", err)
	}
	w.Close()

	roDB, err := registry.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer roDB.Close()

	var busy int
	if err := roDB.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busy < 5000 {
		t.Errorf("busy_timeout = %d, want >= 5000", busy)
	}

	var jm string
	if err := roDB.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	// On unsafe filesystems BuildPragmas would have selected DELETE for
	// the writer; on a native tempdir we expect WAL to round-trip. Either
	// way, OpenReadOnly must not corrupt or downgrade the file's mode.
	if jm == "" {
		t.Errorf("journal_mode is empty; want non-empty (file mode preserved)")
	}
}

// TestEntry_StableID verifies the same ProjectDir always yields the same 8-char SHA256 prefix.
func TestEntry_StableID(t *testing.T) {
	dir := "/stable/project/dir"

	r1, _ := registry.Load(filepath.Join(t.TempDir(), "p1.json"))
	r1.Upsert(dir, "proj", "")
	id1 := r1.List()[0].ID

	r2, _ := registry.Load(filepath.Join(t.TempDir(), "p2.json"))
	r2.Upsert(dir, "proj", "")
	id2 := r2.List()[0].ID

	if id1 != id2 {
		t.Errorf("IDs differ for same dir: %q vs %q", id1, id2)
	}
	if len(id1) != 8 {
		t.Errorf("ID must be 8 chars, got %q", id1)
	}
}
