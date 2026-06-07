package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/guardprofile"
)

// fakeDeps builds guardInitDeps with controllable seams and a recording
// committer so tests never touch the real TTY, clock, git, or stdin.
type recordedCommit struct {
	repoRoot string
	paths    []string
	message  string
	called   bool
}

func fakeDeps(interactive bool, answer string, rec *recordedCommit) guardInitDeps {
	return guardInitDeps{
		interactive: func() bool { return interactive },
		now:         func() time.Time { return time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC) },
		approver:    func(string) string { return "tester" },
		commit: func(repoRoot string, paths []string, message string) error {
			rec.called = true
			rec.repoRoot = repoRoot
			rec.paths = paths
			rec.message = message
			return nil
		},
		in:  strings.NewReader(answer),
		out: &bytes.Buffer{},
	}
}

func writeGoMod(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestEnsureGuardProfile_NoOpWhenApprovedPresent(t *testing.T) {
	root := writeGoMod(t)
	// Pre-write an APPROVED profile.
	p := &guardprofile.Profile{Guards: map[string][]guardprofile.Guard{
		guardprofile.PhaseQuality: {{Name: "go-build", Cmd: "go build ./..."}},
	}}
	p.Approved = guardprofile.Approval{Signature: guardprofile.Signature(p), By: "x", At: "2026-01-01T00:00:00Z"}
	if err := writeGuardProfile(root, p); err != nil {
		t.Fatal(err)
	}

	rec := &recordedCommit{}
	deps := fakeDeps(true, "y\n", rec)
	ensureGuardProfileWith(root, deps)

	if rec.called {
		t.Error("approved profile present: must not re-commit (no-op expected)")
	}
}

func TestEnsureGuardProfile_SkippedWhenNonInteractive(t *testing.T) {
	root := writeGoMod(t) // unconfigured, has signals
	rec := &recordedCommit{}
	deps := fakeDeps(false, "y\n", rec) // non-interactive

	ensureGuardProfileWith(root, deps)

	if rec.called {
		t.Error("non-interactive launch must skip silently, never commit")
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(guardprofile.RelPath))); !os.IsNotExist(err) {
		t.Error("non-interactive launch must not write a profile")
	}
}

func TestGuardInit_WritesApprovedAndCommits(t *testing.T) {
	root := writeGoMod(t)
	rec := &recordedCommit{}
	deps := fakeDeps(true, "y\n", rec)

	p, err := runGuardSetup(root, deps)
	if err != nil {
		t.Fatalf("runGuardSetup: %v", err)
	}
	if p == nil {
		t.Fatal("expected a written profile on approval")
	}
	if !guardprofile.IsApproved(p) {
		t.Error("written profile must carry a valid approval signature")
	}
	if !rec.called {
		t.Fatal("approval must trigger a commit")
	}
	if len(rec.paths) != 1 || rec.paths[0] != filepath.FromSlash(guardprofile.RelPath) {
		t.Errorf("commit must stage only the profile path, got %v", rec.paths)
	}
	if !strings.Contains(rec.message, "(feat-18dac61f)") {
		t.Errorf("commit message must carry the work-item tag, got %q", rec.message)
	}

	// Re-load from disk and confirm the persisted profile is approved.
	loaded, err := guardprofile.Load(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !guardprofile.IsApproved(loaded) {
		t.Error("persisted profile must be approved on reload")
	}
}

func TestGuardInit_DeclineDoesNotCommit(t *testing.T) {
	root := writeGoMod(t)
	rec := &recordedCommit{}
	deps := fakeDeps(true, "n\n", rec)

	p, err := runGuardSetup(root, deps)
	if err != nil {
		t.Fatalf("runGuardSetup: %v", err)
	}
	if p != nil {
		t.Error("decline must not return a written profile")
	}
	if rec.called {
		t.Error("decline must not commit")
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(guardprofile.RelPath))); !os.IsNotExist(err) {
		t.Error("decline must not write a profile")
	}
}

// TestLaunchers_InvokeEnsureGuardProfile is the launcher-coverage guard (plan
// Medium-risk mitigation): every interactive exec entry point must invoke
// ensureGuardProfile beside the OTel/serve bootstrap. The five launcher
// commands (claude, yolo, dev, codex, gemini) all route through one of three
// shared exec sites — launchClaude (claude/yolo/dev), execCodex, execGemini —
// so asserting the wiring at those three sites covers all five.
func TestLaunchers_InvokeEnsureGuardProfile(t *testing.T) {
	cases := []struct{ file, anchor string }{
		{"claude.go", "launchClaude"},   // claude, yolo, dev
		{"codex.go", "execCodex"},       // codex
		{"gemini_launch.go", "execGemini"}, // gemini
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			data, err := os.ReadFile(c.file)
			if err != nil {
				t.Fatalf("read %s: %v", c.file, err)
			}
			src := string(data)
			if !strings.Contains(src, "ensureGuardProfile(") {
				t.Errorf("%s (%s) must invoke ensureGuardProfile()", c.file, c.anchor)
			}
			// Must be wired beside the serve/OTel bootstrap, not elsewhere.
			if !strings.Contains(src, "ensureServeForDashboard(") {
				t.Errorf("%s expected to contain the serve bootstrap anchor", c.file)
			}
		})
	}
}

