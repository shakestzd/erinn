package registry_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/shakestzd/wipnote/internal/registry"
)

// makeSafeBaseDir creates a temp directory directly under os.TempDir() using
// a "proj-*" prefix so no component of the path starts with "Test". This is
// required for tests that call Upsert: t.TempDir() produces a path whose
// parent starts with "Test" (e.g. /tmp/TestFooBar123/001) which triggers
// ShouldSkipRegistration, causing Upsert to silently skip the entry.
//
// Returns the directory path. t.Cleanup is registered to remove it.
func makeSafeBaseDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "proj-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// makeRealProject creates a directory that passes looksLikeRealProject:
// it has a .wipnote/ subdirectory and a .git/ directory. Returns the project root.
//
// The directory is created directly under os.TempDir() with a "proj-*" prefix,
// NOT under t.TempDir(). This is intentional: t.TempDir() returns a path whose
// parent component starts with "Test", which would trigger ShouldSkipRegistration
// and cause Upsert to silently skip the entry — breaking tests that explicitly
// verify registry behaviour for real-looking project directories.
func makeRealProject(t *testing.T) string {
	t.Helper()
	dir := makeSafeBaseDir(t)
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// makePersistentRegistryPath creates a registry path outside os.TempDir() so
// tests can exercise the persistent-registry guard without touching the real
// user registry.
func makePersistentRegistryPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(wd, ".registry-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "projects.json")
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

// TestPrune_RemovesStale ensures Prune removes entries whose <dir>/.wipnote does not exist.
func TestPrune_RemovesStale(t *testing.T) {
	// Use makeSafeBaseDir to avoid ShouldSkipRegistration blocking Upsert.
	validDir := makeSafeBaseDir(t)
	staleDir := makeSafeBaseDir(t)

	// Valid project: has both .wipnote and .git subdirectories (passes Upsert guard).
	if err := os.MkdirAll(filepath.Join(validDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(validDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stale project: was once valid (upserted), then .wipnote was removed.
	if err := os.MkdirAll(filepath.Join(staleDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staleDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "projects.json")
	r, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r.Upsert(validDir, "valid", "")
	r.Upsert(staleDir, "stale", "")

	// Simulate staleness by removing .wipnote from staleDir.
	if err := os.RemoveAll(filepath.Join(staleDir, ".wipnote")); err != nil {
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
	// Create real-looking project dirs (with .wipnote + .git) so Upsert
	// accepts them. Use makeSafeBaseDir to avoid ShouldSkipRegistration.
	addHgGit := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mainDir := makeSafeBaseDir(t)
	wt1 := makeSafeBaseDir(t)
	wt2 := makeSafeBaseDir(t)
	standalone := makeSafeBaseDir(t)
	addHgGit(mainDir)
	addHgGit(wt1)
	addHgGit(wt2)
	addHgGit(standalone)

	path := filepath.Join(t.TempDir(), "projects.json")
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
	path := filepath.Join(t.TempDir(), "projects.json")
	r, _ := registry.Load(path)
	// Use makeSafeBaseDir to avoid ShouldSkipRegistration rejecting the Upsert.
	aDir := makeSafeBaseDir(t)
	if err := os.MkdirAll(filepath.Join(aDir, ".wipnote"), 0o755); err != nil {
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

// TestDefaultPath verifies the path falls back to ~/.local/share/wipnote/projects.json
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
		expected := filepath.Join(home, ".local", "share", "wipnote", "projects.json")
		if got != expected {
			t.Errorf("DefaultPath() = %q, want %q", got, expected)
		}
	})

	// Sub-test: XDG_DATA_HOME set — expect XDG-rooted path.
	t.Run("xdg", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		got := registry.DefaultPath()
		expected := filepath.Join(xdg, "wipnote", "projects.json")
		if got != expected {
			t.Errorf("DefaultPath() = %q, want %q", got, expected)
		}
	})
}

// TestLoad_LegacyFallback verifies the migrate-on-save behaviour: when the
// canonical XDG path is missing but the legacy ~/.local/share path exists,
// Load reads from legacy and the next Save persists to the canonical path
// (the legacy file is left untouched as a side-effect-free safety copy).
func TestLoad_LegacyFallback(t *testing.T) {
	// Redirect $HOME so legacyDefaultPath() points into a tempdir we control,
	// and XDG_DATA_HOME so canonicalDefaultPath() points elsewhere. With both
	// pinned to tempdirs the test never touches the real user home.
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)

	canonical := registry.DefaultPath()
	legacy := filepath.Join(home, ".local", "share", "wipnote", "projects.json")
	if canonical == legacy {
		t.Fatalf("canonical %q must differ from legacy %q for this test", canonical, legacy)
	}

	// Seed the legacy file with a real-looking entry. We bypass Save because
	// Save would write to canonical via Registry.path; here we want raw JSON
	// at the legacy location to simulate a pre-XDG install.
	projectDir := makeRealProject(t)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("mkdir legacy parent: %v", err)
	}
	seed, _ := registry.Load(legacy)
	seed.Upsert(projectDir, "legacy-proj", "")
	if err := seed.Save(); err != nil {
		t.Fatalf("seed legacy save: %v", err)
	}
	if _, err := os.Stat(canonical); err == nil {
		t.Fatalf("canonical %q must not exist before migration", canonical)
	}

	// Load(canonical) should fall back to legacy and pick up the entry.
	r, err := registry.Load(canonical)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := r.List()
	if len(entries) != 1 || entries[0].ProjectDir != projectDir {
		t.Fatalf("expected legacy entry to surface, got %+v", entries)
	}

	// Save persists to canonical, not legacy.
	if err := r.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Errorf("canonical not created after Save: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy file vanished after migration save (should be left intact): %v", err)
	}

	// Subsequent Load(canonical) reads canonical, not legacy.
	r2, err := registry.Load(canonical)
	if err != nil {
		t.Fatalf("post-migration Load: %v", err)
	}
	if len(r2.List()) != 1 {
		t.Errorf("post-migration entries = %d, want 1", len(r2.List()))
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

// TestNoRegistryPollution verifies Upsert's gate: directories without
// a .wipnote/ subdirectory are silently rejected, while directories
// that do have one are accepted regardless of whether they sit inside
// a git repository (review #55 F1 — wipnote projects are not required
// to be Git repos). Test pollution is prevented by the XDG_DATA_HOME
// isolation set up at the top of this test, NOT by a .git heuristic.
func TestNoRegistryPollution(t *testing.T) {
	// Isolate BOTH registry locations: XDG (canonical) and HOME (legacy
	// fallback). Without pinning HOME, the legacy-fallback in Load reads
	// the user's real ~/.local/share/wipnote/projects.json on the first
	// loadCount() call — corrupting the baseline.
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", t.TempDir())

	regPath := registry.DefaultPath()
	loadCount := func() int {
		r, err := registry.Load(regPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return len(r.List())
	}

	// Each sub-test reloads the baseline because Save() calls Prune(),
	// which drops entries whose ProjectDir was cleaned up by a previous
	// sub-test's t.TempDir(). Without re-baselining, a "+1 expected" check
	// can fail because the prior entry was silently pruned mid-flight.

	// Upsert from a plain tempdir (no .wipnote/) — must be rejected so
	// that hooks running inside a stray cwd cannot register garbage.
	t.Run("tempdir_no_wipnote_rejected", func(t *testing.T) {
		baseline := loadCount()
		ghost := t.TempDir()
		r, _ := registry.Load(regPath)
		r.Upsert(ghost, "ghost", "")
		if err := r.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		after := loadCount()
		if after != baseline {
			t.Errorf("registry grew from %d to %d after Upsert of plain tempdir — gate did not reject",
				baseline, after)
		}
	})

	// Upsert from a dir with .wipnote/ but no .git ancestor — must be
	// ACCEPTED. Non-Git projects are valid wipnote projects.
	// Use a proj-* prefixed dir directly under os.TempDir() so the path
	// doesn't have a Test* component (which would trigger ShouldSkipRegistration).
	t.Run("wipnote_without_git_accepted", func(t *testing.T) {
		baseline := loadCount()
		nonGit, err := os.MkdirTemp(os.TempDir(), "proj-nongit-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(nonGit) })
		if err := os.MkdirAll(filepath.Join(nonGit, ".wipnote"), 0o755); err != nil {
			t.Fatal(err)
		}
		r, _ := registry.Load(regPath)
		r.Upsert(nonGit, "non-git", "")
		if err := r.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		after := loadCount()
		if after != baseline+1 {
			t.Errorf("non-Git wipnote project must register: registry went from %d to %d, want %d",
				baseline, after, baseline+1)
		}
	})

	// Upsert from a real-looking project (tempdir + .wipnote + .git).
	// Verify the new entry shows up in the post-Save list — counting
	// deltas is unreliable because Save's internal Prune drops entries
	// whose ProjectDir got cleaned up by a previous sub-test's tempdir.
	t.Run("real_project_accepted", func(t *testing.T) {
		real := makeRealProject(t)
		r, _ := registry.Load(regPath)
		r.Upsert(real, "real", "")
		if err := r.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		reloaded, err := registry.Load(regPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		found := false
		for _, e := range reloaded.List() {
			if e.ProjectDir == real {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("real project %q missing from saved registry; entries=%+v", real, reloaded.List())
		}
	})
}

// TestSave_AtomicTempfileUnique verifies F2: writeEntriesAtomic must not
// leave a fixed-name `<path>.tmp` file behind, because two concurrent
// Save calls would collide on it. We can't easily race-test mid-Save
// from a test, but we can prove the new contract by writing many times
// and confirming no `<path>.tmp` artifact ever appears.
func TestSave_AtomicTempfileUnique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")

	for i := 0; i < 5; i++ {
		r, _ := registry.Load(path)
		r.Upsert(filepath.Join(dir, "p"), "p", "")
		if err := r.Save(); err != nil {
			t.Fatalf("Save iter %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Errorf("found leftover %s.tmp — fixed-name tempfile is back", path)
	}
	// Any randomised `*.tmp` siblings must also be cleaned up.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("found leftover tempfile after Save: %s", e.Name())
		}
	}
}

// TestLoad_LegacyCorrupt covers F3: when the legacy fallback reads a
// present-but-corrupt legacy file, Load must surface the error rather
// than silently treating it as missing (which would let the next Save
// overwrite the canonical path with `[]`).
func TestLoad_LegacyCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)

	canonical := registry.DefaultPath()
	legacy := filepath.Join(home, ".local", "share", "wipnote", "projects.json")

	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := registry.Load(canonical); err == nil {
		t.Fatal("Load returned nil error for corrupt legacy file; want error so caller halts before clobbering canonical")
	}
}

// TestLoad_MigrationPending covers F4: a successful legacy fallback
// surfaces a MigrationPending() flag so callers can save even when the
// in-memory slice is otherwise unchanged. After Save the flag clears.
func TestLoad_MigrationPending(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)

	canonical := registry.DefaultPath()
	legacy := filepath.Join(home, ".local", "share", "wipnote", "projects.json")

	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := makeRealProject(t)
	seed, _ := registry.Load(legacy)
	seed.Upsert(projectDir, "legacy-proj", "")
	if err := seed.Save(); err != nil {
		t.Fatalf("seed legacy save: %v", err)
	}

	r, err := registry.Load(canonical)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !r.MigrationPending() {
		t.Fatal("MigrationPending() = false after legacy fallback; want true")
	}

	if err := r.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if r.MigrationPending() {
		t.Error("MigrationPending() still true after Save; want false")
	}

	// A subsequent Load on canonical (which now exists) must NOT report
	// migration-pending.
	r2, err := registry.Load(canonical)
	if err != nil {
		t.Fatalf("post-migration Load: %v", err)
	}
	if r2.MigrationPending() {
		t.Error("post-migration Load reports MigrationPending(); want false")
	}
}

// ---- ShouldSkipRegistration tests ----

// TestShouldSkipRegistration_TempDirTestPattern verifies that a path inside
// the OS temp dir with a Test* component is identified as a skip candidate.
func TestShouldSkipRegistration_TempDirTestPattern(t *testing.T) {
	// Build a synthetic path under os.TempDir() with a "Test" component.
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		base = os.TempDir()
	}
	testPath := filepath.Join(base, "TestFooBar123", "sub")
	if !registry.ShouldSkipRegistration(testPath) {
		t.Errorf("ShouldSkipRegistration(%q) = false, want true", testPath)
	}
}

// TestShouldSkipRegistration_TempDirNonTest verifies that a path inside
// os.TempDir() without a Test* component is NOT skipped.
func TestShouldSkipRegistration_TempDirNonTest(t *testing.T) {
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		base = os.TempDir()
	}
	realTempPath := filepath.Join(base, "myproject")
	if registry.ShouldSkipRegistration(realTempPath) {
		t.Errorf("ShouldSkipRegistration(%q) = true, want false (no Test* component)", realTempPath)
	}
}

// TestShouldSkipRegistration_RealPath verifies a normal production path is not skipped.
func TestShouldSkipRegistration_RealPath(t *testing.T) {
	if registry.ShouldSkipRegistration("/workspaces/wipnote") {
		t.Error("ShouldSkipRegistration(/workspaces/wipnote) = true, want false")
	}
}

// TestShouldSkipRegistration_EnvVar verifies the WIPNOTE_SKIP_REGISTER=1 env
// var causes all paths to be skipped regardless of the path structure.
func TestShouldSkipRegistration_EnvVar(t *testing.T) {
	t.Setenv("WIPNOTE_SKIP_REGISTER", "1")
	if !registry.ShouldSkipRegistration("/workspaces/wipnote") {
		t.Error("ShouldSkipRegistration with WIPNOTE_SKIP_REGISTER=1 returned false, want true")
	}
	if !registry.ShouldSkipRegistration("/tmp/myproject") {
		t.Error("ShouldSkipRegistration with WIPNOTE_SKIP_REGISTER=1 returned false for /tmp path")
	}
}

// TestShouldSkipRegistration_UpsertBlocked verifies that Upsert silently
// rejects a path that ShouldSkipRegistration identifies as a test tempdir.
func TestShouldSkipRegistration_UpsertBlocked(t *testing.T) {
	// Build a fake Test* path under os.TempDir() that also has a .wipnote dir
	// (to ensure looksLikeRealProject would pass if skip weren't active).
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		base = os.TempDir()
	}
	testProjDir := filepath.Join(base, "TestRegistrySkip123")
	if err := os.MkdirAll(filepath.Join(testProjDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testProjDir)

	reg, _ := registry.Load(makePersistentRegistryPath(t))
	reg.Upsert(testProjDir, "test-proj", "")
	if len(reg.List()) != 0 {
		t.Errorf("Upsert accepted a test tempdir path; registry has %d entries, want 0", len(reg.List()))
	}
}

// ---- PruneStale tests ----

// TestPruneStale_RemovesOldEntries verifies that entries older than the TTL
// are removed while recent ones are kept.
func TestPruneStale_RemovesOldEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")

	old := time.Now().Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)

	entries := []registry.Entry{
		{ID: "aabbccdd", ProjectDir: "/old/proj", Name: "old", LastSeen: old},
		{ID: "11223344", ProjectDir: "/recent/proj", Name: "recent", LastSeen: recent},
	}
	if err := registry.WriteEntriesForTest(path, entries); err != nil {
		t.Fatalf("WriteEntriesForTest: %v", err)
	}

	reg, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	removed := registry.PruneStale(reg, 3*24*time.Hour)
	if removed != 1 {
		t.Errorf("PruneStale removed %d entries, want 1", removed)
	}
	remaining := reg.List()
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining entry, got %d", len(remaining))
	}
	if remaining[0].Name != "recent" {
		t.Errorf("wrong entry kept: %q", remaining[0].Name)
	}
}

