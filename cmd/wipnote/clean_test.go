package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// sampleArchMD is a minimal valid arch-card markdown file for testing.
const sampleArchMD = `---
name: test-invariant
kind: invariant
created_by: test-agent
body: "Test body for clean_test."
---
Test body for clean_test.
`

// setupCleanTestDir creates a temp project with a .wipnote directory,
// sets WIPNOTE_PROJECT_DIR so findWipnoteDir resolves to it, and populates
// it with:
//   - .wipnote/refs.json        (known-dead artifact)
//   - .wipnote/arch/card.md     (arch card to migrate)
//   - .wipnote/agents.json      (unknown orphan — must never be deleted)
func setupCleanTestDir(t *testing.T) (projectDir, wipnoteDir string) {
	t.Helper()
	projectDir = t.TempDir()
	wipnoteDir = filepath.Join(projectDir, ".wipnote")
	archDir := filepath.Join(wipnoteDir, "arch")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("mkdir arch: %v", err)
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	writeFile := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	writeFile(filepath.Join(wipnoteDir, "refs.json"), `{}`)
	writeFile(filepath.Join(archDir, "card.md"), sampleArchMD)
	writeFile(filepath.Join(wipnoteDir, "agents.json"), `{}`)

	return projectDir, wipnoteDir
}

// exists reports whether the file at path exists.
func exists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// TestClean_DryRun checks that dry-run reports without mutating anything.
func TestClean_DryRun(t *testing.T) {
	_, wipnoteDir := setupCleanTestDir(t)

	refsJSON := filepath.Join(wipnoteDir, "refs.json")
	archMD := filepath.Join(wipnoteDir, "arch", "card.md")
	agentsJSON := filepath.Join(wipnoteDir, "agents.json")

	// Dry-run: nothing should change.
	if err := runClean(false /* apply=false → dry-run */); err != nil {
		t.Fatalf("runClean dry-run: %v", err)
	}

	// refs.json must still exist (would-remove, not actually removed).
	if !exists(refsJSON) {
		t.Error("dry-run: refs.json was deleted (should not have been)")
	}

	// arch/card.md must still exist (would-migrate, not actually migrated).
	if !exists(archMD) {
		t.Error("dry-run: arch/card.md was deleted (should not have been)")
	}

	// agents.json must still exist (report-only, never touched).
	if !exists(agentsJSON) {
		t.Error("dry-run: agents.json was deleted (should not have been)")
	}
}

// TestClean_Apply checks that --apply removes refs.json and migrates the arch
// card but leaves agents.json untouched.
func TestClean_Apply(t *testing.T) {
	_, wipnoteDir := setupCleanTestDir(t)

	refsJSON := filepath.Join(wipnoteDir, "refs.json")
	archMD := filepath.Join(wipnoteDir, "arch", "card.md")
	agentsJSON := filepath.Join(wipnoteDir, "agents.json")
	ledger := filepath.Join(wipnoteDir, "architecture.html")

	if err := runClean(true /* apply */); err != nil {
		t.Fatalf("runClean apply: %v", err)
	}

	// refs.json must be gone.
	if exists(refsJSON) {
		t.Error("apply: refs.json was not deleted")
	}

	// arch/card.md must be gone (migrated into ledger).
	if exists(archMD) {
		t.Error("apply: arch/card.md still exists after migration")
	}

	// architecture.html must now exist (ledger was written).
	if !exists(ledger) {
		t.Error("apply: architecture.html was not created")
	}

	// agents.json must still exist (report-only).
	if !exists(agentsJSON) {
		t.Error("apply: agents.json was deleted (should never be deleted)")
	}
}

// TestClean_Idempotent checks that a second run (after apply) is a no-op.
func TestClean_Idempotent(t *testing.T) {
	_, wipnoteDir := setupCleanTestDir(t)

	// First apply.
	if err := runClean(true); err != nil {
		t.Fatalf("first runClean apply: %v", err)
	}

	refsJSON := filepath.Join(wipnoteDir, "refs.json")
	agentsJSON := filepath.Join(wipnoteDir, "agents.json")
	archMD := filepath.Join(wipnoteDir, "arch", "card.md")
	ledger := filepath.Join(wipnoteDir, "architecture.html")

	// Sanity: state is as expected after first apply.
	if exists(refsJSON) {
		t.Fatal("after first apply: refs.json still present")
	}
	if !exists(ledger) {
		t.Fatal("after first apply: architecture.html missing")
	}

	// Second apply — must not error and must not break existing state.
	if err := runClean(true); err != nil {
		t.Fatalf("second runClean apply: %v", err)
	}

	// arch/card.md should not re-appear.
	if exists(archMD) {
		t.Error("idempotent: arch/card.md appeared after second run")
	}

	// agents.json must still be intact.
	if !exists(agentsJSON) {
		t.Error("idempotent: agents.json was deleted")
	}
}

// TestClean_NoWipnoteDir verifies that runClean returns an error (not success)
// when the resolved .wipnote path is not a directory, rather than silently
// reporting a clean project outside any wipnote checkout.
//
// Strategy: place a regular FILE at <projectDir>/.wipnote so findWipnoteDir
// returns a path that exists but is not a directory — our stat guard must
// reject it with a clear error rather than proceeding.
func TestClean_NoWipnoteDir(t *testing.T) {
	fake := t.TempDir()
	// Write a regular file named ".wipnote" — not a directory.
	if err := os.WriteFile(filepath.Join(fake, ".wipnote"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write fake .wipnote file: %v", err)
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", fake)

	err := runClean(false)
	if err == nil {
		t.Error("expected an error when .wipnote is not a directory, got nil")
	}
}

// TestClean_ApplyUnremovableArtifactReturnsError verifies that when --apply
// fails to remove a known-dead artifact, runClean returns a non-nil error
// (in addition to printing the per-item error line).
//
// On Linux we simulate an un-removable file by making refs.json a directory
// (os.Remove returns an error for non-empty paths that are directories).
// On Windows this approach is unreliable, so we skip there.
func TestClean_ApplyUnremovableArtifactReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unremovable test not reliable on Windows")
	}

	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir wipnoteDir: %v", err)
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	// Create refs.json as a non-empty directory so os.Remove fails on it.
	// (os.Remove only removes empty dirs; a non-empty one causes ENOTEMPTY.)
	refsDir := filepath.Join(wipnoteDir, "refs.json")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatalf("mkdir refs.json-as-dir: %v", err)
	}
	// Put a file inside so it's non-empty.
	if err := os.WriteFile(filepath.Join(refsDir, "sentinel"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	err := runClean(true /* apply */)
	if err == nil {
		t.Error("expected non-nil error when a known-dead artifact cannot be removed, got nil")
	}
}
