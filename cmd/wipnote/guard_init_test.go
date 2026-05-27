package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/guardprofile"
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
