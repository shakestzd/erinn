package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncDryRunNoMutation verifies that --dry-run lists planned operations
// without staging or committing anything.
func TestSyncDryRunNoMutation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-dryrun-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedWipnoteCommit(t, mainRepo)

	// Write a dirty file.
	featureID := "feat-dryrun1"
	featureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")
	if err := os.WriteFile(featureHTML, []byte(`<article id="`+featureID+`"></article>`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	countBefore := gitCommitCount(t, mainRepo)

	var out bytes.Buffer
	n, err := runSync(wipnoteDir, true, &out)
	if err != nil {
		t.Fatalf("runSync dry-run: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 planned file, got %d", n)
	}
	if !strings.Contains(out.String(), featureID+".html") {
		t.Errorf("expected dry-run output to mention %s, got:\n%s", featureID, out.String())
	}

	// Index must still be empty: nothing staged.
	staged, _ := exec.Command("git", "-C", mainRepo, "diff", "--cached", "--name-only").CombinedOutput()
	if strings.TrimSpace(string(staged)) != "" {
		t.Errorf("dry-run staged files: %s", staged)
	}

	countAfter := gitCommitCount(t, mainRepo)
	if countAfter != countBefore {
		t.Errorf("dry-run created commit: count %d -> %d", countBefore, countAfter)
	}
}

// TestSyncSkipsCleanTree verifies that running sync on a tree with no dirty
// .wipnote/ files produces no commit and reports 0 files synced.
func TestSyncSkipsCleanTree(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-clean-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Commit a placeholder to ensure .wipnote/ exists but is clean.
	placeholder := filepath.Join(wipnoteDir, ".keep")
	if err := os.WriteFile(placeholder, []byte(""), 0o644); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}
	for _, args := range [][]string{
		{"-C", mainRepo, "add", ".wipnote/.keep"},
		{"-C", mainRepo, "commit", "-m", "seed"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	countBefore := gitCommitCount(t, mainRepo)
	var out bytes.Buffer
	n, err := runSync(wipnoteDir, false, &out)
	if err != nil {
		t.Fatalf("runSync clean: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 files synced on clean tree, got %d", n)
	}
	countAfter := gitCommitCount(t, mainRepo)
	if countAfter != countBefore {
		t.Errorf("clean-tree sync created commit: %d -> %d", countBefore, countAfter)
	}
}

// TestSyncRespectsTestTmpGuard verifies that sync is a no-op when wipnoteDir
// looks like a test temp dir (matches isTestTmpPath). Defense-in-depth — even
// if a test mis-configures isolation, sync must never fire a real git mutation.
func TestSyncRespectsTestTmpGuard(t *testing.T) {
	// Create a wipnoteDir whose path contains ".test-tmp" — this is the
	// pattern isTestTmpPath rejects. We don't need a real git repo because
	// the guard runs before isGitRepo.
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-guard-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	guardedRoot := filepath.Join(tmpDir, ".test-tmp", "fake-project")
	wipnoteDir := filepath.Join(guardedRoot, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Drop a dirty file that would otherwise be picked up.
	featureID := "feat-guard1"
	featureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")
	if err := os.WriteFile(featureHTML, []byte(`<article id="`+featureID+`"></article>`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var out bytes.Buffer
	n, err := runSync(wipnoteDir, false, &out)
	if err != nil {
		t.Fatalf("runSync guarded: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 files synced under test-tmp guard, got %d", n)
	}
}

// TestSyncCommitsDirtyFiles verifies the happy path: dirty files are staged
// and committed in a single commit, with a message matching the spec.
func TestSyncCommitsDirtyFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-happy-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	for _, sub := range []string{"features", "bugs"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	seedWipnoteCommit(t, mainRepo)

	// Write two dirty files (one feature, one bug).
	featureID := "feat-sync001"
	bugID := "bug-sync002"
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", featureID+".html"),
		[]byte(`<article id="`+featureID+`"></article>`), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wipnoteDir, "bugs", bugID+".html"),
		[]byte(`<article id="`+bugID+`"></article>`), 0o644); err != nil {
		t.Fatalf("write bug: %v", err)
	}

	countBefore := gitCommitCount(t, mainRepo)

	var out bytes.Buffer
	n, err := runSync(wipnoteDir, false, &out)
	if err != nil {
		t.Fatalf("runSync: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 files synced, got %d", n)
	}

	countAfter := gitCommitCount(t, mainRepo)
	if countAfter != countBefore+1 {
		t.Errorf("expected exactly 1 new commit, count %d -> %d", countBefore, countAfter)
	}

	// Commit message: 2 items -> "wipnote: sync 2 items"
	subj, err := exec.Command("git", "-C", mainRepo, "log", "-1", "--format=%s").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	got := strings.TrimSpace(string(subj))
	want := "wipnote: sync 2 items"
	if got != want {
		t.Errorf("commit subject = %q, want %q", got, want)
	}

	// Both files should appear in the commit.
	showOut, err := exec.Command("git", "-C", mainRepo, "show", "--name-only", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	for _, want := range []string{featureID + ".html", bugID + ".html"} {
		if !strings.Contains(string(showOut), want) {
			t.Errorf("expected %s in commit, got:\n%s", want, showOut)
		}
	}
}

// TestSyncSingleItemMessage verifies the commit-message variant for a single
// dirty file: "wipnote: sync <id>" (no "N items" suffix).
func TestSyncSingleItemMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-single-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedWipnoteCommit(t, mainRepo)

	featureID := "feat-onlyone"
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", featureID+".html"),
		[]byte(`<article id="`+featureID+`"></article>`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var out bytes.Buffer
	if _, err := runSync(wipnoteDir, false, &out); err != nil {
		t.Fatalf("runSync: %v", err)
	}

	subj, err := exec.Command("git", "-C", mainRepo, "log", "-1", "--format=%s").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	got := strings.TrimSpace(string(subj))
	want := "wipnote: sync " + featureID
	if got != want {
		t.Errorf("commit subject = %q, want %q", got, want)
	}
}

// TestSyncSkipsPreMergeBackup verifies that paths under .wipnote/.pre-merge-backup-*/
// are filtered out, since those are durability snapshots produced by external
// merge tooling and should never be synced back into history.
func TestSyncSkipsPreMergeBackup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-bkp-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	backupDir := filepath.Join(wipnoteDir, ".pre-merge-backup-20260101T000000Z", "features")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("mkdir backup: %v", err)
	}
	seedWipnoteCommit(t, mainRepo)
	// File that should be skipped (inside backup).
	if err := os.WriteFile(filepath.Join(backupDir, "feat-bkp.html"),
		[]byte(`<article id="feat-bkp"></article>`), 0o644); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	var out bytes.Buffer
	n, err := runSync(wipnoteDir, false, &out)
	if err != nil {
		t.Fatalf("runSync: %v", err)
	}
	if n != 0 {
		t.Errorf("expected backup-only tree to sync 0 files, got %d", n)
	}
}

// TestIsRuntimeStateFile is a table test for the runtime-state exclusion used
// by sync staging (bug-fddf5820, finding 14).
func TestIsRuntimeStateFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Excluded runtime state.
		{".wipnote/.active-session", true},
		{".wipnote/.launch-mode", true},
		{".wipnote/.serve.lock", true},
		{".wipnote/debug.log", true},
		{".wipnote/writer.pid", true},
		{".wipnote/logs/writer.log", true},
		{".wipnote/logs/sub/anything.log", true},
		// Real canonical artifacts that MUST still sync.
		{".wipnote/features/feat-abc12345.html", false},
		{".wipnote/bugs/bug-deadbeef.html", false},
		{".wipnote/arch/some-card.md", false},
		{".wipnote/config.json", false},
		// Similar-but-not-runtime names must not be over-matched.
		{".wipnote/sessions/127926be.html", false},
		{".wipnote/active-session-notes.md", false},
	}
	for _, c := range cases {
		if got := isRuntimeStateFile(c.path); got != c.want {
			t.Errorf("isRuntimeStateFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestSyncExcludesRuntimeState verifies that sync stages canonical work-item
// artifacts but skips volatile machine-local runtime state files
// (bug-fddf5820, finding 14).
func TestSyncExcludesRuntimeState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-runtime-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	seedWipnoteCommit(t, mainRepo)

	// One real work-item file that SHOULD sync.
	featureID := "feat-rt00001"
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", featureID+".html"),
		[]byte(`<article id="`+featureID+`"></article>`), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	// Runtime-state files that should be EXCLUDED.
	runtime := map[string]string{
		".active-session": "sess-123",
		".launch-mode":    "dev",
		".serve.lock":     "8088",
		"debug.log":       "noise\n",
		"writer.pid":      "4242",
		"logs/writer.log": "log line\n",
	}
	for rel, content := range runtime {
		full := filepath.Join(wipnoteDir, rel)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write runtime %s: %v", rel, err)
		}
	}

	var out bytes.Buffer
	n, err := runSync(wipnoteDir, false, &out)
	if err != nil {
		t.Fatalf("runSync: %v", err)
	}
	if n != 1 {
		t.Errorf("expected exactly 1 file synced (the feature), got %d\noutput: %s", n, out.String())
	}

	showOut, err := exec.Command("git", "-C", mainRepo, "show", "--name-only", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	if !strings.Contains(string(showOut), featureID+".html") {
		t.Errorf("expected %s.html in commit, got:\n%s", featureID, showOut)
	}
	for rel := range runtime {
		if strings.Contains(string(showOut), filepath.Base(rel)) {
			t.Errorf("runtime-state file %q must not be committed, got:\n%s", rel, showOut)
		}
	}
}

// TestSyncSingleIDDerivation verifies extractWorkItemID returns the ID stem
// for canonical .wipnote/<type>s/<id>.html paths, and empty for paths it
// cannot confidently map (used to fall back to "N items" wording).
func TestSyncSingleIDDerivation(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{".wipnote/features/feat-abc12345.html", "feat-abc12345"},
		{".wipnote/bugs/bug-deadbeef.html", "bug-deadbeef"},
		{".wipnote/spikes/spike-99.html", "spike-99"},
		{".wipnote/config.json", ""},
		{".wipnote/.keep", ""},
		{".wipnote/features/notes.txt", ""},
	}
	for _, c := range cases {
		if got := extractWorkItemID(c.path); got != c.want {
			t.Errorf("extractWorkItemID(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// TestSyncFindsWorktreeExcludedFiles verifies that sync discovers and stages
// .wipnote/ files that are hidden from `git status` by a per-worktree exclude
// (info/exclude). This is the regression gate for roborev #1720: without
// --ignored=matching + `git add -f`, sync was a no-op in exactly the worktrees
// it is meant to flush (excludeWipnoteFromWorktree installs that exclude).
func TestSyncFindsWorktreeExcludedFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-excluded-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedWipnoteCommit(t, mainRepo)

	// Install the exact exclude that excludeWipnoteFromWorktree writes into
	// linked worktrees. From git's perspective the repo now treats any
	// untracked file under .wipnote/ as ignored — `git status` hides them,
	// and `git add` refuses them without -f.
	excludePath := filepath.Join(mainRepo, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		t.Fatalf("mkdir info/: %v", err)
	}
	if err := os.WriteFile(excludePath, []byte(".wipnote/\n"), 0o644); err != nil {
		t.Fatalf("write exclude: %v", err)
	}

	// Drop an untracked file that the exclude would normally hide.
	featureID := "feat-excluded1"
	featureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")
	if err := os.WriteFile(featureHTML, []byte(`<article id="`+featureID+`"></article>`), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}

	// Sanity check: confirm the exclude actually hides the file from a plain
	// `git status` so this test is exercising the bug it documents, not a
	// silently-passing variation.
	plain, _ := exec.Command("git", "-C", mainRepo, "status", "--porcelain", "--", ".wipnote/").CombinedOutput()
	if strings.Contains(string(plain), featureID) {
		t.Fatalf("test precondition broken: plain `git status` should hide excluded files but emitted: %s", plain)
	}

	countBefore := gitCommitCount(t, mainRepo)

	var out bytes.Buffer
	n, err := runSync(wipnoteDir, false, &out)
	if err != nil {
		t.Fatalf("runSync: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 worktree-excluded file synced, got %d (sync is silently missing excluded files — regression of roborev #1720)", n)
	}
	if got := gitCommitCount(t, mainRepo); got != countBefore+1 {
		t.Errorf("expected exactly 1 new commit, count %d -> %d", countBefore, got)
	}

	showOut, _ := exec.Command("git", "-C", mainRepo, "show", "--name-only", "HEAD").CombinedOutput()
	if !strings.Contains(string(showOut), featureID+".html") {
		t.Errorf("expected %s.html in commit, got:\n%s", featureID, showOut)
	}
}

// TestSyncReturnsStagedCountOnCommitFailure verifies that when files were
// discovered and staged but the commit fails (e.g. a pre-commit hook rejects
// it), runSync returns the staged count rather than 0. The CLI wrapper uses
// the return value to decide whether to print "nothing to sync" — returning
// 0 in this branch misleads the user that no work was found. Regression gate
// for roborev #1720.
func TestSyncReturnsStagedCountOnCommitFailure(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-sync-failcommit-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedWipnoteCommit(t, mainRepo)

	// Install a pre-commit hook that always rejects, simulating a hook-side
	// failure. We use the worktree's .git/hooks/ which is the per-worktree
	// path Git resolves first.
	hooksDir := filepath.Join(mainRepo, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho 'rejected by test hook' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	featureID := "feat-hookfail1"
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", featureID+".html"),
		[]byte(`<article id="`+featureID+`"></article>`), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}

	var out bytes.Buffer
	n, err := runSync(wipnoteDir, false, &out)
	if err != nil {
		t.Fatalf("runSync: %v", err)
	}
	if n != 1 {
		t.Errorf("expected staged count = 1 on hook-rejected commit, got %d (CLI wrapper would falsely print 'nothing to sync')", n)
	}
}

// seedWipnoteCommit writes and commits a .wipnote/.keep placeholder so the
// repo has a HEAD that `gitCommitCount` can read, and so subsequent dirty
// .wipnote/ writes show up as modifications/untracked rather than appearing
// before the first commit.
func seedWipnoteCommit(t *testing.T, repoRoot string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".wipnote"), 0o755); err != nil {
		t.Fatalf("seedWipnoteCommit mkdir: %v", err)
	}
	keep := filepath.Join(repoRoot, ".wipnote", ".keep")
	if err := os.WriteFile(keep, []byte(""), 0o644); err != nil {
		t.Fatalf("seedWipnoteCommit write: %v", err)
	}
	for _, args := range [][]string{
		{"-C", repoRoot, "add", ".wipnote/.keep"},
		{"-C", repoRoot, "commit", "-m", "seed wipnote"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("seedWipnoteCommit git %v: %v\n%s", args, err, out)
		}
	}
}

