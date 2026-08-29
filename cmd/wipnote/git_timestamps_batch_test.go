package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// runGitTimestampsBatchTest runs a git command in dir, failing on error.
func runGitTimestampsBatchTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// --- batchGitFileTimestamps: equivalence with the per-file path ---

// TestBatchGitFileTimestamps_MatchesPerFile_MultiCommit verifies that, for a
// realistic set of files each touched by a different number of commits, the
// batched walk produces exactly the same (created, updated) pair per file as
// the old one-subprocess-pair-per-file gitFileTimestamps (bug-4e5816f4).
func TestBatchGitFileTimestamps_MatchesPerFile_MultiCommit(t *testing.T) {
	repoDir := setupGitRepo(t)

	_ = commitFile(t, repoDir, "features/feat-a.html", "v1")
	_ = commitFile(t, repoDir, "features/feat-a.html", "v2")
	_ = commitFile(t, repoDir, "features/feat-b.html", "v1")
	_ = commitFile(t, repoDir, "bugs/bug-c.html", "v1")
	_ = commitFile(t, repoDir, "bugs/bug-c.html", "v2")
	_ = commitFile(t, repoDir, "bugs/bug-c.html", "v3")

	paths := []string{
		filepath.Join(repoDir, "features/feat-a.html"),
		filepath.Join(repoDir, "features/feat-b.html"),
		filepath.Join(repoDir, "bugs/bug-c.html"),
	}

	batch := batchGitFileTimestamps(repoDir, paths)

	for _, p := range paths {
		wantCreated, wantUpdated, err := gitFileTimestamps(repoDir, p)
		if err != nil {
			t.Fatalf("gitFileTimestamps(%s): %v", p, err)
		}
		got, ok := batch[p]
		if !ok {
			t.Errorf("%s: missing from batch result", p)
			continue
		}
		if !got.created.Equal(wantCreated) {
			t.Errorf("%s: created batch=%v single=%v", p, got.created, wantCreated)
		}
		if !got.updated.Equal(wantUpdated) {
			t.Errorf("%s: updated batch=%v single=%v", p, got.updated, wantUpdated)
		}
	}
}

