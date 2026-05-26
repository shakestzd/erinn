package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitDirLookup is a seam for isMainWorktree — injectable in tests.
// In production it calls git; in tests it can be replaced with a stub.
type gitDirLookupFunc func(dir, flag string) (string, error)

// TestIsMainWorktree_MainRepo verifies that isMainWorktreeWith returns true for
// the primary worktree (where --git-common-dir == --git-dir).
func TestIsMainWorktree_MainRepo(t *testing.T) {
	tmpDir := os.Getenv("TMPDIR")
	if tmpDir == "" {
		tmpDir = t.TempDir()
	}
	base, err := os.MkdirTemp(tmpDir, "wipnote-iso-main-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })

	mainRepo := filepath.Join(base, "main")
	if err := os.MkdirAll(mainRepo, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := exec.Command("git", "-C", mainRepo, "init", "-b", "main").Run(); err != nil {
		t.Skip("git init failed:", err)
	}
	_ = exec.Command("git", "-C", mainRepo, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", mainRepo, "config", "user.name", "Test").Run()
	if err := os.WriteFile(filepath.Join(mainRepo, "README"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", mainRepo, "add", "README").Run()
	if err := exec.Command("git", "-C", mainRepo, "commit", "-m", "init").Run(); err != nil {
		t.Skip("git commit failed:", err)
	}

	// In the primary worktree, --git-common-dir and --git-dir should produce the same path.
	got := isMainWorktree(mainRepo)
	if !got {
		t.Errorf("isMainWorktree(mainRepo) = false, want true for primary worktree")
	}
}

// TestIsMainWorktree_LinkedWorktree verifies that isMainWorktreeWith returns
// false for a linked worktree (where --git-dir is under .git/worktrees/).
func TestIsMainWorktree_LinkedWorktree(t *testing.T) {
	tmpDir := os.Getenv("TMPDIR")
	if tmpDir == "" {
		tmpDir = t.TempDir()
	}
	base, err := os.MkdirTemp(tmpDir, "wipnote-iso-linked-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })

	mainRepo := filepath.Join(base, "main")
	if err := os.MkdirAll(mainRepo, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := exec.Command("git", "-C", mainRepo, "init", "-b", "main").Run(); err != nil {
		t.Skip("git init failed:", err)
	}
	_ = exec.Command("git", "-C", mainRepo, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", mainRepo, "config", "user.name", "Test").Run()
	if err := os.WriteFile(filepath.Join(mainRepo, "README"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", mainRepo, "add", "README").Run()
	if err := exec.Command("git", "-C", mainRepo, "commit", "-m", "init").Run(); err != nil {
		t.Skip("git commit failed:", err)
	}

	// Create a linked worktree.
	linkedPath := filepath.Join(base, "linked")
	if err := exec.Command("git", "-C", mainRepo, "worktree", "add", linkedPath, "-b", "wt-branch").Run(); err != nil {
		t.Skip("git worktree add failed:", err)
	}

	// A linked worktree should NOT be the main worktree.
	got := isMainWorktree(linkedPath)
	if got {
		t.Errorf("isMainWorktree(linkedPath) = true, want false for linked worktree")
	}
}

// TestIsMainWorktreeWith_Seam verifies the injectable seam used in tests.
// It uses a fake gitDirLookup that simulates main vs linked worktree behavior.
func TestIsMainWorktreeWith_Seam(t *testing.T) {
	tests := []struct {
		name      string
		gitDir    string
		commonDir string
		wantMain  bool
	}{
		{
			name:      "main worktree - same path",
			gitDir:    "/repo/.git",
			commonDir: "/repo/.git",
			wantMain:  true,
		},
		{
			name:      "linked worktree - gitDir under worktrees/",
			gitDir:    "/repo/.git/worktrees/feat-abc",
			commonDir: "/repo/.git",
			wantMain:  false,
		},
		{
			name:      "gitDir error - not a repo",
			gitDir:    "",
			commonDir: "",
			wantMain:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(dir, flag string) (string, error) {
				switch flag {
				case "--git-dir":
					return tc.gitDir, nil
				case "--git-common-dir":
					return tc.commonDir, nil
				default:
					return "", nil
				}
			}
			got := isMainWorktreeWith("/any/dir", lookup)
			if got != tc.wantMain {
				t.Errorf("isMainWorktreeWith(...) = %v, want %v (gitDir=%q, commonDir=%q)",
					got, tc.wantMain, tc.gitDir, tc.commonDir)
			}
		})
	}
}

// TestReportMainWorktreeIsolation_Warning verifies that the report section
// surfaces isolation guidance when running in a primary worktree. Per roborev
// #3647 this is an informational NOTE (a primary worktree may legitimately be a
// dedicated isolated clone), and it must condition the guidance on the checkout
// being SHARED rather than asserting unconditional risk.
func TestReportMainWorktreeIsolation_Warning(t *testing.T) {
	var b strings.Builder
	reportMainWorktreeIsolationTo(&b, true /* isMain */, "/repo")
	out := b.String()
	low := strings.ToLower(out)

	if !strings.Contains(low, "note") {
		t.Errorf("expected an informational NOTE for primary worktree, got:\n%s", out)
	}
	if !strings.Contains(low, "shared") {
		t.Errorf("expected the guidance to condition on a SHARED checkout (not flag isolated clones), got:\n%s", out)
	}
	if !strings.Contains(low, "worktree") {
		t.Errorf("expected 'worktree' mention in output, got:\n%s", out)
	}
	if !strings.Contains(low, "isolat") {
		t.Errorf("expected isolation guidance in output, got:\n%s", out)
	}
}

// TestReportMainWorktreeIsolation_NoWarning verifies that the report section
// is clean (no warning) when already running in a linked worktree.
func TestReportMainWorktreeIsolation_NoWarning(t *testing.T) {
	var b strings.Builder
	reportMainWorktreeIsolationTo(&b, false /* isMain */, "/repo/.git/worktrees/feat-abc")
	out := b.String()

	if strings.Contains(out, "WARN") {
		t.Errorf("no warning expected for linked worktree, got:\n%s", out)
	}
	// Should still emit the section header (non-empty output).
	if strings.TrimSpace(out) == "" {
		t.Errorf("expected section header even for linked worktree, got empty output")
	}
}

// TestDoctorReport_IncludesIsolationSection verifies that runDoctorReport
// includes the multi-agent isolation section header.
func TestDoctorReport_IncludesIsolationSection(t *testing.T) {
	repoRoot := t.TempDir()
	if err := exec.Command("git", "-C", repoRoot, "init", "-q", "-b", "main").Run(); err != nil {
		t.Skip("git init failed:", err)
	}
	_ = exec.Command("git", "-C", repoRoot, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", repoRoot, "config", "user.name", "Test").Run()
	if err := os.WriteFile(filepath.Join(repoRoot, "README"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repoRoot, "add", "README").Run()
	_ = exec.Command("git", "-C", repoRoot, "commit", "-q", "-m", "init").Run()

	report := runDoctorReport(repoRoot)
	if !strings.Contains(strings.ToLower(report), "multi-agent") &&
		!strings.Contains(strings.ToLower(report), "isolation") {
		t.Errorf("expected multi-agent isolation section in doctor report, got:\n%s", report)
	}
}
