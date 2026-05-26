package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/planyaml"
)

// TestCommitPlanChange_RoutedThroughGitRunner asserts that commitPlanChange's
// add and commit mutations flow through gitRunner (the runGitWithLockRetry
// seam that runGitMutation delegates to) rather than via bare exec.Command.
// This is the TDD gate for feat-c0307d7a: the test is written before the
// implementation change, so it must FAIL against the old exec.Command code and
// PASS after commitPlanChange is routed through runGitMutation.
func TestCommitPlanChange_RoutedThroughGitRunner(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	planID := "plan-seam1234"
	planPath := filepath.Join(plansDir, planID+".yaml")
	plan := planyaml.NewPlan(planID, "Seam Test", "gitRunner seam assertion")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	// Override the gitRunner seam and the lock path so no real flock or
	// git subprocess is needed for the seam-assertion portion.
	// We still need the real git calls to succeed for the test to work end-to-end,
	// so we delegate to the real runner but record that it was called.
	origRunner := gitRunner
	origLockPath := gitMutationLockPath
	t.Cleanup(func() {
		gitRunner = origRunner
		gitMutationLockPath = origLockPath
	})

	// Point the lock path at a temp file so the flock doesn't need the cache dir.
	lockFile := filepath.Join(dir, "git-mutation.lock")
	gitMutationLockPath = func(_ string) (string, error) { return lockFile, nil }

	var addCalled, commitCalled bool
	gitRunner = func(repoRoot string, args ...string) ([]byte, error) {
		// Record which mutations flow through this seam.
		if len(args) > 0 {
			switch args[0] {
			case "add":
				addCalled = true
			case "commit":
				commitCalled = true
			}
		}
		// Delegate to the real git so the commit actually happens.
		all := append([]string{"-C", repoRoot}, args...)
		return exec.Command("git", all...).CombinedOutput()
	}

	if err := commitPlanChange(planPath, "plan(plan-seam1234): seam test"); err != nil {
		t.Fatalf("commitPlanChange: %v", err)
	}

	if !addCalled {
		t.Error("git add did not flow through gitRunner — commitPlanChange is not using runGitMutation for the add step")
	}
	if !commitCalled {
		t.Error("git commit did not flow through gitRunner — commitPlanChange is not using runGitMutation for the commit step")
	}
}

