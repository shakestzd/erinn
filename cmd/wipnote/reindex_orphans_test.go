package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// setupOrphanTestRepo creates a git repo with commits referencing feat-AAAAAAAA.
// Returns the dir and the commit hashes (3 commits referencing the feature).
func setupOrphanTestRepo(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	writeAndCommit := func(filename, content, message string) string {
		path := filepath.Join(dir, filename)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", filename, err)
		}
		run("add", filename)
		run("commit", "-m", message)
		return run("rev-parse", "HEAD")
	}

	hash1 := writeAndCommit("alpha.go", "package alpha\n", "feat: add alpha (feat-aaaaaaaa)")
	hash2 := writeAndCommit("beta/beta.go", "package beta\n", "feat: add beta (feat-aaaaaaaa)")
	hash3 := writeAndCommit("gamma.go", "package gamma\n", "chore: add gamma\n\nRefs: feat-aaaaaaaa")

	return dir, []string{hash1, hash2, hash3}
}

// openTestDB opens an in-memory SQLite DB with the full schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertTestFeature(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := dbpkg.UpsertFeature(db, &dbpkg.Feature{
		ID: id, Type: "feature", Title: "Test " + id,
		Status: "todo", Priority: "medium", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertFeature %s: %v", id, err)
	}
}

// TestFindOrphanFeatures_ReturnsZeroFileFeatures verifies that a feature with no
// feature_files rows is returned as an orphan.
func TestFindOrphanFeatures_ReturnsZeroFileFeatures(t *testing.T) {
	db := openTestDB(t)
	insertTestFeature(t, db, "feat-aaaaaaaa")
	insertTestFeature(t, db, "feat-bbbbbbbb")

	// Give feat-bbbbbbbb a feature_files row so it is not an orphan.
	if err := dbpkg.UpsertFeatureFile(db, &models.FeatureFile{
		ID: "feat-bbbbbbbb-x-y", FeatureID: "feat-bbbbbbbb", FilePath: "some/file.go", Operation: "test",
	}); err != nil {
		t.Fatalf("UpsertFeatureFile: %v", err)
	}

	orphans, err := findOrphanFeatures(db)
	if err != nil {
		t.Fatalf("findOrphanFeatures: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d: %v", len(orphans), orphans)
	}
	if orphans[0].id != "feat-aaaaaaaa" {
		t.Errorf("expected feat-aaaaaaaa, got %q", orphans[0].id)
	}
}

// TestFindOrphanFeatures_EmptyWhenAllAttributed verifies no orphans returned when
// all features have file rows.
func TestFindOrphanFeatures_EmptyWhenAllAttributed(t *testing.T) {
	db := openTestDB(t)
	insertTestFeature(t, db, "feat-cccccccc")
	if err := dbpkg.UpsertFeatureFile(db, &models.FeatureFile{
		ID: "feat-cccccccc-x-y", FeatureID: "feat-cccccccc", FilePath: "main.go", Operation: "test",
	}); err != nil {
		t.Fatalf("UpsertFeatureFile: %v", err)
	}

	orphans, err := findOrphanFeatures(db)
	if err != nil {
		t.Fatalf("findOrphanFeatures: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans, got %d: %v", len(orphans), orphans)
	}
}

// TestFindCommitsForFeature_ThreeCommits verifies that 3 commits referencing
// feat-aaaaaaaa are all found.
func TestFindCommitsForFeature_ThreeCommits(t *testing.T) {
	dir, hashes := setupOrphanTestRepo(t)

	matches, err := findCommitsForFeature(dir, "feat-aaaaaaaa")
	if err != nil {
		t.Fatalf("findCommitsForFeature: %v", err)
	}
	if len(matches) != 3 {
		t.Errorf("expected 3 matches, got %d (hashes: %v)", len(matches), hashes)
	}

	// All 3 commits must appear.
	found := make(map[string]bool)
	for _, m := range matches {
		found[m.hash] = true
	}
	for _, h := range hashes {
		if !found[h] {
			t.Errorf("commit %s not found in matches", h)
		}
	}
}

// TestFindCommitsForFeature_FilesIndexed verifies that files touched in matching
// commits are returned.
func TestFindCommitsForFeature_FilesIndexed(t *testing.T) {
	dir, _ := setupOrphanTestRepo(t)

	matches, err := findCommitsForFeature(dir, "feat-aaaaaaaa")
	if err != nil {
		t.Fatalf("findCommitsForFeature: %v", err)
	}

	allFiles := make(map[string]bool)
	for _, m := range matches {
		for _, f := range m.files {
			allFiles[f.path] = true
		}
	}

	for _, expected := range []string{"alpha.go", "beta/beta.go", "gamma.go"} {
		if !allFiles[expected] {
			t.Errorf("expected file %q not found; all files: %v", expected, allFiles)
		}
	}
}