// TestGuardInit_RevertsProfileWhenCommitFails is the regression for roborev
// #3688 (Medium): if the commit fails after the approved profile is written,
// the file must be removed so a future launch retries setup instead of seeing
// IsApproved and skipping a profile that was never committed.
func TestGuardInit_RevertsProfileWhenCommitFails(t *testing.T) {
	root := writeGoMod(t)
	deps := fakeDeps(true, "y\n", &recordedCommit{})
	deps.commit = func(string, []string, string) error { return os.ErrPermission } // simulate commit failure

	if _, err := runGuardSetup(root, deps); err == nil {
		t.Fatal("expected an error when commit fails")
	}
	profilePath := filepath.Join(root, filepath.FromSlash(guardprofile.RelPath))
	if _, statErr := os.Stat(profilePath); !os.IsNotExist(statErr) {
		t.Error("guard profile must be removed when the commit fails (so the next launch retries)")
	}
}

// TestGuardInit_CommitScopedToProfilePath is the regression for roborev #3688
// (High): the commit must be scoped to the guard-profile pathspec and must NOT
// sweep unrelated already-staged changes into the guard-profile commit.
func TestGuardInit_CommitScopedToProfilePath(t *testing.T) {
	root := writeGoMod(t)
	initGitRepo(t, root)

	// Pre-stage an unrelated file.
	unrelated := filepath.Join(root, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "unrelated.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add unrelated: %v\n%s", err, out)
	}

	// Point the advisory mutation lock at a temp file (avoid the real cache dir).
	origLock := gitMutationLockPath
	t.Cleanup(func() { gitMutationLockPath = origLock })
	gitMutationLockPath = func(string) (string, error) { return filepath.Join(root, "m.lock"), nil }

	// Production deps, but force interactive + auto-approve.
	deps := defaultGuardInitDeps()
	deps.interactive = func() bool { return true }
	deps.in = strings.NewReader("y\n")
	deps.out = &bytes.Buffer{}
	deps.now = func() time.Time { return time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC) }

	if _, err := runGuardSetup(root, deps); err != nil {
		t.Fatalf("runGuardSetup: %v", err)
	}

	show, err := exec.Command("git", "-C", root, "show", "--stat", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v\n%s", err, show)
	}
	if !strings.Contains(string(show), "guard-profile.yaml") {
		t.Errorf("guard profile not in the commit:\n%s", show)
	}
	if strings.Contains(string(show), "unrelated.txt") {
		t.Errorf("unrelated.txt was swept into the guard-profile commit:\n%s", show)
	}

	st, err := exec.Command("git", "-C", root, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if !strings.Contains(string(st), "A  unrelated.txt") {
		t.Errorf("unrelated.txt should remain staged (not committed), got:\n%s", st)
	}
}

// TestGuardInit_UnstagesProfileWhenCommitFails covers roborev #3692: in the
// production path `git add -- <profile>` runs before `git commit`, so on commit
// failure the revert must ALSO unstage the profile from the index, not just
// delete the working-tree file — otherwise a later user commit could include the
// supposedly-reverted profile.
func TestGuardInit_UnstagesProfileWhenCommitFails(t *testing.T) {
	root := writeGoMod(t)
	initGitRepo(t, root)

	origLock := gitMutationLockPath
	t.Cleanup(func() { gitMutationLockPath = origLock })
	gitMutationLockPath = func(string) (string, error) { return filepath.Join(root, "m.lock"), nil }

	deps := defaultGuardInitDeps()
	deps.interactive = func() bool { return true }
	deps.in = strings.NewReader("y\n")
	deps.out = &bytes.Buffer{}
	deps.now = func() time.Time { return time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC) }
	// Stage the profile for real (as the production add would), then fail the
	// commit. The add MUST succeed or the test would not exercise the
	// unstage-on-failure path (roborev #3695) — assert it.
	deps.commit = func(repoRoot string, paths []string, message string) error {
		addArgs := append([]string{"-C", repoRoot, "add", "--"}, paths...)
		if out, addErr := exec.Command("git", addArgs...).CombinedOutput(); addErr != nil {
			t.Fatalf("staging setup failed (test would be vacuous): %v\n%s", addErr, out)
		}
		return os.ErrPermission
	}

	if _, err := runGuardSetup(root, deps); err == nil {
		t.Fatal("expected an error when commit fails")
	}

	rel := filepath.FromSlash(guardprofile.RelPath)
	if _, statErr := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(statErr) {
		t.Error("profile file must be removed on commit failure")
	}
	st, err := exec.Command("git", "-C", root, "status", "--porcelain", "--", rel).CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(st)) != "" {
		t.Errorf("profile must be unstaged from the index after revert, got: %q", st)
	}
}

