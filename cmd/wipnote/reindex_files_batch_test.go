package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// runGitBatchTest runs a git command in dir, failing the test on error.
func runGitBatchTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeMergeRepoFile writes body to name under dir, creating parent directories.
func writeMergeRepoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// buildMergeRepo constructs a repo with: a root commit, a diverging branch
// commit, a mainline commit, and a merge commit joining them. Returns the
// hashes in the order (root, mainline, branch, merge).
func buildMergeRepo(t *testing.T) (dir string, root, mainline, branch, merge string) {
	t.Helper()
	dir = t.TempDir()
	runGitBatchTest(t, dir, "init", "-b", "main")
	runGitBatchTest(t, dir, "config", "user.email", "test@test.com")
	runGitBatchTest(t, dir, "config", "user.name", "Test")

	writeMergeRepoFile(t, dir, "root.txt", "root\n")
	runGitBatchTest(t, dir, "add", "root.txt")
	runGitBatchTest(t, dir, "commit", "-m", "root commit")
	root = runGitBatchTest(t, dir, "rev-parse", "HEAD")

	runGitBatchTest(t, dir, "checkout", "-b", "feature-branch")
	writeMergeRepoFile(t, dir, "branch.txt", "branch\n")
	runGitBatchTest(t, dir, "add", "branch.txt")
	runGitBatchTest(t, dir, "commit", "-m", "branch commit")
	branch = runGitBatchTest(t, dir, "rev-parse", "HEAD")

	runGitBatchTest(t, dir, "checkout", "main")
	writeMergeRepoFile(t, dir, "mainline.txt", "mainline\n")
	runGitBatchTest(t, dir, "add", "mainline.txt")
	runGitBatchTest(t, dir, "commit", "-m", "mainline commit")
	mainline = runGitBatchTest(t, dir, "rev-parse", "HEAD")

	runGitBatchTest(t, dir, "merge", "--no-ff", "-m", "merge branch into main", "feature-branch")
	merge = runGitBatchTest(t, dir, "rev-parse", "HEAD")

	return dir, root, mainline, branch, merge
}

// TestCommitFilesByHash_MergeCommitYieldsNoFiles verifies that a merge commit
// (no -m/-c/--cc passed) resolves to zero files, matching the pre-batching
// per-commit `git diff-tree --root --no-commit-id -r --name-only <hash>`
// default -- diff-tree does not show a diff for a merge commit unless told
// which parent to diff against.
func TestCommitFilesByHash_MergeCommitYieldsNoFiles(t *testing.T) {
	dir, _, _, _, merge := buildMergeRepo(t)

	result := commitFilesByHash(dir, []string{merge})
	if files := result[merge]; len(files) != 0 {
		t.Errorf("merge commit %s: expected 0 files, got %v", merge, files)
	}
}

// TestCommitFilesByHash_RootCommitDiffsAgainstEmptyTree verifies --root
// behavior is preserved: a parentless commit's files are its full tree.
func TestCommitFilesByHash_RootCommitDiffsAgainstEmptyTree(t *testing.T) {
	dir, root, _, _, _ := buildMergeRepo(t)

	result := commitFilesByHash(dir, []string{root})
	files := result[root]
	if len(files) != 1 || files[0] != "root.txt" {
		t.Errorf("root commit %s: expected [root.txt], got %v", root, files)
	}
}

// TestCommitFilesByHash_BatchMatchesPerCommit is the equivalence proof: for a
// realistic mixed batch (root, mainline, branch, merge, and a bogus hash),
// batching them into one commitFilesByHash call must produce exactly the
// same per-commit file sets as resolving each hash individually (simulating
// the pre-fix one-subprocess-per-commit behavior).
func TestCommitFilesByHash_BatchMatchesPerCommit(t *testing.T) {
	dir, root, mainline, branch, merge := buildMergeRepo(t)
	bogus := "deadbeefdeadbeefdeadbeefdeadbeef12345678"
	hashes := []string{root, mainline, branch, merge, bogus}

	batched := commitFilesByHash(dir, hashes)

	for _, h := range hashes {
		perCommit := commitFilesByHash(dir, []string{h})
		wantFiles := append([]string(nil), perCommit[h]...)
		gotFiles := append([]string(nil), batched[h]...)
		sort.Strings(wantFiles)
		sort.Strings(gotFiles)
		if strings.Join(wantFiles, ",") != strings.Join(gotFiles, ",") {
			t.Errorf("hash %s: batched=%v single=%v", h, gotFiles, wantFiles)
		}
	}
}

// TestReindexFeatureFiles_SameCommitAcrossFeatures verifies that when two
// features reference the SAME commit, both features get the commit's files
// attributed (the dedup that collapses the single git invocation must not
// drop the fan-out to multiple features).
func TestReindexFeatureFiles_SameCommitAcrossFeatures(t *testing.T) {
	dir, _, mainline, _, _ := buildMergeRepo(t)

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	insertFeatureRow(t, database, "feat-shared-a")
	insertFeatureRow(t, database, "feat-shared-b")
	insertGitCommitRow(t, database, "feat-shared-a", mainline)
	insertGitCommitRow(t, database, "feat-shared-b", mainline)

	if _, err := reindexFeatureFiles(database, dir); err != nil {
		t.Fatalf("reindexFeatureFiles: %v", err)
	}

	for _, featureID := range []string{"feat-shared-a", "feat-shared-b"} {
		rows, err := dbpkg.ListFilesByFeature(database, featureID)
		if err != nil {
			t.Fatalf("ListFilesByFeature(%s): %v", featureID, err)
		}
		found := false
		for _, r := range rows {
			if r.FilePath == "mainline.txt" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected mainline.txt attributed, got %v", featureID, rows)
		}
	}
}

// TestReindexFeatureFiles_MergeCommitLinkedToFeatureYieldsNoFiles verifies
// end-to-end that a feature whose only linked commit is a merge commit gets
// zero feature_files rows, exactly like the pre-batching implementation.
func TestReindexFeatureFiles_MergeCommitLinkedToFeatureYieldsNoFiles(t *testing.T) {
	dir, _, _, _, merge := buildMergeRepo(t)

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	insertFeatureRow(t, database, "feat-merge-only")
	insertGitCommitRow(t, database, "feat-merge-only", merge)

	count, err := reindexFeatureFiles(database, dir)
	if err != nil {
		t.Fatalf("reindexFeatureFiles: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 associations for merge-only feature, got %d", count)
	}
}