// TestPruneStale_EmptyRegistry verifies PruneStale is a no-op on an empty registry.
func TestPruneStale_EmptyRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	reg, _ := registry.Load(path)
	removed := registry.PruneStale(reg, 3*24*time.Hour)
	if removed != 0 {
		t.Errorf("PruneStale on empty registry removed %d, want 0", removed)
	}
	if len(reg.List()) != 0 {
		t.Errorf("PruneStale on empty registry left %d entries, want 0", len(reg.List()))
	}
}

// TestPruneStale_AllRecent verifies PruneStale keeps all entries when none are stale.
func TestPruneStale_AllRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")

	recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	entries := []registry.Entry{
		{ID: "aabbccdd", ProjectDir: "/proj/a", Name: "a", LastSeen: recent},
		{ID: "11223344", ProjectDir: "/proj/b", Name: "b", LastSeen: recent},
	}
	if err := registry.WriteEntriesForTest(path, entries); err != nil {
		t.Fatalf("WriteEntriesForTest: %v", err)
	}

	reg, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	removed := registry.PruneStale(reg, 3*24*time.Hour)
	if removed != 0 {
		t.Errorf("PruneStale removed %d entries from all-recent registry, want 0", removed)
	}
	if len(reg.List()) != 2 {
		t.Errorf("expected 2 entries, got %d", len(reg.List()))
	}
}