// TestBatchGitFileTimestamps_UntrackedFileOmitted verifies an untracked file
// mixed into the batch never resolves to a non-zero git timestamp — matching
// gitFileTimestamps returning zero times for an untracked file. A present
// map entry with zero values is fine (timestampsFromBatch treats that
// identically to a miss, falling back to HTML attributes via
// resolveTimestamps); what must never happen is a false non-zero timestamp.
func TestBatchGitFileTimestamps_UntrackedFileOmitted(t *testing.T) {
	repoDir := setupGitRepo(t)
	_ = commitFile(t, repoDir, "features/feat-a.html", "v1")

	untracked := filepath.Join(repoDir, "features/feat-untracked.html")
	if err := os.MkdirAll(filepath.Dir(untracked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	batch := batchGitFileTimestamps(repoDir, []string{
		filepath.Join(repoDir, "features/feat-a.html"),
		untracked,
	})

	if ts, ok := batch[untracked]; ok && (!ts.created.IsZero() || !ts.updated.IsZero()) {
		t.Errorf("untracked file should not resolve to a non-zero git timestamp, got %+v", ts)
	}
	if _, ok := batch[filepath.Join(repoDir, "features/feat-a.html")]; !ok {
		t.Error("committed file missing from batch result")
	}
}

// TestBatchGitFileTimestamps_RenamedFile_FallsBackToFollowSemantics is the
// critical equivalence case: a file that was git-mv'd to its current path
// has no "A" (added) event under its current name at all. batchGitFileTimestamps
// resolves "created" via the exact original gitFirstAdded (--follow) call for
// every file, precisely so cases like this are never at risk -- an earlier
// version of this function tried to trace renames/copies itself from a bulk
// `git log` walk and a real-corpus equivalence test caught it producing
// wrong "created" values for exactly this shape of history (this repo's own
// history has real examples, including a rename AND a git-detected
// low-similarity copy, that a cheap bulk heuristic silently miscomputed).
// Correctly tracing renames/copies requires `--find-copies-harder`, an O(n^2)
// whole-tree scan too expensive to run once per reindex, so "created" simply
// isn't batched at all -- this test guards that the per-file fallback stays
// wired up for every caller, not just the common case.
func TestBatchGitFileTimestamps_RenamedFile_FallsBackToFollowSemantics(t *testing.T) {
	repoDir := setupGitRepo(t)
	newPath := filepath.Join(repoDir, "features/feat-new-name.html")

	createdTime := commitFile(t, repoDir, "features/feat-old-name.html", "v1")

	runGitTimestampsBatchTest(t, repoDir, "mv", "features/feat-old-name.html", "features/feat-new-name.html")
	runGitTimestampsBatchTest(t, repoDir, "add", "-A")
	runGitTimestampsBatchTest(t, repoDir, "commit", "-m", "rename feat-old-name to feat-new-name")

	// Sanity: git recognizes this as a rename, not a plain add+delete.
	out, err := exec.Command("git", "-C", repoDir, "log", "-1", "--name-status", "--diff-filter=R", "--pretty=format:").Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("expected the last commit to be detected as a rename, got %q (err=%v)", out, err)
	}

	batch := batchGitFileTimestamps(repoDir, []string{newPath})

	got, ok := batch[newPath]
	if !ok {
		t.Fatalf("renamed file missing from batch result")
	}
	// The --follow-based single-file path traces through the rename to the
	// original add; the batch result must agree, not report the rename
	// commit as the creation time.
	wantCreated, _, err := gitFileTimestamps(repoDir, newPath)
	if err != nil {
		t.Fatalf("gitFileTimestamps(%s): %v", newPath, err)
	}
	if !got.created.Equal(wantCreated) {
		t.Errorf("created: batch=%v want(follow)=%v", got.created, wantCreated)
	}
	if !got.created.Truncate(time.Second).Equal(createdTime) {
		t.Errorf("created should trace back through the rename to the original add: got %v, want %v", got.created, createdTime)
	}
}

// TestTimestampsFromBatch_AgreesWithApplyGitTimestamps checks the full
// html-fallback resolution path (resolveTimestamps) produces identical
// output whether reached via the single-file applyGitTimestamps call or via
// timestampsFromBatch backed by batchGitFileTimestamps -- across the
// committed, multi-commit, and untracked-with-stale-HTML-override cases
// already covered for applyGitTimestamps above.
func TestTimestampsFromBatch_AgreesWithApplyGitTimestamps(t *testing.T) {
	repoDir := setupGitRepo(t)
	trackedTime := commitFile(t, repoDir, "feat.html", "v1")
	trackedPath := filepath.Join(repoDir, "feat.html")

	untrackedPath := filepath.Join(repoDir, "untracked.html")
	if err := os.WriteFile(untrackedPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	htmlCreated := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	htmlUpdated := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)

	batch := batchGitFileTimestamps(repoDir, []string{trackedPath, untrackedPath})

	for _, p := range []string{trackedPath, untrackedPath} {
		wantCreated, wantUpdated := applyGitTimestamps(repoDir, p, htmlCreated, htmlUpdated)
		gotCreated, gotUpdated, ok := timestampsFromBatch(batch, p, htmlCreated, htmlUpdated)
		if p == trackedPath && !ok {
			t.Fatalf("%s: expected a batch hit for a committed file", p)
		}
		if !ok {
			// Untracked files may legitimately miss the batch (no git record
			// at all) -- callers fall back to applyGitTimestamps, which is
			// exactly what we're comparing against here, so compute it directly.
			gotCreated, gotUpdated = applyGitTimestamps(repoDir, p, htmlCreated, htmlUpdated)
		}
		if !gotCreated.Equal(wantCreated) {
			t.Errorf("%s: created batch=%v single=%v", p, gotCreated, wantCreated)
		}
		if !gotUpdated.Equal(wantUpdated) {
			t.Errorf("%s: updated batch=%v single=%v", p, gotUpdated, wantUpdated)
		}
	}

	// Sanity the tracked file actually picked up the real git time, not the
	// stale HTML fallback -- otherwise the comparison above would be
	// vacuously true for both branches.
	created, _, ok := timestampsFromBatch(batch, trackedPath, htmlCreated, htmlUpdated)
	if !ok {
		t.Fatal("expected batch hit for tracked file")
	}
	if !created.Truncate(time.Second).Equal(trackedTime) {
		t.Errorf("tracked file created = %v, want git time %v", created, trackedTime)
	}
}

// TestBatchGitFileTimestamps_NonGitDir_FallsBackToHTML mirrors
// TestApplyGitTimestamps_NonGitDir_FallsBackToHTML for the batch path: when
// projectDir isn't a git repo at all, both the whole-batch bulkLastModified
// walk and the per-file gitFirstAdded call fail the same way, so the path
// never gets a batch entry and timestampsFromBatch reports a miss -- the
// caller falls back to applyGitTimestamps, which independently fails the
// same way and returns the HTML attributes unchanged.
func TestBatchGitFileTimestamps_NonGitDir_FallsBackToHTML(t *testing.T) {
	dir := t.TempDir() // not a git repo
	path := filepath.Join(dir, "feat.html")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	batch := batchGitFileTimestamps(dir, []string{path})
	if _, present := batch[path]; present {
		t.Errorf("expected no batch entry for a non-git projectDir, got %+v", batch[path])
	}

	htmlCreated := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
	htmlUpdated := time.Date(2024, 3, 16, 0, 0, 0, 0, time.UTC)
	if _, _, ok := timestampsFromBatch(batch, path, htmlCreated, htmlUpdated); ok {
		t.Error("expected a batch miss for a non-git projectDir")
	}
}

// TestBulkLastModified_MatchesGitLastModified verifies the batched
// "last modified" computation (bulkLastModified) agrees with the original
// per-file gitLastModified for both a committed and an untracked file.
func TestBulkLastModified_MatchesGitLastModified(t *testing.T) {
	repoDir := setupGitRepo(t)
	commitTime := commitFile(t, repoDir, "features/feat-a.html", "v1")
	_ = commitFile(t, repoDir, "features/feat-a.html", "v2")

	untracked := filepath.Join(repoDir, "features/feat-untracked.html")
	if err := os.WriteFile(untracked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	trackedPath := filepath.Join(repoDir, "features/feat-a.html")

	got := bulkLastModified(repoDir, "features")

	wantUpdated, err := gitLastModified(repoDir, trackedPath)
	if err != nil {
		t.Fatalf("gitLastModified: %v", err)
	}
	if !got["features/feat-a.html"].Equal(wantUpdated) {
		t.Errorf("tracked: got %v, want %v", got["features/feat-a.html"], wantUpdated)
	}
	if wantUpdated.Truncate(time.Second).Before(commitTime) {
		t.Errorf("sanity: last-modified %v should be at or after the first commit %v", wantUpdated, commitTime)
	}
	if ts, ok := got["features/feat-untracked.html"]; ok && !ts.IsZero() {
		t.Errorf("untracked file should not resolve to a non-zero timestamp, got %v", ts)
	}
}

// --- end-to-end: reindexTracks / reindexFeatureDir thread the batch through ---

// TestReindexFeatureDir_TimestampsMatchGitHistory verifies end-to-end that
// routing indexWorkitemNode through a pre-computed batch (as reindexFeatureDir
// now does) still lands the correct git-derived created/updated timestamps in
// the database -- not just that files get upserted (already covered by the
// existing reindexFeatureFiles/reindex tests) but that the VALUES survive the
// batching refactor.
func TestReindexFeatureDir_TimestampsMatchGitHistory(t *testing.T) {
	repoDir := setupGitRepo(t)
	wipnoteDir := filepath.Join(repoDir, ".wipnote")

	firstTime := commitFile(t, repoDir,
		filepath.Join(".wipnote", "features", "feat-batch01.html"),
		`<!DOCTYPE html><html><body>
<article id="feat-batch01" data-type="feature" data-status="todo" data-priority="medium">
<header><h1>Batch Test</h1></header>
</article>
</body></html>`)
	secondTime := commitFile(t, repoDir,
		filepath.Join(".wipnote", "features", "feat-batch01.html"),
		`<!DOCTYPE html><html><body>
<article id="feat-batch01" data-type="feature" data-status="active" data-priority="medium">
<header><h1>Batch Test</h1></header>
</article>
</body></html>`)

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	validIDs := make(map[string]bool)
	total, upserted, errCount := reindexFeatureDir(database, wipnoteDir, repoDir, "features", validIDs, false)
	if errCount != 0 {
		t.Fatalf("reindexFeatureDir: %d errors (of %d total, %d upserted)", errCount, total, upserted)
	}
	if upserted != 1 {
		t.Fatalf("expected 1 upserted, got %d", upserted)
	}

	var createdAt, updatedAt time.Time
	row := database.QueryRow(`SELECT created_at, updated_at FROM features WHERE id = ?`, "feat-batch01")
	if scanErr := row.Scan(&createdAt, &updatedAt); scanErr != nil {
		t.Fatalf("scan feature row: %v", scanErr)
	}

	if !createdAt.Truncate(time.Second).Equal(firstTime.UTC()) {
		t.Errorf("created_at = %v, want %v (first commit)", createdAt, firstTime)
	}
	if !updatedAt.Truncate(time.Second).Equal(secondTime.UTC()) {
		t.Errorf("updated_at = %v, want %v (second commit)", updatedAt, secondTime)
	}
}
