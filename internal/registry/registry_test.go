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

// makeRealProject creates a tempdir that passes looksLikeRealProject:
// it has a .htmlgraph/ subdirectory and a .git/ directory (in the same dir,
// satisfying the ancestor walk). Returns the project root.
func makeRealProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".htmlgraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

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
	projectDir := makeRealProject(t)
	r, err := registry.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert(projectDir, "my-project", "https://github.com/example/repo")

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
	if e.ProjectDir != projectDir {
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
	projectDir := makeRealProject(t)
	r, err := registry.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert(projectDir, "project-a", "")
	if len(r.List()) == 0 {
		t.Fatal("expected entry after first Upsert, got 0")
	}
	firstID := r.List()[0].ID
	firstSeen := r.List()[0].LastSeen

	// Re-upsert same dir.
	r.Upsert(projectDir, "project-a-renamed", "")
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
	alphaDir := makeRealProject(t)
	betaDir := makeRealProject(t)

	path := filepath.Join(t.TempDir(), "sub", "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert(alphaDir, "alpha", "git@github.com:alpha/alpha.git")
	r.Upsert(betaDir, "beta", "")

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
	projectDir := makeRealProject(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert(projectDir, "foo", "")
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

	// Valid project: has both .htmlgraph and .git subdirectories (passes Upsert guard).
	validDir := filepath.Join(tmp, "valid-project")
	if err := os.MkdirAll(filepath.Join(validDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(validDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stale project: was once valid (upserted), then .htmlgraph was removed.
	staleDir := filepath.Join(tmp, "stale-project")
	if err := os.MkdirAll(filepath.Join(staleDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staleDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(tmp, "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert(validDir, "valid", "")
	r.Upsert(staleDir, "stale", "")

	// Simulate staleness by removing .htmlgraph from staleDir.
	if err := os.RemoveAll(filepath.Join(staleDir, ".htmlgraph")); err != nil {
		t.Fatal(err)
	}

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

	// Create real-looking project dirs (with .htmlgraph + .git) so Upsert
	// accepts them.
	makeProjAt := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".htmlgraph"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mainDir := filepath.Join(tmp, "main")
	wt1 := filepath.Join(tmp, "wt-feat-a")
	wt2 := filepath.Join(tmp, "wt-feat-b")
	standalone := filepath.Join(tmp, "other-project")
	makeProjAt(mainDir)
	makeProjAt(wt1)
	makeProjAt(wt2)
	makeProjAt(standalone)

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
	aDir := filepath.Join(tmp, "a")
	if err := os.MkdirAll(filepath.Join(aDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(aDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Upsert(aDir, "a", "")
	dropped := r.DropLinkedWorktrees(nil)
	if dropped != nil {
		t.Errorf("nil resolver should return nil, got %v", dropped)
	}
	if len(r.List()) != 1 {
		t.Errorf("nil resolver should not mutate entries")
	}
}

// TestDefaultPath verifies the path falls back to ~/.local/share/htmlgraph/projects.json
// when XDG_DATA_HOME is not set, and honors XDG_DATA_HOME when it is set.
func TestDefaultPath(t *testing.T) {
	// Sub-test: XDG_DATA_HOME unset — expect home-dir fallback.
	t.Run("fallback", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		got := registry.DefaultPath()
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot determine home dir: %v", err)
		}
		expected := filepath.Join(home, ".local", "share", "htmlgraph", "projects.json")
		if got != expected {
			t.Errorf("DefaultPath() = %q, want %q", got, expected)
		}
	})

	// Sub-test: XDG_DATA_HOME set — expect XDG-rooted path.
	t.Run("xdg", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		got := registry.DefaultPath()
		expected := filepath.Join(xdg, "htmlgraph", "projects.json")
		if got != expected {
			t.Errorf("DefaultPath() = %q, want %q", got, expected)
		}
	})
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

// TestEntry_StableID verifies the same ProjectDir always yields the same 8-char SHA256 prefix.
func TestEntry_StableID(t *testing.T) {
	dir := makeRealProject(t)

	r1, _ := registry.Load(filepath.Join(t.TempDir(), "p1.json"))
	r1.Upsert(dir, "proj", "")
	if len(r1.List()) == 0 {
		t.Fatal("expected entry after Upsert in r1")
	}
	id1 := r1.List()[0].ID

	r2, _ := registry.Load(filepath.Join(t.TempDir(), "p2.json"))
	r2.Upsert(dir, "proj", "")
	if len(r2.List()) == 0 {
		t.Fatal("expected entry after Upsert in r2")
	}
	id2 := r2.List()[0].ID

	if id1 != id2 {
		t.Errorf("IDs differ for same dir: %q vs %q", id1, id2)
	}
	if len(id1) != 8 {
		t.Errorf("ID must be 8 chars, got %q", id1)
	}
}

// TestNoRegistryPollution is a load-bearing regression test that verifies
// Upsert silently skips directories that are not inside a git repository
// (the shape of all Go test tempdirs), preventing registry pollution from
// test runs.
func TestNoRegistryPollution(t *testing.T) {
	// Isolate registry writes to a per-test tmpdir.
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)

	regPath := registry.DefaultPath()
	loadCount := func() int {
		r, err := registry.Load(regPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return len(r.List())
	}

	baseline := loadCount()

	// Upsert from a plain tempdir (no .htmlgraph/, no .git ancestor).
	// This simulates a Go test tempdir that should NOT pollute the registry.
	t.Run("tempdir_rejected", func(t *testing.T) {
		ghost := t.TempDir() // No .htmlgraph/, no .git
		r, _ := registry.Load(regPath)
		r.Upsert(ghost, "ghost", "")
		if err := r.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		after := loadCount()
		if after != baseline {
			t.Errorf("registry grew from %d to %d after Upsert of plain tempdir — pollution not blocked",
				baseline, after)
		}
	})

	// Upsert from a dir that has .htmlgraph/ but no .git ancestor.
	// Should also be rejected.
	t.Run("htmlgraph_but_no_git_rejected", func(t *testing.T) {
		partial := t.TempDir()
		if err := os.MkdirAll(filepath.Join(partial, ".htmlgraph"), 0o755); err != nil {
			t.Fatal(err)
		}
		r, _ := registry.Load(regPath)
		r.Upsert(partial, "partial", "")
		if err := r.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		after := loadCount()
		if after != baseline {
			t.Errorf("registry grew from %d to %d after Upsert of dir without .git — pollution not blocked",
				baseline, after)
		}
	})

	// Upsert from a real-looking project (tempdir + .htmlgraph + .git).
	// Should be accepted and grow the count by exactly 1.
	t.Run("real_project_accepted", func(t *testing.T) {
		real := makeRealProject(t)
		r, _ := registry.Load(regPath)
		r.Upsert(real, "real", "")
		if err := r.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		after := loadCount()
		if after != baseline+1 {
			t.Errorf("expected registry to grow by 1 (from %d to %d), got %d",
				baseline, baseline+1, after)
		}
	})
}