// ---- GOTMPDIR detection tests ----

// TestIsGoTestTempDirPath_GOTMPDIRDetection verifies that a Test* path inside
// GOTMPDIR is identified as a test temp dir, even when it differs from os.TempDir().
// This covers the devcontainer case where GOTMPDIR=/home/vscode/.gotest-tmp but
// os.TempDir() returns /tmp.
func TestIsGoTestTempDirPath_GOTMPDIRDetection(t *testing.T) {
	// Use a dedicated temp dir that is NOT os.TempDir() to simulate GOTMPDIR.
	altTmp := t.TempDir()
	t.Setenv("GOTMPDIR", altTmp)

	// Build a synthetic Test* path inside altTmp.
	testPath := filepath.Join(altTmp, "TestFooBar1234567890", "001")
	if !registry.IsGoTestTempDirPath(testPath) {
		t.Errorf("IsGoTestTempDirPath(%q) = false, want true (GOTMPDIR=%s)", testPath, altTmp)
	}
}

// TestIsGoTestTempDirPath_GOTMPDIRNonTestPath verifies that a non-Test path
// inside GOTMPDIR is NOT identified as a test temp dir.
func TestIsGoTestTempDirPath_GOTMPDIRNonTestPath(t *testing.T) {
	altTmp := t.TempDir()
	t.Setenv("GOTMPDIR", altTmp)

	nonTestPath := filepath.Join(altTmp, "my-project")
	if registry.IsGoTestTempDirPath(nonTestPath) {
		t.Errorf("IsGoTestTempDirPath(%q) = true, want false (non-Test* path under GOTMPDIR)", nonTestPath)
	}
}

