package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// withRegistryAt points the package-level defaultRegistryPath at a tmpdir
// path and seeds it with the given entries. Returns the registry file path.
func withRegistryAt(t *testing.T, entries []registry.Entry) string {
	t.Helper()
	tmpHome := t.TempDir()
	regPath := filepath.Join(tmpHome, "projects.json")
	reg, _ := registry.Load(regPath)
	for _, e := range entries {
		reg.Upsert(e.ProjectDir, e.Name, e.GitRemoteURL)
	}
	if err := reg.Save(); err != nil {
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
	fakeProject := filepath.Join(t.TempDir(), "does-not-exist")

	withRegistryAt(t, []registry.Entry{
		{ProjectDir: realProject, Name: "real"},
		{ProjectDir: fakeProject, Name: "fake"},
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
	if !strings.Contains(out, "fake") {
		t.Errorf("expected 'fake' in output, got: %s", out)
	}
	if !strings.Contains(out, "exists") {
		t.Errorf("expected STATUS=exists for real project, got: %s", out)
	}
	if !strings.Contains(out, "missing") {
		t.Errorf("expected STATUS=missing for fake project, got: %s", out)
	}
	if !strings.Contains(out, "3f 2b 1s") {
		t.Errorf("expected ITEMS '3f 2b 1s' for real project, got: %s", out)
	}
}

// TestProjectsPrune_RemovesAndSaves verifies prune removes stale entries
// and persists the result.
func TestProjectsPrune_RemovesAndSaves(t *testing.T) {
	realProject := makeProjectDBWithSchema(t, 0, 0, 0)
	fakeProject := filepath.Join(t.TempDir(), "does-not-exist")

	regPath := withRegistryAt(t, []registry.Entry{
		{ProjectDir: realProject, Name: "real"},
		{ProjectDir: fakeProject, Name: "fake"},
	})

	cmd := projectsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Reload the registry and check the fake project is gone.
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
	if !strings.Contains(buf.String(), "pruned:") {
		t.Errorf("expected 'pruned:' in output, got: %s", buf.String())
	}
}

// TestProjectsList_NoMigrations ensures `projects list` does not create any
// new tables in foreign project DBs — it must use registry.OpenReadOnly.
func TestProjectsList_NoMigrations(t *testing.T) {
	realProject := makeProjectDBWithSchema(t, 1, 1, 1)
	withRegistryAt(t, []registry.Entry{{ProjectDir: realProject, Name: "real"}})

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