// TestGuardInit_ReapprovesExistingProfileWithoutOverwriting is the regression
// for roborev #3703: re-approving an existing (unapproved/drifted) profile must
// sign its CURRENT content, not regenerate from manifests — otherwise the user's
// hand-edited guards are silently overwritten.
func TestGuardInit_ReapprovesExistingProfileWithoutOverwriting(t *testing.T) {
	root := writeGoMod(t) // go.mod present: discovery WOULD generate go guards
	existing := &guardprofile.Profile{Guards: map[string][]guardprofile.Guard{
		guardprofile.PhaseQuality: {{Name: "custom-handrolled", Cmd: "make verify"}},
	}} // no approved signature -> unapproved/drifted
	if err := writeGuardProfile(root, existing); err != nil {
		t.Fatal(err)
	}

	rec := &recordedCommit{}
	p, err := runGuardSetup(root, fakeDeps(true, "y\n", rec))
	if err != nil {
		t.Fatalf("runGuardSetup: %v", err)
	}
	if p == nil {
		t.Fatal("expected an approved profile")
	}
	q := p.Guards[guardprofile.PhaseQuality]
	if len(q) != 1 || q[0].Name != "custom-handrolled" || q[0].Cmd != "make verify" {
		t.Errorf("re-approval overwrote the hand-edited guard, got %+v", q)
	}
	if !guardprofile.IsApproved(p) {
		t.Error("re-approved profile must be approved")
	}
	loaded, _ := guardprofile.Load(root)
	if loaded == nil || len(loaded.Guards[guardprofile.PhaseQuality]) != 1 ||
		loaded.Guards[guardprofile.PhaseQuality][0].Name != "custom-handrolled" {
		t.Errorf("persisted profile lost the custom guard: %+v", loaded)
	}
}

// TestGuardInit_AlreadyApprovedIsNoOp: re-running on an already-approved,
// up-to-date profile must not rewrite or commit.
func TestGuardInit_AlreadyApprovedIsNoOp(t *testing.T) {
	root := writeGoMod(t)
	p := &guardprofile.Profile{Guards: map[string][]guardprofile.Guard{
		guardprofile.PhaseQuality: {{Name: "x", Cmd: "echo x"}},
	}}
	p.Approved = guardprofile.Approval{Signature: guardprofile.Signature(p), By: "x", At: "2026-01-01T00:00:00Z"}
	if err := writeGuardProfile(root, p); err != nil {
		t.Fatal(err)
	}
	rec := &recordedCommit{}
	if _, err := runGuardSetup(root, fakeDeps(true, "y\n", rec)); err != nil {
		t.Fatalf("runGuardSetup: %v", err)
	}
	if rec.called {
		t.Error("already-approved up-to-date profile must not re-commit")
	}
}

// TestGuardInit_ReapprovalRestoresProfileOnCommitFailure is the regression for
// roborev #3708: on the re-approval path the profile file PRE-EXISTED (user's
// hand-edited content), so a failed commit must restore it, not delete it.
func TestGuardInit_ReapprovalRestoresProfileOnCommitFailure(t *testing.T) {
	root := writeGoMod(t)
	existing := &guardprofile.Profile{Guards: map[string][]guardprofile.Guard{
		guardprofile.PhaseQuality: {{Name: "custom-handrolled", Cmd: "make verify"}},
	}} // unapproved/drifted, hand-edited
	if err := writeGuardProfile(root, existing); err != nil {
		t.Fatal(err)
	}

	deps := fakeDeps(true, "y\n", &recordedCommit{})
	deps.commit = func(string, []string, string) error { return os.ErrPermission } // commit fails

	if _, err := runGuardSetup(root, deps); err == nil {
		t.Fatal("expected an error when commit fails")
	}

	// The pre-existing profile MUST survive (edits not lost)...
	loaded, err := guardprofile.Load(root)
	if err != nil || loaded == nil {
		t.Fatalf("re-approval commit failure must not delete the pre-existing profile (load: %v)", err)
	}
	q := loaded.Guards[guardprofile.PhaseQuality]
	if len(q) != 1 || q[0].Name != "custom-handrolled" {
		t.Errorf("must restore the user's original guards, got %+v", q)
	}
	// ...as the ORIGINAL unapproved content (no leaked approval signature).
	if guardprofile.IsApproved(loaded) {
		t.Error("restored profile must be the original UNAPPROVED content")
	}
}