// TestUpsert_GOTMPDIRPathRejected verifies that Upsert silently rejects a path
// under GOTMPDIR that matches Go's t.TempDir() naming convention, even when
// the path has a .wipnote/ subdirectory and would otherwise look like a real project.
func TestUpsert_GOTMPDIRPathRejected(t *testing.T) {
	altTmp := t.TempDir()
	t.Setenv("GOTMPDIR", altTmp)

	// Create a project dir inside the synthetic GOTMPDIR with Test* naming.
	// This is exactly what test subprocesses that create t.TempDir() projects produce.
	testProjDir := filepath.Join(altTmp, "TestOtelCollect12345", "001")
	if err := os.MkdirAll(filepath.Join(testProjDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Use a persistent registry path so the normal tempdir-path guard activates.
	regPath := makePersistentRegistryPath(t)
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	reg.Upsert(testProjDir, "test-proj", "")
	if n := len(reg.List()); n != 0 {
		t.Errorf("Upsert accepted GOTMPDIR test path; registry has %d entries, want 0", n)
	}
}

// TestPathInsideTempDir_GOTMPDIRRecognised verifies that pathInsideTempDir
// (indirectly exercised via ShouldSkipRegistration) treats GOTMPDIR as a
// temp root when checking whether a registry file is inside a temp dir.
func TestPathInsideTempDir_GOTMPDIRRecognised(t *testing.T) {
	altTmp := t.TempDir()
	t.Setenv("GOTMPDIR", altTmp)

	// A registry file inside the GOTMPDIR should be treated as a temp-local
	// registry, which relaxes the tempdir-path guard in Upsert.
	regPath := filepath.Join(altTmp, "projects.json")
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// A test project dir inside altTmp/TestXxx/001 should be ACCEPTED by a
	// temp-local registry (both the registry and the project are temp).
	testProjDir := filepath.Join(altTmp, "TestRegistryLocal123", "001")
	if err := os.MkdirAll(filepath.Join(testProjDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg.Upsert(testProjDir, "local-test", "")
	if n := len(reg.List()); n != 1 {
		t.Errorf("temp-local registry should accept tempdir project; got %d entries, want 1", n)
	}
}
