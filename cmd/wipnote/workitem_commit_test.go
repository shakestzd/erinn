package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// setupTransactionalCompleteRepo builds a real git repo OUTSIDE the project
// tree (under /tmp, so isTestTmpPath does not short-circuit the strict commit)
// containing a .wipnote project with a pinned SQLite DB and one created+started
// feature. It returns the repo root, the .wipnote dir, the feature ID, and the
// session/agent identity used to drive wiSetStatusWithAgent. On return the
// feature is in-progress with its artifact committed, so a subsequent
// "done" transition is a clean transactional complete.
func setupTransactionalCompleteRepo(t *testing.T) (repoRoot, wipnoteDir, featID, sessionID, agentID string) {
	t.Helper()

	tmpParent, err := os.MkdirTemp("/tmp", "wipnote-txn-complete-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpParent) })
	repoRoot = setupWorktreeGitRepoIn(t, tmpParent)

	wipnoteDir = filepath.Join(repoRoot, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	sessionID = "txn-complete-session"
	agentID = dbpkg.AgentRootSentinel

	dbPath := filepath.Join(wipnoteDir, ".db", "wipnote.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now(),
	}); err != nil {
		database.Close()
		t.Fatalf("insert session: %v", err)
	}
	database.Close()

	projectDirFlag = repoRoot
	t.Cleanup(func() { projectDirFlag = "" })
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpParent)
	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", "separate")

	trackID := testSetupTrack(t, wipnoteDir)
	if err := testCreate("feature", "Txn Complete Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(wipnoteDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	node, _ := htmlparse.ParseFile(featFiles[0])
	featID = node.ID

	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("start feature: %v", err)
	}
	// Commit everything created so far so the only pending change for the
	// "done" transition is the artifact's status flip.
	if out, err := exec.Command("git", "-C", repoRoot, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add -A: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", repoRoot, "commit", "-m", "seed wipnote project").CombinedOutput(); err != nil {
		t.Fatalf("git commit seed: %v\n%s", err, out)
	}
	return repoRoot, wipnoteDir, featID, sessionID, agentID
}

// gitMustCommitInitial guarantees the repo has at least one commit and a clean
// tree. The shared setupWorktreeGitRepoIn helper's initial commit can silently
// fail in this sandbox (leaving an unborn HEAD with README staged); this stages
// everything and commits, asserting success so tests start from a known state.
func gitMustCommitInitial(t *testing.T, repoRoot string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", repoRoot, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add -A: %v\n%s", err, out)
	}
	// Nothing staged → already has a clean committed tree; nothing to do.
	if err := exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet").Run(); err == nil {
		return
	}
	out, err := exec.Command(
		"git", "-C", repoRoot,
		"-c", "user.email=test@test.com", "-c", "user.name=Test",
		"commit", "-m", "initial",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("git commit initial: %v\n%s", err, out)
	}
}

func diskStatus(t *testing.T, wipnoteDir, featID string) string {
	t.Helper()
	node, err := htmlparse.ParseFile(filepath.Join(wipnoteDir, "features", featID+".html"))
	if err != nil {
		t.Fatalf("parse feature HTML: %v", err)
	}
	return string(node.Status)
}

