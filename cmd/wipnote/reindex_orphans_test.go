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
	"github.com/shakestzd/wipnote/core/htmlparse"
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

// writeOrphanTestItem writes a minimal canonical work-item HTML file into the
// right collection subdir. When affectedFiles is non-empty it is emitted as the
// data-affected_files attribute, which is how the node-property wire format
// encodes an attribute-safe string key (core/htmlparse/node_props.go).
func writeOrphanTestItem(t *testing.T, wipnoteDir, id, subdir, affectedFiles string) {
	t.Helper()
	dir := filepath.Join(wipnoteDir, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	nodeType := strings.TrimSuffix(subdir, "s")
	attrs := ""
	if affectedFiles != "" {
		attrs = ` data-affected_files="` + affectedFiles + `"`
	}
	html := `<!DOCTYPE html><html><body><article id="` + id +
		`" data-type="` + nodeType + `" data-status="todo" data-priority="medium"` + attrs +
		`><h1>Test ` + id + `</h1></article></body></html>`
	if err := os.WriteFile(filepath.Join(dir, id+".html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
}

// TestFindOrphanWorkItems_ReturnsItemsWithNoAffectedFiles pins the orphan set to
// the canonical artifacts: an item is an orphan when its HTML declares no
// affected_files, regardless of any derived index.
func TestFindOrphanWorkItems_ReturnsItemsWithNoAffectedFiles(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	writeOrphanTestItem(t, wipnoteDir, "feat-aaaaaaaa", "features", "")
	writeOrphanTestItem(t, wipnoteDir, "feat-bbbbbbbb", "features", "some/file.go")
	writeOrphanTestItem(t, wipnoteDir, "bug-cccccccc", "bugs", "")

	orphans, err := findOrphanWorkItems(wipnoteDir)
	if err != nil {
		t.Fatalf("findOrphanWorkItems: %v", err)
	}
	var ids []string
	for _, o := range orphans {
		ids = append(ids, o.id)
	}
	want := []string{"bug-cccccccc", "feat-aaaaaaaa"}
	if len(ids) != len(want) {
		t.Fatalf("orphans = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("orphans = %v, want %v (sorted)", ids, want)
		}
	}
}

// TestFindOrphanWorkItems_EmptyWhenAllAttributed is the negative case: nothing
// is an orphan once every item carries attribution.
func TestFindOrphanWorkItems_EmptyWhenAllAttributed(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	writeOrphanTestItem(t, wipnoteDir, "feat-cccccccc", "features", "main.go")
	writeOrphanTestItem(t, wipnoteDir, "spk-dddddddd", "spikes", "notes.md")

	orphans, err := findOrphanWorkItems(wipnoteDir)
	if err != nil {
		t.Fatalf("findOrphanWorkItems: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans, got %v", orphans)
	}
}

// TestDistinctFilesFromMatches verifies the flattening: one entry per file, not
// one per (commit, file) pair, sorted for a stable property value.
func TestDistinctFilesFromMatches(t *testing.T) {
	matches := []commitMatch{
		{hash: "aaa", files: []fileStats{{path: "b.go"}, {path: "a.go"}}},
		{hash: "bbb", files: []fileStats{{path: "a.go"}, {path: "c.go"}, {path: ""}}},
	}
	got := distinctFilesFromMatches(matches)
	want := []string{"a.go", "b.go", "c.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("distinctFilesFromMatches = %v, want %v", got, want)
	}
}

// TestBackfillOrphansWritesAffectedFilesToHTML is the end-to-end proof that the
// backfill lands somewhere durable: it runs the real command against a real git
// repo and then re-reads the work item off disk. This is what the old
// feature_files insert could not do — its rows never outlived the process.
func TestBackfillOrphansWritesAffectedFilesToHTML(t *testing.T) {
	repoDir, _ := setupOrphanTestRepo(t)
	wipnoteDir := filepath.Join(repoDir, ".wipnote")
	writeOrphanTestItem(t, wipnoteDir, "feat-aaaaaaaa", "features", "")

	isolateProjectDir(t, repoDir)

	cmd := reindexBackfillOrphansCmd()
	if err := cmd.Flags().Set("write", "true"); err != nil {
		t.Fatalf("set --write: %v", err)
	}
	withWorkingDir(t, repoDir, func() {
		if err := runReindexBackfillOrphans(cmd, nil); err != nil {
			t.Fatalf("runReindexBackfillOrphans: %v", err)
		}
	})

	node, err := htmlparse.ParseFile(filepath.Join(wipnoteDir, "features", "feat-aaaaaaaa.html"))
	if err != nil {
		t.Fatalf("re-read work item: %v", err)
	}
	got := nodeAffectedFiles(node)
	for _, want := range []string{"alpha.go", "beta/beta.go", "gamma.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("affected_files = %q, missing %q", got, want)
		}
	}

	// Second pass: the item is no longer an orphan, so it is not revisited.
	orphans, err := findOrphanWorkItems(wipnoteDir)
	if err != nil {
		t.Fatalf("findOrphanWorkItems: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected the backfilled item to stop being an orphan, got %v", orphans)
	}
}

// isolateProjectDir pins every project-dir resolution inside the test to dir.
//
// This is not optional hygiene. paths.ResolveProjectDir consults
// WIPNOTE_PROJECT_DIR (priority 2) and CLAUDE_PROJECT_DIR (priority 3) BEFORE
// it ever looks at the working directory (priority 5), and both are set in any
// shell running under a wipnote launcher — including the one running `go test`.
// A test that calls a command entry point without pinning them therefore runs
// against the developer's real .wipnote/, and a command that writes will write
// there. Chdir alone does not protect you.
func isolateProjectDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("WIPNOTE_PROJECT_DIR", dir)
	t.Setenv("CLAUDE_PROJECT_DIR", dir)
}