// initGitRepo creates a git repo in dir and configures a test user identity.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "Test User"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// writePlanFiles writes minimal YAML and HTML plan files under dir/plans/.
func writePlanFiles(t *testing.T, dir, planID string) (yamlPath, htmlPath string) {
	t.Helper()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	yamlPath = filepath.Join(plansDir, planID+".yaml")
	htmlPath = filepath.Join(plansDir, planID+".html")
	if err := os.WriteFile(yamlPath, []byte("meta:\n  id: "+planID+"\n"), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(htmlPath, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	return yamlPath, htmlPath
}

// gitLog runs git log --oneline and returns the output.
func gitLog(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	return string(out)
}

func TestAutocommitPlan_CreatesCommit(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	yamlPath, _ := writePlanFiles(t, dir, "plan-test1234")

	if err := commitPlanChange(yamlPath, "plan(plan-test1234): test commit"); err != nil {
		t.Fatalf("commitPlanChange: %v", err)
	}

	log := gitLog(t, dir)
	if !strings.Contains(log, "plan(plan-test1234): test commit") {
		t.Errorf("expected commit subject in log, got:\n%s", log)
	}

	// Verify the commit includes both plan files.
	showOut, err := exec.Command("git", "-C", dir, "show", "--stat", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	showStr := string(showOut)
	if !strings.Contains(showStr, "plan-test1234.yaml") {
		t.Errorf("expected yaml in commit stat, got:\n%s", showStr)
	}
	if !strings.Contains(showStr, "plan-test1234.html") {
		t.Errorf("expected html in commit stat, got:\n%s", showStr)
	}
}

func TestAutocommitPlan_SkipsWhenNoGitRepo(t *testing.T) {
	dir := t.TempDir()
	// Do NOT init git.

	yamlPath, _ := writePlanFiles(t, dir, "plan-nogit12")

	if err := commitPlanChange(yamlPath, "should be skipped"); err != nil {
		t.Fatalf("expected nil error in non-git dir, got: %v", err)
	}
	// No assertions on git state — there is no repo. The function returning nil is the spec.
}

func TestAutocommitPlan_PreservesUnrelatedStagedChanges(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	yamlPath, _ := writePlanFiles(t, dir, "plan-isol5678")

	// Stage an unrelated file BEFORE calling commitPlanChange.
	unrelated := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write unrelated: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "unrelated.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add unrelated: %v\n%s", err, out)
	}

	if err := commitPlanChange(yamlPath, "plan(plan-isol5678): isolation test"); err != nil {
		t.Fatalf("commitPlanChange: %v", err)
	}

	// The commit should NOT contain unrelated.txt.
	showOut, err := exec.Command("git", "-C", dir, "show", "--stat", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	if strings.Contains(string(showOut), "unrelated.txt") {
		t.Errorf("unrelated.txt was included in the plan commit:\n%s", showOut)
	}

	// unrelated.txt should still be staged (index A).
	statusOut, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if !strings.Contains(string(statusOut), "A  unrelated.txt") {
		t.Errorf("expected unrelated.txt to remain staged, got:\n%s", statusOut)
	}
}

func TestAutocommitPlan_NoOpCommit(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	planID := "plan-noop9876"
	planPath := filepath.Join(plansDir, planID+".yaml")

	// Create a proper YAML and render HTML via commitPlanChange so the first
	// commit captures the canonical rendered content. A second call with
	// identical YAML+HTML must produce no new commit (no-op).
	plan := planyaml.NewPlan(planID, "No-op Test", "idempotency check")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	// First call — creates the initial commit (re-renders HTML internally).
	if err := commitPlanChange(planPath, "plan(plan-noop9876): initial"); err != nil {
		t.Fatalf("first commitPlanChange: %v", err)
	}

	// Count commits before the second call.
	beforeOut, _ := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").CombinedOutput()
	before := strings.TrimSpace(string(beforeOut))

	// Second call with no YAML change — should produce no new commit.
	if err := commitPlanChange(planPath, "should be no-op"); err != nil {
		t.Fatalf("second commitPlanChange: %v", err)
	}

	// Count commits after — should be unchanged.
	afterOut, _ := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").CombinedOutput()
	after := strings.TrimSpace(string(afterOut))

	if before != after {
		t.Errorf("expected no new commit (count %s → %s)", before, after)
	}
}

func TestAutocommitPlan_StaleHtmlRegeneratedBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	planID := "plan-regen5678"
	planPath := filepath.Join(plansDir, planID+".yaml")
	htmlPath := filepath.Join(plansDir, planID+".html")

	// Create a YAML plan with initial title and render HTML once.
	plan := planyaml.NewPlan(planID, "Old Title", "initial description")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save initial yaml: %v", err)
	}
	if err := renderPlanToFileQuiet(dir, planID); err != nil {
		t.Fatalf("initial render: %v", err)
	}

	// Confirm initial HTML contains "Old Title".
	initialHTML, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read initial html: %v", err)
	}
	if !strings.Contains(string(initialHTML), "Old Title") {
		t.Fatalf("initial html should contain 'Old Title', got:\n%s", string(initialHTML))
	}

	// Mutate the YAML to change the title — do NOT manually re-render HTML.
	plan.Meta.Title = "New Title"
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save mutated yaml: %v", err)
	}

	// Call commitPlanChange — it should re-render HTML internally then commit.
	if err := commitPlanChange(planPath, "plan(plan-regen5678): stale html test"); err != nil {
		t.Fatalf("commitPlanChange: %v", err)
	}

	// Verify a commit was created.
	log := gitLog(t, dir)
	if !strings.Contains(log, "plan(plan-regen5678): stale html test") {
		t.Errorf("expected commit in log, got:\n%s", log)
	}

	// Inspect the committed HTML — it must contain "New Title", not "Old Title".
	showOut, err := exec.Command("git", "-C", dir, "show", "HEAD:plans/"+planID+".html").CombinedOutput()
	if err != nil {
		t.Fatalf("git show html: %v\n%s", err, showOut)
	}
	committedHTML := string(showOut)
	if !strings.Contains(committedHTML, "New Title") {
		t.Errorf("committed HTML should contain 'New Title' (re-render happened), got:\n%s", committedHTML)
	}
	if strings.Contains(committedHTML, "Old Title") {
		t.Errorf("committed HTML should NOT contain 'Old Title' (stale), got:\n%s", committedHTML)
	}

	// Also verify committed YAML has the new title.
	showYAML, err := exec.Command("git", "-C", dir, "show", "HEAD:plans/"+planID+".yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("git show yaml: %v\n%s", err, showYAML)
	}
	if !strings.Contains(string(showYAML), "New Title") {
		t.Errorf("committed YAML should contain 'New Title', got:\n%s", string(showYAML))
	}
}

func TestAutocommitPlan_CommitHookFailureIsNonFatal(t *testing.T) {
	// t.TempDir() uses /tmp which may have noexec — hooks would be silently ignored.
	// Prefer GOTMPDIR (set in CI) or the user home dir (always exec-capable) over
	// t.TempDir() so the pre-commit hook script can actually run.
	base := os.Getenv("GOTMPDIR")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = home
		} else {
			base = t.TempDir()
		}
	}
	dir, err := os.MkdirTemp(base, "TestAutocommitHook*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	initGitRepo(t, dir)

	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	planID := "plan-hook1234"
	planPath := filepath.Join(plansDir, planID+".yaml")

	// Create a valid YAML plan.
	plan := planyaml.NewPlan(planID, "Hook Test Plan", "pre-commit hook rejection test")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	// Install a pre-commit hook that always rejects.
	hooksDir := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookScript := "#!/bin/sh\nexit 1\n"
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}

	// Capture stderr via the package-level seam — no pipe, no race.
	var buf bytes.Buffer
	origStderr := stderr
	stderr = &buf
	t.Cleanup(func() { stderr = origStderr })

	commitErr := commitPlanChange(planPath, "plan(plan-hook1234): should be non-fatal")

	stderrOutput := buf.String()

	// Assert non-fatal: commitPlanChange must return nil.
	if commitErr != nil {
		t.Fatalf("expected nil error when pre-commit hook rejects, got: %v", commitErr)
	}

	// Assert warning was emitted to stderr.
	if !strings.Contains(stderrOutput, "autocommit warning") && !strings.Contains(stderrOutput, "please commit manually") {
		t.Errorf("expected warning on stderr, got: %q", stderrOutput)
	}

	// Assert plan files are still on disk (not rolled back).
	if _, err := os.Stat(planPath); err != nil {
		t.Errorf("plan yaml should still exist on disk: %v", err)
	}

	// Assert no commit was created (hook rejected it).
	countOut, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").CombinedOutput()
	if err != nil {
		// rev-list fails when there are no commits — that's expected: no commit means the hook worked.
		// This is the success case.
		return
	}
	count := strings.TrimSpace(string(countOut))
	if count != "0" {
		t.Errorf("expected 0 commits (hook rejected), got %s", count)
	}
}
