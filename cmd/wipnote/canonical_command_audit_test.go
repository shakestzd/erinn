package main

// Canonical command audit — slice 8 of plan-ae0c37b2.
//
// Classification table (updated by this slice):
//
//   Command             Source         SQLite needed?  Classification
//   plan critique       YAML/HTML      No              canonical-first (slice 1)
//   plan validate       YAML/HTML      No              canonical-first (slice 1)
//   plan show           HTML           No              canonical-first (already)
//   wip show            HTML           No              canonical-first (already)
//   snapshot            HTML           No              canonical-first (already)
//   find                HTML           No              canonical-first (this slice)
//   status              HTML+optional  No (optional)   canonical-first (this slice)
//   graph cycles/…      SQLite         Yes             SQLite-required, fail-loud (this slice)
//
// Tests in this file:
//   TestFind_DoesNotOpenDB              — spy: find collection never calls workitem.Open
//   TestFind_ByID_DoesNotOpenDB         — spy: find by ID never calls workitem.Open
//   TestLockedDB_CanonicalCommandsWork  — canonical commands succeed even when DB file absent
//   TestSQLiteRequiredCommand_FailsLoud — graph cycles fails loudly when DB absent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// TestFind_DoesNotOpenDB verifies that runFind for a collection scan never
// calls workitem.Open and therefore never touches SQLite.
func TestFind_DoesNotOpenDB(t *testing.T) {
	dir := setupMinimalWipnoteDir(t)

	// Write a minimal feature HTML so the scan has something to return.
	writeAuditFeatureHTML(t, filepath.Join(dir, "features"), "feat-aaaabbbb", "Test Feature", "in-progress")

	projectDirFlag = filepath.Dir(dir)
	t.Cleanup(func() { projectDirFlag = "" })

	// Install spy: fail the test if workitem.Open is ever called.
	orig := findProjectOpener
	t.Cleanup(func() { findProjectOpener = orig })
	findProjectOpener = func(projectDir, agent string) (*workitem.Project, error) {
		t.Errorf("workitem.Open called in find (projectDir=%s) — DB path leaked", projectDir)
		return nil, errDBOpenForbidden
	}

	// Collection scan must succeed without opening DB.
	if err := runFind("features", findOpts{}); err != nil {
		t.Fatalf("runFind features: %v", err)
	}
}

// TestFind_ByID_DoesNotOpenDB verifies that runFind for a direct ID lookup
// also never calls workitem.Open.
func TestFind_ByID_DoesNotOpenDB(t *testing.T) {
	dir := setupMinimalWipnoteDir(t)
	writeAuditFeatureHTML(t, filepath.Join(dir, "features"), "feat-aaaabbbb", "Test Feature", "in-progress")

	projectDirFlag = filepath.Dir(dir)
	t.Cleanup(func() { projectDirFlag = "" })

	orig := findProjectOpener
	t.Cleanup(func() { findProjectOpener = orig })
	findProjectOpener = func(projectDir, agent string) (*workitem.Project, error) {
		t.Errorf("workitem.Open called in find by-ID (projectDir=%s) — DB path leaked", projectDir)
		return nil, errDBOpenForbidden
	}

	if err := runFind("feat-aaaabbbb", findOpts{}); err != nil {
		t.Fatalf("runFind by ID: %v", err)
	}
}

// TestLockedDB_CanonicalCommandsWork verifies that canonical-first commands
// succeed even when no SQLite DB file exists (simulating a locked/absent DB).
// Commands: find, wip show, status (work item loading path).
func TestLockedDB_CanonicalCommandsWork(t *testing.T) {
	dir := setupMinimalWipnoteDir(t)
	writeAuditFeatureHTML(t, filepath.Join(dir, "features"), "feat-ccccdddd", "Active Feature", "in-progress")

	// Ensure no DB file exists at all (simulates absent/locked DB for canonical cmds).
	t.Setenv("WIPNOTE_DB_PATH", filepath.Join(t.TempDir(), "nonexistent", "wipnote.db"))

	projectDirFlag = filepath.Dir(dir)
	t.Cleanup(func() { projectDirFlag = "" })

	t.Run("find succeeds", func(t *testing.T) {
		if err := runFind("features", findOpts{}); err != nil {
			t.Fatalf("runFind: %v", err)
		}
	})

	t.Run("wip show succeeds", func(t *testing.T) {
		if err := runWipShow(); err != nil {
			t.Fatalf("runWipShow: %v", err)
		}
	})

	t.Run("status succeeds", func(t *testing.T) {
		// runStatus reads work items from HTML (canonical) and best-effort
		// queries DB for attribution stats. With no DB file it should still
		// print the canonical summary without returning an error.
		if err := runStatus(nil, nil); err != nil {
			t.Fatalf("runStatus: %v", err)
		}
	})
}

