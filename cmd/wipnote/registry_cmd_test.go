package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/registry"
)

// TestRegistryPrune_DefaultIsDryRun verifies that `wipnote registry prune`
// without --force is a dry-run: it reports what would be removed but writes nothing.
func TestRegistryPrune_DefaultIsDryRun(t *testing.T) {
	// Create a stale project dir (directory exists but has no .wipnote).
	staleDir := t.TempDir()
	staleProjectDir := filepath.Join(staleDir, "stale-project")

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: staleProjectDir, Name: "stale"},
	})

	cmd := registryCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("registry prune: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "would prune") {
		t.Errorf("expected dry-run 'would prune' output, got: %q", out)
	}

	// Registry should still have 1 entry (dry-run does not write).
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := len(reg.List()); n != 1 {
		t.Errorf("dry-run should not modify registry; got %d entries, want 1", n)
	}
}

// TestRegistryPrune_ForceRemovesStale verifies that `wipnote registry prune --force`
// removes entries whose .wipnote directory no longer exists.
func TestRegistryPrune_ForceRemovesStale(t *testing.T) {
	// Create a live project outside temp roots so it is not treated as a test tempdir.
	liveProject := makeSafeProjDir(t)
	if err := os.MkdirAll(filepath.Join(liveProject, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a stale project (directory doesn't exist at all).
	staleProjectDir := filepath.Join(t.TempDir(), "gone-project")

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: liveProject, Name: "live"},
		{ProjectDir: staleProjectDir, Name: "gone"},
	})

	cmd := registryCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("registry prune --force: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "pruned") {
		t.Errorf("expected prune output, got: %q", out)
	}

	// Reload and verify only live project remains.
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load after prune: %v", err)
	}
	entries := reg.List()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after --force prune, got %d: %+v", len(entries), entries)
	}
	if len(entries) == 1 && entries[0].ProjectDir != liveProject {
		t.Errorf("remaining entry is %q, want %q", entries[0].ProjectDir, liveProject)
	}
}

// TestRegistryPrune_ForcePreservesValidProject verifies that --force never
// removes an entry whose .wipnote directory currently exists.
func TestRegistryPrune_ForcePreservesValidProject(t *testing.T) {
	// Use makeSafeProjDir so the project dir is not inside a temp root.
	validProject := makeSafeProjDir(t)
	if err := os.MkdirAll(filepath.Join(validProject, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: validProject, Name: "valid"},
	})

	cmd := registryCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("registry prune --force: %v", err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := len(reg.List()); n != 1 {
		t.Errorf("valid project must not be pruned; got %d entries, want 1", n)
	}
}

// TestRegistryPrune_TempdirEntries verifies that tempdir entries matching Go's
// t.TempDir() naming convention are pruned by --force.
func TestRegistryPrune_TempdirEntries(t *testing.T) {
	// Build a real test-tempdir path so IsGoTestTempDirPath returns true.
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		base = os.TempDir()
	}
	testTempProjDir := filepath.Join(base, "TestFakeProj9876543", "001")
	if err := os.MkdirAll(filepath.Join(testTempProjDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Join(base, "TestFakeProj9876543"))

	// A valid non-tempdir project (use makeSafeProjDir so it's not a Test* path).
	validProject := makeSafeProjDir(t)
	if err := os.MkdirAll(filepath.Join(validProject, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: testTempProjDir, Name: "temptest"},
		{ProjectDir: validProject, Name: "valid"},
	})

	cmd := registryCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("registry prune --force: %v", err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := reg.List()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (valid) after prune, got %d: %+v", len(entries), entries)
	}
	if len(entries) == 1 && entries[0].Name != "valid" {
		t.Errorf("remaining entry is %q, want %q", entries[0].Name, "valid")
	}
}

// TestRegistryPrune_SinceFlag verifies --since prunes stale-by-time entries.
func TestRegistryPrune_SinceFlag(t *testing.T) {
	// Use makeSafeProjDir so projects are not inside a temp root.
	oldProject := makeSafeProjDir(t)
	if err := os.MkdirAll(filepath.Join(oldProject, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	recentProject := makeSafeProjDir(t)
	if err := os.MkdirAll(filepath.Join(recentProject, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: oldProject, Name: "old", LastSeen: old},
		{ProjectDir: recentProject, Name: "recent", LastSeen: recent},
	})

	cmd := registryCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--since", "48h", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("registry prune --since 48h --force: %v", err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := reg.List()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (recent) after --since prune, got %d: %+v", len(entries), entries)
	}
	if len(entries) == 1 && entries[0].Name != "recent" {
		t.Errorf("remaining entry is %q, want %q", entries[0].Name, "recent")
	}
}

// TestRegistryPrune_GOTMPDIRPaths verifies that paths inside GOTMPDIR matching
// Go's Test* naming are pruned even when GOTMPDIR differs from os.TempDir().
func TestRegistryPrune_GOTMPDIRPaths(t *testing.T) {
	altTmp := t.TempDir()
	t.Setenv("GOTMPDIR", altTmp)

	// Create a project inside the synthetic GOTMPDIR with Test* naming.
	testProjDir := filepath.Join(altTmp, "TestGotmpdir1234", "001")
	if err := os.MkdirAll(filepath.Join(testProjDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A valid project outside the temp area.
	validProject := makeSafeProjDir(t)
	if err := os.MkdirAll(filepath.Join(validProject, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	regPath := withRegistryAtAndStale(t, []registry.Entry{
		{ProjectDir: testProjDir, Name: "gotmpdir-test"},
		{ProjectDir: validProject, Name: "valid"},
	})

	cmd := registryCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"prune", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("registry prune --force: %v", err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := reg.List()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (valid) after GOTMPDIR prune, got %d: %+v", len(entries), entries)
	}
	if len(entries) == 1 && entries[0].Name != "valid" {
		t.Errorf("remaining entry is %q, want %q", entries[0].Name, "valid")
	}
}

// makeSafeProjDir creates a temp directory outside the standard temp roots so
// it is not treated as a test tempdir by IsGoTestTempDirPath. It creates a dir
// relative to the test's working directory.
func makeSafeProjDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(wd, ".safe-proj-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
