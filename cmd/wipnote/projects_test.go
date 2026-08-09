package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/registry"
)

// makeProjectWithItems creates a tmpdir "project" whose .wipnote/ holds the
// given number of canonical feature, bug, and spike HTML files, so the ITEMS
// column has something real to count.
//
// It replaces makeProjectDBWithSchema, which hand-created a `features` table in
// a file-backed SQLite DB at WIPNOTE_DB_PATH. projects.go now calls
// openDB(hgDir), which builds a private in-memory projection hydrated from the
// canonical artifacts, so a seeded file DB is a database nothing opens and
// countItems returned "-" (feat-fc3cc9e0).
func makeProjectWithItems(t *testing.T, numFeatures, numBugs, numSpikes int) string {
	t.Helper()
	tmp := t.TempDir()
	hgDir := filepath.Join(tmp, ".wipnote")
	// .git so the project passes the looksLikeRealProject check.
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(sub, prefix, nodeType string, n int) {
		dir := filepath.Join(hgDir, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("%s-%08x", prefix, i+1)
			html := `<!DOCTYPE html><html><body><article id="` + id +
				`" data-type="` + nodeType + `" data-status="todo" data-priority="medium">` +
				`<h1>` + nodeType + ` ` + id + `</h1></article></body></html>`
			if err := os.WriteFile(filepath.Join(dir, id+".html"), []byte(html), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("features", "feat", "feature", numFeatures)
	write("bugs", "bug", "bug", numBugs)
	write("spikes", "spk", "spike", numSpikes)
	return tmp
}

func withRegistryAtAndStale(t *testing.T, entries []registry.Entry) string {
	t.Helper()
	tmpHome := t.TempDir()
	regPath := filepath.Join(tmpHome, "projects.json")

	// Manually fill in the metadata Upsert would normally populate.
	for i := range entries {
		if entries[i].LastSeen == "" {
			entries[i].LastSeen = time.Now().UTC().Format(time.RFC3339)
		}
		if entries[i].ID == "" {
			entries[i].ID = registry.ComputeID(entries[i].ProjectDir)
		}
	}

	if err := registry.WriteEntriesForTest(regPath, entries); err != nil {
		t.Fatal(err)
	}

	orig := defaultRegistryPath
	defaultRegistryPath = func() string { return regPath }
	t.Cleanup(func() { defaultRegistryPath = orig })
	return regPath
}

// TestProjectsList_Output verifies that `projects list` prints one row per
// registry entry with correct STATUS and ITEMS columns.
func TestProjectsList_Output(t *testing.T) {
	realProject := makeProjectWithItems(t, 3, 2, 1)
	staleProjectDir := filepath.Join(t.TempDir(), "stale-project")
	if err := os.MkdirAll(filepath.Join(staleProjectDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staleProjectDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Remove .wipnote to make it stale
	if err := os.RemoveAll(filepath.Join(staleProjectDir, ".wipnote")); err != nil {
		t.Fatal(err)
	}

	withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: realProject, Name: "real"},
		{ProjectDir: staleProjectDir, Name: "stale"},
	})

	cmd := projectsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "real") {
		t.Errorf("expected 'real' in output, got: %s", out)
	}
	if !strings.Contains(out, "stale") {
		t.Errorf("expected 'stale' in output, got: %s", out)
	}
	if !strings.Contains(out, "exists") {
		t.Errorf("expected STATUS=exists for real project, got: %s", out)
	}
	if !strings.Contains(out, "missing") {
		t.Errorf("expected STATUS=missing for stale project, got: %s", out)
	}
	if !strings.Contains(out, "3f 2b 1s") {
		t.Errorf("expected ITEMS '3f 2b 1s' for real project, got: %s", out)
	}
}

// TestProjectsPrune_RemovesAndSaves verifies prune removes stale entries
// and persists the result.
func TestProjectsPrune_RemovesAndSaves(t *testing.T) {
	realProject := makeProjectWithItems(t, 0, 0, 0)
	staleProjectDir := filepath.Join(t.TempDir(), "stale-project")
	if err := os.MkdirAll(filepath.Join(staleProjectDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staleProjectDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Remove .wipnote to make it stale
	if err := os.RemoveAll(filepath.Join(staleProjectDir, ".wipnote")); err != nil {
		t.Fatal(err)
	}

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: realProject, Name: "real"},
		{ProjectDir: staleProjectDir, Name: "stale"},
	})

	cmd := projectsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Reload the registry and check the stale project is gone.
	reloaded, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	entries := reloaded.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after prune, got %d: %+v", len(entries), entries)
	}
	if entries[0].ProjectDir != realProject {
		t.Errorf("wrong entry remaining: %s", entries[0].ProjectDir)
	}
	out := buf.String()
	if !strings.Contains(out, "pruned:") {
		t.Errorf("expected 'pruned:' in output, got: %s", out)
	}
	if !strings.Contains(out, "pruned 1 stale projects, kept 1") {
		t.Errorf("expected summary line in output, got: %s", out)
	}
}

