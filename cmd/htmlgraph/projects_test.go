package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/htmlgraph/internal/registry"
)

// makeProjectDBWithSchema creates a tmpdir "project" with a .htmlgraph/
// subdirectory and a SQLite DB that has a `features` table matching the
// real htmlgraph schema (type column — 'feature' | 'bug' | 'spike').
// Populates a few rows so ITEMS counts are non-zero.
func makeProjectDBWithSchema(t *testing.T, numFeatures, numBugs, numSpikes int) string {
	t.Helper()
	tmp := t.TempDir()
	hgDir := filepath.Join(tmp, ".htmlgraph")
	if err := os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create .git directory so project passes looksLikeRealProject check
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(hgDir, ".db", "htmlgraph.db")
	t.Setenv("HTMLGRAPH_DB_PATH", dbPath)

	// Use modernc.org/sqlite driver registered as "sqlite".
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE features (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(kind string, n int) {
		for i := 0; i < n; i++ {
			_, err := db.Exec("INSERT INTO features (id, type, title) VALUES (?, ?, ?)",
				kind+string(rune('a'+i)), kind, kind+" title")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	insert("feature", numFeatures)
	insert("bug", numBugs)
	insert("spike", numSpikes)
	db.Close()
	return tmp
}

// withRegistryAtAndStale sets up a registry with entries that were previously registered
// but whose .htmlgraph directories may have been deleted (stale). It writes entries directly
// to bypass Upsert's looksLikeRealProject guard, allowing the tests to verify prune behavior.
func withRegistryAtAndStale(t *testing.T, entries []registry.Entry) string {
	t.Helper()
	tmpHome := t.TempDir()
	regPath := filepath.Join(tmpHome, "projects.json")

	// Manually construct entries with valid timestamps so they can be saved
	for i := range entries {
		if entries[i].LastSeen == "" {
			entries[i].LastSeen = time.Now().UTC().Format(time.RFC3339)
		}
		if entries[i].ID == "" {
			entries[i].ID = registry.ComputeID(entries[i].ProjectDir)
		}
	}

	// Write entries directly to bypass Upsert guard
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(regPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regPath, data, 0o644); err != nil {
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
	realProject := makeProjectDBWithSchema(t, 3, 2, 1)
	staleProjectDir := filepath.Join(t.TempDir(), "stale-project")
	if err := os.MkdirAll(filepath.Join(staleProjectDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staleProjectDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Remove .htmlgraph to make it stale
	if err := os.RemoveAll(filepath.Join(staleProjectDir, ".htmlgraph")); err != nil {
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
	realProject := makeProjectDBWithSchema(t, 0, 0, 0)
	staleProjectDir := filepath.Join(t.TempDir(), "stale-project")
	if err := os.MkdirAll(filepath.Join(staleProjectDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staleProjectDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Remove .htmlgraph to make it stale
	if err := os.RemoveAll(filepath.Join(staleProjectDir, ".htmlgraph")); err != nil {
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

// TestProjectsList_NoMigrations ensures `projects list` does not create any
// new tables in foreign project DBs — it must use registry.OpenReadOnly.
func TestProjectsList_NoMigrations(t *testing.T) {
	realProject := makeProjectDBWithSchema(t, 1, 1, 1)
	withRegistryAtAndStale(t, []registry.Entry{{ProjectDir: realProject, Name: "real"}})

	// Snapshot table set before.
	dbPath := filepath.Join(realProject, ".htmlgraph", ".db", "htmlgraph.db")
	before := readTableNames(t, dbPath)

	cmd := projectsCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	// Snapshot table set after.
	after := readTableNames(t, dbPath)
	if len(before) != len(after) {
		t.Fatalf("table set changed: before=%v after=%v", before, after)
	}
}

func readTableNames(t *testing.T, dbPath string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}