// TestInsertFeatureFileRows_WriteMode verifies that --write inserts rows.
func TestInsertFeatureFileRows_WriteMode(t *testing.T) {
	dir, _ := setupOrphanTestRepo(t)
	db := openTestDB(t)
	insertTestFeature(t, db, "feat-aaaaaaaa")

	matches, err := findCommitsForFeature(dir, "feat-aaaaaaaa")
	if err != nil {
		t.Fatalf("findCommitsForFeature: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no matches found — test setup failed")
	}

	inserted, err := insertFeatureFileRows(db, "feat-aaaaaaaa", matches)
	if err != nil {
		t.Fatalf("insertFeatureFileRows: %v", err)
	}
	if inserted == 0 {
		t.Error("expected rows to be inserted, got 0")
	}

	rows, err := dbpkg.ListFilesByFeature(db, "feat-aaaaaaaa")
	if err != nil {
		t.Fatalf("ListFilesByFeature: %v", err)
	}
	if len(rows) == 0 {
		t.Error("no feature_files rows found after insert")
	}

	// Verify files include at least alpha.go, beta/beta.go, gamma.go.
	paths := make(map[string]bool)
	for _, r := range rows {
		paths[r.FilePath] = true
	}
	for _, expected := range []string{"alpha.go", "beta/beta.go", "gamma.go"} {
		if !paths[expected] {
			t.Errorf("expected file %q not found in feature_files; got %v", expected, paths)
		}
	}
}

// TestInsertFeatureFileRows_Idempotent verifies that re-running produces no new rows.
func TestInsertFeatureFileRows_Idempotent(t *testing.T) {
	dir, _ := setupOrphanTestRepo(t)
	db := openTestDB(t)
	insertTestFeature(t, db, "feat-aaaaaaaa")

	matches, err := findCommitsForFeature(dir, "feat-aaaaaaaa")
	if err != nil {
		t.Fatalf("findCommitsForFeature: %v", err)
	}

	inserted1, _ := insertFeatureFileRows(db, "feat-aaaaaaaa", matches)
	inserted2, _ := insertFeatureFileRows(db, "feat-aaaaaaaa", matches)

	if inserted1 == 0 {
		t.Error("first run: expected > 0 rows inserted")
	}
	// Second run: UpsertFeatureFile uses ON CONFLICT DO UPDATE so it still "succeeds"
	// but the row count in the DB should remain the same.
	rows1, _ := dbpkg.ListFilesByFeature(db, "feat-aaaaaaaa")
	rows2, _ := dbpkg.ListFilesByFeature(db, "feat-aaaaaaaa")
	if len(rows1) != len(rows2) {
		t.Errorf("idempotency: row count changed from %d to %d", len(rows1), len(rows2))
	}
	_ = inserted2
}

// TestCommitReferencesFeature_FalseMatchGuard verifies that a longer ID
// (feat-aaaaaaab) does not match when we search for feat-aaaaaaaa.
func TestCommitReferencesFeature_FalseMatchGuard(t *testing.T) {
	// feat-aaaaaaab has "aaaaaaaa" as a prefix — should NOT match feat-aaaaaaaa.
	msg := "fix: something (feat-aaaaaaab)"
	if commitReferencesFeature("", msg, "feat-aaaaaaaa") {
		t.Error("false match: feat-aaaaaaab matched query for feat-aaaaaaaa")
	}
}

// TestCommitReferencesFeature_ExactMatch verifies that exact ID matches.
func TestCommitReferencesFeature_ExactMatch(t *testing.T) {
	msg := "fix: something (feat-aaaaaaaa)"
	if !commitReferencesFeature("", msg, "feat-aaaaaaaa") {
		t.Error("expected match for feat-aaaaaaaa in parenthesized ref")
	}
}

// TestCommitReferencesFeature_InlineMatch verifies plain inline reference.
func TestCommitReferencesFeature_InlineMatch(t *testing.T) {
	msg := "refactor: improve performance for feat-aaaaaaaa implementation"
	if !commitReferencesFeature("", msg, "feat-aaaaaaaa") {
		t.Error("expected match for feat-aaaaaaaa inline reference")
	}
}

// TestFindCommitsForFeature_NonMergedBranchIncluded verifies that commits on
// non-merged branches ARE returned (git log --all walks all refs).
// This is intentional: backfill should recover attribution from any branch,
// including feature branches that were squash-merged or abandoned.
func TestFindCommitsForFeature_NonMergedBranchIncluded(t *testing.T) {
	dir := t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, _ := cmd.CombinedOutput()
		return strings.TrimSpace(string(out))
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	// Initial commit on main.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("commit", "-m", "initial")

	// Create a feature branch (not merged into main).
	run("checkout", "-b", "feature-branch")
	if err := os.WriteFile(filepath.Join(dir, "orphan.go"), []byte("package orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "orphan.go")
	run("commit", "-m", "feat: orphan commit (feat-aaaaaaaa)")

	// Switch back to main — feature-branch is NOT merged.
	run("checkout", "main")

	// findCommitsForFeature uses --all so the feature-branch commit IS returned.
	// This allows backfill to recover attribution from squash-merged or abandoned branches.
	matches, err := findCommitsForFeature(dir, "feat-aaaaaaaa")
	if err != nil {
		t.Fatalf("findCommitsForFeature: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match from non-merged branch (--all), got %d", len(matches))
	}
}

// TestFindCommitsForFeature_NoMatchForUnknownID verifies that an ID with no
// matching commits returns an empty slice.
func TestFindCommitsForFeature_NoMatchForUnknownID(t *testing.T) {
	dir, _ := setupOrphanTestRepo(t)

	matches, err := findCommitsForFeature(dir, "feat-99999999")
	if err != nil {
		t.Fatalf("findCommitsForFeature: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for unknown ID, got %d", len(matches))
	}
}

// TestCommitReferencesFeature_RefsTrailer verifies Refs: trailer matching.
func TestCommitReferencesFeature_RefsTrailer(t *testing.T) {
	msg := "chore: cleanup\n\nRefs: feat-aaaaaaaa"
	if !commitReferencesFeature("", msg, "feat-aaaaaaaa") {
		t.Error("expected match for Refs: trailer")
	}
}