// TestProjectsList_CreatesNoSQLiteArtifacts replaces
// TestProjectsList_NoMigrations (feat-fc3cc9e0).
//
// That test opened a foreign project's file-backed DB and asserted `projects
// list` added no tables to it — a guard against running migrations against
// someone else's database. `projects list` no longer opens a project DB at all;
// it builds a private in-memory projection per project. The old assertion
// cannot fail any more, so it is replaced by the stronger property the cutover
// actually promises: walking a foreign project must leave no SQLite artifact
// behind in it, not even a file.
func TestProjectsList_CreatesNoSQLiteArtifacts(t *testing.T) {
	realProject := makeProjectWithItems(t, 1, 1, 1)
	withRegistryAtAndStale(t, []registry.Entry{{ProjectDir: realProject, Name: "real"}})

	cmd := projectsCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	assertNoSQLiteArtifacts(t, realProject)
}


// TestPruneSince_3d_RemovesOlder verifies that --since 3d removes entries older
// than 3 days while keeping recent ones.
func TestPruneSince_3d_RemovesOlder(t *testing.T) {
	old := time.Now().Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)

	// Create actual projects with .wipnote directories
	tmpHome := t.TempDir()
	oldProj := filepath.Join(tmpHome, "old-project")
	recentProj := filepath.Join(tmpHome, "recent-project")
	if err := os.MkdirAll(filepath.Join(oldProj, ".wipnote"), 0755); err != nil {
		t.Fatalf("create old project: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(recentProj, ".wipnote"), 0755); err != nil {
		t.Fatalf("create recent project: %v", err)
	}

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: oldProj, Name: "old", LastSeen: old},
		{ProjectDir: recentProj, Name: "recent", LastSeen: recent},
	})

	cmd := projectsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--since", "3d"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	reloaded, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	entries := reloaded.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after --since prune, got %d: %+v", len(entries), entries)
	}
	if entries[0].Name != "recent" {
		t.Errorf("wrong entry kept: %q", entries[0].Name)
	}
	out := buf.String()
	if !strings.Contains(out, "pruned 1") {
		t.Errorf("expected 'pruned 1' in output, got: %s", out)
	}
}

// TestPruneTempdirOnly_RemovesTestPaths verifies --tempdir-only removes only
// entries that match Go test tempdir naming pattern.
func TestPruneTempdirOnly_RemovesTestPaths(t *testing.T) {
	// Build a real test-tempdir path so ShouldSkipRegistration returns true.
	base := os.TempDir()
	testPath := filepath.Join(base, "TestPruneTarget999")

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: testPath, Name: "test-pollution"},
		{ProjectDir: "/workspaces/wipnote", Name: "real"},
	})

	cmd := projectsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--tempdir-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	reloaded, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	entries := reloaded.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after --tempdir-only prune, got %d: %+v", len(entries), entries)
	}
	if entries[0].Name != "real" {
		t.Errorf("wrong entry kept: %q", entries[0].Name)
	}
}

// TestPruneDryRun_DoesNotWrite verifies --dry-run prints what would be removed
// without mutating the registry on disk.
func TestPruneDryRun_DoesNotWrite(t *testing.T) {
	old := time.Now().Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)

	// Create a project with .wipnote directory
	tmpHome := t.TempDir()
	oldProj := filepath.Join(tmpHome, "old-dry-project")
	if err := os.MkdirAll(filepath.Join(oldProj, ".wipnote"), 0755); err != nil {
		t.Fatalf("create project: %v", err)
	}

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: oldProj, Name: "old", LastSeen: old},
	})

	cmd := projectsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--since", "3d", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Registry on disk should be unchanged.
	reloaded, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	entries := reloaded.List()
	if len(entries) != 1 {
		t.Fatalf("dry-run must not write: expected 1 entry on disk, got %d", len(entries))
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected 'dry-run' in output, got: %s", out)
	}
	if !strings.Contains(out, "would prune") {
		t.Errorf("expected 'would prune' in output, got: %s", out)
	}
}

// TestPruneTempdirEntries_HonorsEnvVar is a regression test for Finding 1:
// verifies that PruneTempdirEntries does NOT remove non-test entries even
// when WIPNOTE_SKIP_REGISTER=1 is set. The env-var opt-out should not leak
// from registration semantics into pruning semantics.
func TestPruneTempdirEntries_HonorsEnvVar(t *testing.T) {
	// Create a real project outside tempdir
	realProj := filepath.Join(t.TempDir(), "real-project")
	if err := os.MkdirAll(filepath.Join(realProj, ".wipnote"), 0755); err != nil {
		t.Fatalf("create real project: %v", err)
	}

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: realProj, Name: "real-project"},
	})

	// Set the env var that should only affect registration, not pruning
	oldEnv := os.Getenv("WIPNOTE_SKIP_REGISTER")
	defer func() {
		if oldEnv == "" {
			os.Unsetenv("WIPNOTE_SKIP_REGISTER")
		} else {
			os.Setenv("WIPNOTE_SKIP_REGISTER", oldEnv)
		}
	}()
	os.Setenv("WIPNOTE_SKIP_REGISTER", "1")

	cmd := projectsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--tempdir-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// The real project should still be in the registry after --tempdir-only prune
	// because it's not a test temp dir, even though WIPNOTE_SKIP_REGISTER=1
	reloaded, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	entries := reloaded.List()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after prune with WIPNOTE_SKIP_REGISTER=1, got %d: %+v", len(entries), entries)
	}
	if entries[0].Name != "real-project" {
		t.Errorf("wrong entry: expected 'real-project', got %q", entries[0].Name)
	}
}