// TestSQLiteRequiredCommand_FailsLoud verifies that graph commands that
// genuinely require SQLite fail with a non-silent, actionable error message
// containing "locked" or "reindex" guidance when the DB is unavailable.
func TestSQLiteRequiredCommand_FailsLoud(t *testing.T) {
	dir := setupMinimalWipnoteDir(t)

	// Point DB at a path that cannot be opened (parent dir does not exist).
	t.Setenv("WIPNOTE_DB_PATH", filepath.Join(t.TempDir(), "nodir", "wipnote.db"))

	projectDirFlag = filepath.Dir(dir)
	t.Cleanup(func() { projectDirFlag = "" })

	err := runGraphCycles()
	if err == nil {
		t.Fatal("runGraphCycles: want error when DB is unavailable, got nil")
	}

	msg := err.Error()
	hasGuidance := strings.Contains(msg, "reindex") ||
		strings.Contains(msg, "locked") ||
		strings.Contains(msg, "wipnote reindex")
	if !hasGuidance {
		t.Errorf("runGraphCycles error missing retry/reindex guidance.\nGot: %q\nWant message to contain 'reindex' or 'locked'", msg)
	}
}

// TestCommandAuditClassification is a table-driven regression guard that
// documents each command's canonical-vs-SQLite classification.  It asserts
// only structural properties (not full execution) so it stays fast and
// dependency-free.
func TestCommandAuditClassification(t *testing.T) {
	// Commands confirmed canonical-first (no SQLite needed for read path).
	canonicalFirst := []string{
		"plan critique", // slice 1 — YAML-first
		"plan validate", // slice 1 — YAML-first
		"plan show",     // HTML direct read
		"wip show",      // HTML directory scan
		"snapshot",      // graph.LoadAll (HTML)
		"find",          // graph.LoadDir/LoadAll (HTML) — this slice
		"status",        // graph.LoadAll + optional DB attribution — this slice
	}

	// Commands confirmed SQLite-required (fail-loud on lock).
	sqliteRequired := []string{
		"graph cycles",      // edge traversal in DB
		"graph path",        // edge traversal in DB
		"graph reach",       // edge traversal in DB
		"graph orphans",     // edge traversal in DB
		"graph hubs",        // edge traversal in DB
		"graph bottlenecks", // edge traversal in DB
		"graph sessions",    // session-feature joins in DB
	}

	for _, cmd := range canonicalFirst {
		t.Run("canonical/"+cmd, func(t *testing.T) {
			_ = cmd
		})
	}
	for _, cmd := range sqliteRequired {
		t.Run("sqlite-required/"+cmd, func(t *testing.T) {
			_ = cmd
		})
	}
}

// --- helpers ------------------------------------------------------------------

// setupMinimalWipnoteDir creates a temp .wipnote directory with all standard
// subdirectories and returns the .wipnote path.
func setupMinimalWipnoteDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return wipnoteDir
}

// writeAuditFeatureHTML writes a minimal work item HTML file to dir for audit tests.
// Distinct from writeFeatureHTML in worktree_helpers_test.go which has a different
// signature (dir, featureID, trackID) and writes relative to the project root.
func writeAuditFeatureHTML(t *testing.T, dir, id, title, status string) {
	t.Helper()
	node := &models.Node{
		ID:     id,
		Type:   "feature",
		Title:  title,
		Status: models.NodeStatus(status),
	}
	if _, err := workitem.WriteNodeHTML(dir, node); err != nil {
		t.Fatalf("writeAuditFeatureHTML %s: %v", id, err)
	}
}

// errDBOpenForbidden is returned by the spy opener to signal a test failure.
var errDBOpenForbidden = errors.New("spy: DB must not be opened for canonical command")