// TestTransactionalComplete_StrictFailureTriggersCompensatingReopen injects a
// strict-commit failure via the strictCommitFn seam and asserts the complete
// path (1) returns a non-nil error, (2) leaves the item in-progress (NOT done)
// on disk AND in SQLite — the compensating re-open fired — and (3) the
// re-open's side effects are coherent (active_feature_id cleared, statusline
// cache reflects the still-active item).
func TestTransactionalComplete_StrictFailureTriggersCompensatingReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git-backed work-item completion")
	}

	repoRoot, wipnoteDir, featID, sessionID, agentID := setupTransactionalCompleteRepo(t)

	orig := strictCommitFn
	t.Cleanup(func() { strictCommitFn = orig })
	strictCommitFn = func(_, _, _, _ string) (bool, error) {
		return false, fmt.Errorf("injected strict commit failure (locked index)")
	}

	err := wiSetStatusWithAgent("feature", featID, "done", sessionID, agentID)
	if err == nil {
		t.Fatal("expected non-nil error when strict commit fails, got nil")
	}
	if !strings.Contains(err.Error(), "completion aborted") {
		t.Errorf("error should explain completion was aborted, got: %v", err)
	}
	if !strings.Contains(err.Error(), "wipnote feature complete "+featID) {
		t.Errorf("error should include the exact remediation command, got: %v", err)
	}

	if got := diskStatus(t, wipnoteDir, featID); got != "in-progress" {
		t.Errorf("on-disk status after failed complete = %q, want in-progress (compensating re-open)", got)
	}

	database, derr := dbpkg.Open(filepath.Join(wipnoteDir, ".db", "wipnote.db"))
	if derr != nil {
		t.Fatalf("open db: %v", derr)
	}
	defer database.Close()
	var dbStatus string
	if qerr := database.QueryRow(`SELECT status FROM features WHERE id = ?`, featID).Scan(&dbStatus); qerr != nil {
		t.Fatalf("query feature status: %v", qerr)
	}
	if dbStatus != "in-progress" {
		t.Errorf("SQLite status after failed complete = %q, want in-progress", dbStatus)
	}

	// Re-open side effect: legacy active_feature_id must NOT still flag this
	// item as completed-and-cleared in a way that contradicts in-progress.
	var stuck int
	if qerr := database.QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE active_feature_id = ?`, featID,
	).Scan(&stuck); qerr != nil {
		t.Fatalf("query sessions: %v", qerr)
	}
	if stuck != 0 {
		t.Errorf("expected active_feature_id cleared for %s after re-open, %d sessions still point at it", featID, stuck)
	}

	// Side effect coherence: statusline cache is rebuilt as a NON-EMPTY active
	// line (it stores a rendered status line, not the bare ID). An empty cache
	// would mean "no active item" — incoherent with an in-progress re-open.
	cached := ReadStatuslineCache(wipnoteDir)
	if strings.TrimSpace(cached) == "" {
		t.Errorf("statusline cache is empty after re-open; expected an active-item line for %s", featID)
	}
	if !strings.Contains(cached, "Txn Complete Feature") {
		t.Errorf("statusline cache should reference the re-opened item, got: %q", cached)
	}

	// The repo HEAD must NOT carry a "complete" commit for this item.
	logOut, _ := exec.Command("git", "-C", repoRoot, "log", "--format=%s").CombinedOutput()
	if strings.Contains(string(logOut), "wipnote: complete "+featID) {
		t.Errorf("repo must not contain a 'complete' commit after aborted completion:\n%s", logOut)
	}
}

// TestTransactionalComplete_HappyPathAdvancesArtifactHead drives a real
// transactional complete with no injected failure and asserts the artifact's
// pre-commit HEAD differs from the post-commit HEAD and the item ends "done".
func TestTransactionalComplete_HappyPathAdvancesArtifactHead(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git-backed work-item completion")
	}

	repoRoot, wipnoteDir, featID, sessionID, agentID := setupTransactionalCompleteRepo(t)

	absArtifact := filepath.Join(wipnoteDir, "features", featID+".html")
	preHead := artifactHeadCommit(repoRoot, absArtifact)
	if preHead == "" {
		t.Fatal("expected a pre-commit HEAD for the artifact (it was seeded), got empty")
	}

	if err := wiSetStatusWithAgent("feature", featID, "done", sessionID, agentID); err != nil {
		t.Fatalf("transactional complete (happy path): %v", err)
	}

	postHead := artifactHeadCommit(repoRoot, absArtifact)
	if postHead == "" || postHead == preHead {
		t.Errorf("artifact HEAD did not advance: pre=%q post=%q", preHead, postHead)
	}
	if got := diskStatus(t, wipnoteDir, featID); got != "done" {
		t.Errorf("on-disk status after happy-path complete = %q, want done", got)
	}
	if artifactPathDirty(repoRoot, absArtifact) {
		t.Errorf("artifact path should be clean after transactional complete")
	}
}

// TestNonCompleteCallerCommitNonFatalContractPreserved verifies the existing
// non-fatal commitWipnoteArtifact contract is byte-for-byte intact for
// non-complete callers: a commit-blocking pre-commit hook makes git commit
// fail, yet the function still returns nil (state must not depend on it).
func TestNonCompleteCallerCommitNonFatalContractPreserved(t *testing.T) {
	// Force git to have no usable committer identity for THIS process: point
	// the global/system config at /dev/null so no ambient identity leaks in.
	// `git add` still succeeds (it needs no identity); `git commit` then fails
	// deterministically with "unable to auto-detect email address" — a
	// COMMIT-stage failure, independent of hook/exec-bit semantics, exercising
	// exactly the swallow at workitem_commit.go:117-125.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	// noIdentityRepo builds a pristine git repo with a deterministic initial
	// commit, then STRIPS the repo-local committer identity and writes a fresh
	// (unstaged) feature artifact. Each contract gets its OWN repo so one
	// failure cannot perturb the other.
	noIdentityRepo := func(t *testing.T, featureID string) (wipnoteDir string) {
		t.Helper()
		tmpDir, err := os.MkdirTemp("/tmp", "wipnote-nonfatal-*")
		if err != nil {
			t.Fatalf("MkdirTemp /tmp: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(tmpDir) })
		mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
		gitMustCommitInitial(t, mainRepo)
		// Remove the repo-local identity set by the shared helper so the
		// next commit has no name/email from any scope.
		exec.Command("git", "-C", mainRepo, "config", "--unset", "user.email").Run() //nolint:errcheck
		exec.Command("git", "-C", mainRepo, "config", "--unset", "user.name").Run()  //nolint:errcheck

		wipnoteDir = filepath.Join(mainRepo, ".wipnote")
		if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
			t.Fatalf("mkdir features: %v", err)
		}
		if err := os.WriteFile(filepath.Join(wipnoteDir, "features", featureID+".html"),
			[]byte(`<article id="`+featureID+`" data-status="in-progress"></article>`), 0o644); err != nil {
			t.Fatalf("write feature HTML: %v", err)
		}
		return wipnoteDir
	}

	// Non-complete caller path: commitWipnoteArtifact MUST return nil even
	// though the COMMIT fails (no committer identity) — non-fatal contract.
	wd1 := noIdentityRepo(t, "feat-nonfatal1")
	if err := commitWipnoteArtifact(wd1, "feature", "feat-nonfatal1", "start"); err != nil {
		t.Fatalf("non-fatal contract violated: commitWipnoteArtifact returned %v, want nil", err)
	}

	// Strict variant (used only by the complete path) MUST surface the same
	// commit failure as an error — proving the two contracts genuinely differ.
	wd2 := noIdentityRepo(t, "feat-nonfatal2")
	if _, serr := commitWipnoteArtifactStrict(wd2, "feature", "feat-nonfatal2", "complete"); serr == nil {
		t.Error("commitWipnoteArtifactStrict should return an error when the commit has no committer identity")
	}
}

// TestCheckUncommittedSourceCompleteGate_Unchanged is a regression assertion
// that the outside-.wipnote dirty-source guard's behavior is byte-identical:
// clean → nil, dirty → refusal naming the file, dirty+allowDirty → nil, and
// a dirty .wipnote-only change does NOT trip the gate.
func TestCheckUncommittedSourceCompleteGate_Unchanged(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-gate-regress-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	gitMustCommitInitial(t, mainRepo)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Clean tree → nil.
	if err := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-gate1", false); err != nil {
		t.Fatalf("clean tree should pass, got: %v", err)
	}

	// Dirty .wipnote-only change must NOT trip the gate (gate ignores .wipnote).
	wpFile := filepath.Join(wipnoteDir, "features", "feat-gate1.html")
	if err := os.MkdirAll(filepath.Dir(wpFile), 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	if err := os.WriteFile(wpFile, []byte("<article></article>"), 0o644); err != nil {
		t.Fatalf("write wipnote file: %v", err)
	}
	if out, err := exec.Command("git", "-C", mainRepo, "add", ".wipnote").CombinedOutput(); err != nil {
		t.Fatalf("git add .wipnote: %v\n%s", err, out)
	}
	if err := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-gate1", false); err != nil {
		t.Fatalf("dirty .wipnote-only should pass, got: %v", err)
	}

	// Dirty tracked SOURCE file → refusal that names the file.
	if err := os.WriteFile(filepath.Join(mainRepo, "README.md"),
		[]byte("# Test\nmodified\n"), 0o644); err != nil {
		t.Fatalf("modify README: %v", err)
	}
	gateErr := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-gate1", false)
	if gateErr == nil {
		t.Fatal("dirty source must be refused, got nil")
	}
	if !strings.Contains(gateErr.Error(), "README.md") ||
		!strings.Contains(gateErr.Error(), "uncommitted source changes outside .wipnote/") {
		t.Errorf("refusal must name the dirty file and reason, got: %v", gateErr)
	}

	// allowDirty=true → nil despite the dirty source file.
	if err := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-gate1", true); err != nil {
		t.Fatalf("allowDirty should bypass the gate, got: %v", err)
	}
}

// TestCheckUncommittedSourceCompleteGate_IgnoresSettingsLocal verifies that the
// ephemeral .claude/settings.local.json file does not count as dirty source.
func TestCheckUncommittedSourceCompleteGate_IgnoresSettingsLocal(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-gate-local-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)
	gitMustCommitInitial(t, mainRepo)
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	claudeDir := filepath.Join(mainRepo, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatalf("write settings.local.json: %v", err)
	}

	if err := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-gate2", false); err != nil {
		t.Fatalf("settings.local.json should be ignored, got: %v", err)
	}
}

// TestCompleteCommitsWipnoteArtifact verifies that completing a feature from
// inside a worktree commits the .wipnote/features/ HTML to the main repo even
// though the worktree's per-worktree exclude suppresses .wipnote/ from the
// worktree's own git status.
func TestCompleteCommitsWipnoteArtifact(t *testing.T) {
	// Force temp dirs to /tmp (outside the project tree) so that t.TempDir()
	// returns a path that cannot walk up into the project's .git directory.
	// The project may set TMPDIR to .test-tmp/ (inside the repo) to avoid
	// /tmp noexec in devcontainers; we override that here so our isolated git
	// repos stay truly outside the real repo.
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-commit-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	// Step 1: create a main git repo with an initial commit.
	// setupWorktreeGitRepo (worktree_helpers_test.go) creates a temp dir with
	// a git repo and an initial commit — exactly what worktree creation needs.
	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)

	// Step 2: create the .wipnote structure and commit a seed so the dir exists.
	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	// Write and commit a placeholder so .wipnote/ is tracked in the main repo.
	placeholder := filepath.Join(wipnoteDir, ".keep")
	if err := os.WriteFile(placeholder, []byte(""), 0o644); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}
	for _, args := range [][]string{
		{"-C", mainRepo, "add", ".wipnote/.keep"},
		{"-C", mainRepo, "commit", "-m", "add wipnote dir"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Step 3: create a worktree for the feature and install the per-worktree
	// exclude using EnsureForFeature which internally calls excludeWipnoteFromWorktree.
	featureID := "feat-test123"
	writeFeatureHTML(t, mainRepo, featureID, "")

	worktreePath, err := EnsureForFeature(featureID, mainRepo, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForFeature: %v", err)
	}

	// Verify the worktree exclude actually suppresses .wipnote/.
	// Write a file and check git status shows it as untracked in the worktree.
	testFile := filepath.Join(worktreePath, ".wipnote", "features", "check.html")
	_ = os.MkdirAll(filepath.Dir(testFile), 0o755)
	_ = os.WriteFile(testFile, []byte("<html></html>"), 0o644)
	statusOut, _ := exec.Command("git", "-C", worktreePath, "status", "--porcelain").CombinedOutput()
	if strings.Contains(string(statusOut), ".wipnote") {
		t.Logf("Note: .wipnote/ visible in worktree status — exclude may not have fired: %s", statusOut)
	}

	// Step 4: from the worktree's CWD, write the feature HTML to the MAIN repo.
	// (The worktree has a different .wipnote path; write to the main repo's path.)
	mainFeatureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")
	if err := os.WriteFile(mainFeatureHTML, []byte(`<article id="`+featureID+`" data-status="done"><header><h1>Test Feature</h1></header><section data-content><p>Description</p></section></article>`), 0o644); err != nil {
		t.Fatalf("write feature HTML to main repo: %v", err)
	}

	// Step 5: call commitWipnoteArtifact directly, simulating what wiSetStatusWithAgent
	// does after completing a work item.
	if err := commitWipnoteArtifact(wipnoteDir, "feature", featureID, "complete"); err != nil {
		t.Fatalf("commitWipnoteArtifact: %v", err)
	}

	// Step 6: assert the commit landed on the main repo HEAD.
	logOut, err := exec.Command("git", "-C", mainRepo, "log", "--oneline", "-1").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "wipnote:") {
		t.Errorf("expected HEAD commit to start with 'wipnote:', got: %s", logOut)
	}

	// Step 7: assert the commit contains the feature HTML.
	showOut, err := exec.Command("git", "-C", mainRepo, "show", "--name-only", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v\n%s", err, showOut)
	}
	if !strings.Contains(string(showOut), featureID+".html") {
		t.Errorf("expected %s.html in commit, got:\n%s", featureID, showOut)
	}
}

// TestCompleteCommitsWipnoteArtifact_NoOpWhenAlreadyCommitted verifies that
// calling commitWipnoteArtifact when the file is already committed and
// unmodified produces no new commit (idempotent / nothing-to-commit path).
func TestCompleteCommitsWipnoteArtifact_NoOpWhenAlreadyCommitted(t *testing.T) {
	// Force temp dirs to /tmp (outside the project tree). See
	// TestCompleteCommitsWipnoteArtifact for the full rationale.
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-commit-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)

	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	featureID := "feat-already1"
	featureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")
	if err := os.WriteFile(featureHTML, []byte(`<article id="`+featureID+`" data-status="done"></article>`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Commit the file directly — simulating it already being committed.
	for _, args := range [][]string{
		{"-C", mainRepo, "add", ".wipnote/features/" + featureID + ".html"},
		{"-C", mainRepo, "commit", "-m", "wipnote: complete " + featureID},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Record commit count before the idempotent call.
	countBefore := gitCommitCount(t, mainRepo)

	// Call commitWipnoteArtifact — nothing has changed, so it should be a no-op.
	if err := commitWipnoteArtifact(wipnoteDir, "feature", featureID, "complete"); err != nil {
		t.Fatalf("commitWipnoteArtifact (idempotent): %v", err)
	}

	countAfter := gitCommitCount(t, mainRepo)
	if countAfter != countBefore {
		t.Errorf("expected no new commit (idempotent), commit count changed from %d to %d", countBefore, countAfter)
	}
}

// TestCompleteCommitsWipnoteArtifact_SkipsWhenNoGitRepo verifies that the
// function is a no-op (returns nil) when the wipnote dir is not inside a git repo.
func TestCompleteCommitsWipnoteArtifact_SkipsWhenNoGitRepo(t *testing.T) {
	// CRITICAL: use os.MkdirTemp("/tmp", ...) to create a directory that is
	// NOT inside the project tree. t.TempDir() would return a path under
	// .test-tmp/ which is inside the wipnote repo — the isGitRepo check would
	// then walk up to the real .git directory and find it, causing the test to
	// fire a real git commit instead of skipping. By anchoring to /tmp we
	// ensure the path is genuinely outside any git repository.
	dir, err := os.MkdirTemp("/tmp", "wipnote-nogit-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	featureID := "feat-nogit99"
	featureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")
	if err := os.WriteFile(featureHTML, []byte(`<article id="`+featureID+`" data-status="done"></article>`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Should return nil even though there is no git repo.
	if err := commitWipnoteArtifact(wipnoteDir, "feature", featureID, "complete"); err != nil {
		t.Fatalf("expected nil in non-git dir, got: %v", err)
	}
}

func TestWorkitemArtifactCommitPolicyForEnv(t *testing.T) {
	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", "")
	if got := workitemArtifactCommitPolicyForEnv(); got != workitemArtifactCommitPolicyDefer {
		t.Fatalf("empty policy = %q, want default %q", got, workitemArtifactCommitPolicyDefer)
	}

	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", "defer")
	if got := workitemArtifactCommitPolicyForEnv(); got != workitemArtifactCommitPolicyDefer {
		t.Fatalf("defer policy = %q, want %q", got, workitemArtifactCommitPolicyDefer)
	}

	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", "separate")
	if got := workitemArtifactCommitPolicyForEnv(); got != workitemArtifactCommitPolicySeparate {
		t.Fatalf("separate policy = %q, want explicit legacy %q", got, workitemArtifactCommitPolicySeparate)
	}

	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", "bogus")
	if got := workitemArtifactCommitPolicyForEnv(); got != workitemArtifactCommitPolicyDefer {
		t.Fatalf("unknown policy = %q, want default fallback %q", got, workitemArtifactCommitPolicyDefer)
	}
}

func TestPersistWorkitemArtifactTransition_DeferRecordsIntentWithoutGitCommit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-defer-intent-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	repoRoot := setupWorktreeGitRepoIn(t, tmpDir)
	gitMustCommitInitial(t, repoRoot)
	wipnoteDir := filepath.Join(repoRoot, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}

	const featureID = "feat-defer-intent"
	featureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")
	if err := os.WriteFile(featureHTML, []byte(`<article id="`+featureID+`" data-status="in-progress"></article>`), 0o644); err != nil {
		t.Fatalf("write feature HTML: %v", err)
	}

	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpDir, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })
	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", "defer")

	countBefore := gitCommitCount(t, repoRoot)
	if err := persistWorkitemArtifactTransition(wipnoteDir, "feature", featureID, "start"); err != nil {
		t.Fatalf("persistWorkitemArtifactTransition: %v", err)
	}
	countAfter := gitCommitCount(t, repoRoot)
	if countAfter != countBefore {
		t.Fatalf("defer policy should not create a git commit, count changed from %d to %d", countBefore, countAfter)
	}

	ob, err := openCommitOutbox(repoRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	pending, err := ob.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending intent, got %d", len(pending))
	}
	if pending[0].Message != "wipnote: start "+featureID {
		t.Fatalf("intent message = %q, want %q", pending[0].Message, "wipnote: start "+featureID)
	}
	if len(pending[0].RelPaths) != 1 || pending[0].RelPaths[0] != ".wipnote/features/"+featureID+".html" {
		t.Fatalf("intent rel paths = %#v", pending[0].RelPaths)
	}
}

func TestPersistWorkitemArtifactTransition_DeferCoalescesLatestIntent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-defer-coalesce-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	repoRoot := setupWorktreeGitRepoIn(t, tmpDir)
	gitMustCommitInitial(t, repoRoot)
	wipnoteDir := filepath.Join(repoRoot, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}

	const featureID = "feat-defer-coalesce"
	featureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")
	if err := os.WriteFile(featureHTML, []byte(`<article id="`+featureID+`" data-status="done"></article>`), 0o644); err != nil {
		t.Fatalf("write feature HTML: %v", err)
	}

	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpDir, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })
	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", "defer")

	for _, action := range []string{"create", "start", "complete"} {
		if err := persistWorkitemArtifactTransition(wipnoteDir, "feature", featureID, action); err != nil {
			t.Fatalf("persistWorkitemArtifactTransition(%s): %v", action, err)
		}
	}

	ob, err := openCommitOutbox(repoRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	pending, err := ob.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected coalesced 1 pending intent, got %d: %#v", len(pending), pending)
	}
	if pending[0].Action != "complete" || pending[0].Message != "wipnote: complete "+featureID {
		t.Fatalf("latest intent not preserved: action=%q message=%q", pending[0].Action, pending[0].Message)
	}
}

// TestShouldAutocommitWorkitemArtifact verifies the allowlist that gates the
// auto-commit call site in wiSetStatusWithAgent (workitem.go). Plans are
// excluded because they use commitPlanChange (plan_yaml_cmds.go:42-90) to
// atomically commit both YAML and HTML; auto-committing only the rendered
// HTML would leave plan state inconsistent (roborev #1662). Unknown types
// must default to false so a future work-item type cannot silently inherit
// the wrong behavior — adding it requires touching the helper.
func TestShouldAutocommitWorkitemArtifact(t *testing.T) {
	cases := []struct {
		typeName string
		want     bool
	}{
		{"feature", true},
		{"bug", true},
		{"spike", true},
		{"plan", false},
		{"track", false},
		{"spec", false},
		{"", false},
		{"unknown", false},
		{"FEATURE", false}, // case-sensitive
	}
	for _, c := range cases {
		if got := shouldAutocommitWorkitemArtifact(c.typeName); got != c.want {
			t.Errorf("shouldAutocommitWorkitemArtifact(%q) = %v, want %v", c.typeName, got, c.want)
		}
	}
}

func TestTransactionalComplete_DeferQueuesIntentAndWarns(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git-backed work-item completion")
	}

	repoRoot, wipnoteDir, featID, sessionID, agentID := setupTransactionalCompleteRepo(t)

	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })
	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", "defer")

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	runErr := wiSetStatusWithAgent("feature", featID, "done", sessionID, agentID)
	_ = w.Close()
	stderrBytes, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("ReadAll(stderr): %v", readErr)
	}
	if runErr != nil {
		t.Fatalf("expected defer-mode complete to succeed, got: %v", runErr)
	}

	if got := diskStatus(t, wipnoteDir, featID); got != "done" {
		t.Fatalf("on-disk status after defer completion = %q, want done", got)
	}

	ob, err := openCommitOutbox(repoRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	pending, err := ob.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending deferred intent, got %d", len(pending))
	}
	if pending[0].Message != "wipnote: complete "+featID {
		t.Fatalf("deferred completion intent message = %q", pending[0].Message)
	}

	logOut, _ := exec.Command("git", "-C", repoRoot, "log", "--format=%s").CombinedOutput()
	if strings.Contains(string(logOut), "wipnote: complete "+featID) {
		t.Fatalf("repo must not contain a separate 'complete' commit in defer mode:\n%s", logOut)
	}

	stderrText := string(stderrBytes)
	if !strings.Contains(stderrText, "WIPNOTE_ARTIFACT_COMMIT_POLICY=defer") ||
		!strings.Contains(stderrText, "wipnote commit-queue flush") {
		t.Fatalf("expected explicit deferred-commit guidance on stderr, got:\n%s", stderrText)
	}
}

// TestActionFromStatus verifies the status→action verb mapping used to compose
// auto-commit messages. "in-progress" maps to "start" (the human-readable
// transition verb), "done" maps to "complete", everything else passes through.
func TestActionFromStatus(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"in-progress", "start"},
		{"done", "complete"},
		{"todo", "todo"},
		{"blocked", "blocked"},
		{"", ""},
		{"reopen", "reopen"},
	}
	for _, c := range cases {
		if got := actionFromStatus(c.status); got != c.want {
			t.Errorf("actionFromStatus(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

// TestCommitMessageReflectsAction verifies commitWipnoteArtifact embeds the
// action verb into the commit subject. This is the per-transition trail that
// gives `git log` a clean view of work-item lifecycle events.
func TestCommitMessageReflectsAction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-action-msg-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	mainRepo := setupWorktreeGitRepoIn(t, tmpDir)

	wipnoteDir := filepath.Join(mainRepo, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	featureID := "feat-actionmsg1"
	featureHTML := filepath.Join(wipnoteDir, "features", featureID+".html")

	// Each action writes a distinct HTML body so the file differs each time
	// and commitWipnoteArtifact has something to stage. In real use, the
	// HTML differs because of status/timestamp mutations.
	cases := []struct {
		action string
		body   string
	}{
		{"create", `<article id="` + featureID + `" data-status="todo"></article>`},
		{"start", `<article id="` + featureID + `" data-status="in-progress"></article>`},
		{"complete", `<article id="` + featureID + `" data-status="done"></article>`},
	}
	for _, c := range cases {
		if err := os.WriteFile(featureHTML, []byte(c.body), 0o644); err != nil {
			t.Fatalf("write feature HTML for %s: %v", c.action, err)
		}
		if err := commitWipnoteArtifact(wipnoteDir, "feature", featureID, c.action); err != nil {
			t.Fatalf("commitWipnoteArtifact(%q): %v", c.action, err)
		}
		// Read the latest commit subject and assert it contains the expected
		// action verb.
		out, err := exec.Command("git", "-C", mainRepo, "log", "-1", "--format=%s").CombinedOutput()
		if err != nil {
			t.Fatalf("git log -1 after %q: %v\n%s", c.action, err, out)
		}
		wantPrefix := "wipnote: " + c.action + " " + featureID
		if got := strings.TrimSpace(string(out)); got != wantPrefix {
			t.Errorf("commit subject after %q action = %q, want %q", c.action, got, wantPrefix)
		}
	}
}

// TestTransactionalComplete_ReadOnlyFSSkipsReopen injects a read-only-FS error
// via the strictCommitFn seam and asserts the complete path (1) returns nil,
// (2) leaves the item in "done" status (NOT reopened to in-progress), and (3)
// the repo HEAD does NOT carry a "complete" commit (autocommit was skipped).
func TestTransactionalComplete_ReadOnlyFSSkipsReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git-backed work-item completion")
	}

	repoRoot, wipnoteDir, featID, sessionID, agentID := setupTransactionalCompleteRepo(t)

	orig := strictCommitFn
	t.Cleanup(func() { strictCommitFn = orig })
	strictCommitFn = func(_, _, _, _ string) (bool, error) {
		return false, fmt.Errorf("strict autocommit: git add x: fatal: Unable to create '.git/index.lock': Read-only file system: %w", errReadOnlyFS)
	}

	err := wiSetStatusWithAgent("feature", featID, "done", sessionID, agentID)
	if err != nil {
		t.Fatalf("expected nil error on read-only FS (item stays done), got: %v", err)
	}

	if got := diskStatus(t, wipnoteDir, featID); got != "done" {
		t.Errorf("on-disk status after read-only FS = %q, want done (no reopen)", got)
	}

	// Verify SQLite also reflects done (not re-opened to in-progress).
	database, derr := dbpkg.Open(filepath.Join(wipnoteDir, ".db", "wipnote.db"))
	if derr != nil {
		t.Fatalf("open db: %v", derr)
	}
	defer database.Close()
	var dbStatus string
	if qerr := database.QueryRow(`SELECT status FROM features WHERE id = ?`, featID).Scan(&dbStatus); qerr != nil {
		t.Fatalf("query feature status: %v", qerr)
	}
	if dbStatus != "done" {
		t.Errorf("SQLite status after read-only FS = %q, want done", dbStatus)
	}

	// The repo HEAD must NOT carry a "complete" commit for this item (autocommit
	// was skipped because the FS is read-only).
	logOut, _ := exec.Command("git", "-C", repoRoot, "log", "--format=%s").CombinedOutput()
	if strings.Contains(string(logOut), "wipnote: complete "+featID) {
		t.Errorf("repo must not contain a 'complete' commit when autocommit was skipped:\n%s", logOut)
	}
}

// TestIsReadOnlyFSError verifies the string-match detector for the read-only
// filesystem signal from git subprocess output.
func TestIsReadOnlyFSError(t *testing.T) {
	cases := []struct {
		output string
		want   bool
	}{
		{"fatal: Unable to create '.git/index.lock': Read-only file system", true},
		{"error: open('.git/index.lock'): Read-only file system", true},
		{"Read-only file system", true},
		{"", false},
		{"Unable to create '.git/index.lock': File exists", false},
		{"Another git process seems to be running", false},
		{"hook rejection: pre-commit failed", false},
	}
	for _, c := range cases {
		if got := isReadOnlyFSError(c.output); got != c.want {
			t.Errorf("isReadOnlyFSError(%q) = %v, want %v", c.output, got, c.want)
		}
	}
}

// TestIsGitLockContention_ReadOnlyIsNotContention verifies that the read-only
// filesystem signal is NOT treated as transient lock contention (stops wasted
// retries in runGitWithLockRetry).
func TestIsGitLockContention_ReadOnlyIsNotContention(t *testing.T) {
	readOnlyOutput := "fatal: Unable to create '.git/index.lock': Read-only file system"
	if isGitLockContention(readOnlyOutput) {
		t.Errorf("isGitLockContention(%q) = true, want false (read-only FS is not transient lock contention)", readOnlyOutput)
	}

	// Sanity-check that real lock contention still matches.
	lockOutput := "fatal: Unable to create '.git/index.lock': File exists"
	if !isGitLockContention(lockOutput) {
		t.Errorf("isGitLockContention(%q) = false, want true (real lock contention should match)", lockOutput)
	}
}

// gitCommitCount returns the number of commits in the repo at dir.
func gitCommitCount(t *testing.T, dir string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-list: %v\n%s", err, out)
	}
	count := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count); err != nil {
		t.Fatalf("parse count: %v", err)
	}
	return count
}
