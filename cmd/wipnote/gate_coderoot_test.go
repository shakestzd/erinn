package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestResolveCodeRoot exercises the pure decision: the gate overrides to the
// invocation worktree ONLY when it is a linked worktree of the same repo as
// projectRoot (shared git-common-dir); otherwise it stays on projectRoot.
func TestResolveCodeRoot(t *testing.T) {
	const proj = "/repo/main"
	const wt = "/repo/.worktrees/feat"
	const common = "/repo/main/.git"

	cases := []struct {
		name                          string
		cwdTop, cwdCommon, projCommon string
		want                          string
	}{
		{"cwd-not-in-git", "", "", common, proj},
		{"project-not-a-repo", wt, "/repo/.worktrees/feat/.git", "", proj},
		{"cwd-is-project", proj, common, common, proj},
		{"linked-worktree-same-repo", wt, common, common, wt},
		{"unrelated-repo", "/other/repo", "/other/repo/.git", common, proj},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveCodeRoot(proj, tc.cwdTop, tc.cwdCommon, tc.projCommon)
			if got != tc.want {
				t.Fatalf("resolveCodeRoot(%q, %q, %q, %q) = %q, want %q",
					proj, tc.cwdTop, tc.cwdCommon, tc.projCommon, got, tc.want)
			}
		})
	}
}

// TestGateCodeRoot_LinkedWorktreeOverride verifies that gateCodeRoot, when the
// process CWD is inside a linked worktree of the project repo, returns that
// worktree — not projectRoot. This is the regression guard for the bug where
// the completion gate tested the main checkout instead of the branch's code.
func TestGateCodeRoot_LinkedWorktreeOverride(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	mainRepo := filepath.Join(root, "main")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("git", "init", mainRepo).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	git(mainRepo, "config", "user.email", "t@example.com")
	git(mainRepo, "config", "user.name", "t")
	git(mainRepo, "commit", "--allow-empty", "-m", "init")
	// Linked worktree on a new branch, as `wipnote yolo`/conductor create.
	wt := filepath.Join(root, "wt-feat")
	git(mainRepo, "worktree", "add", "-b", "feat-x", wt)

	// EvalSymlinks because macOS/CI temp dirs are often symlinked and git
	// reports the resolved path.
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}

	// From inside the worktree, gateCodeRoot must return the worktree.
	t.Chdir(wt)
	if got := resolve(gateCodeRoot(mainRepo)); got != resolve(wt) {
		t.Errorf("gateCodeRoot from worktree = %q, want %q", got, resolve(wt))
	}

	// From inside the main checkout, it must return projectRoot unchanged.
	t.Chdir(mainRepo)
	if got := resolve(gateCodeRoot(mainRepo)); got != resolve(mainRepo) {
		t.Errorf("gateCodeRoot from main = %q, want %q", got, resolve(mainRepo))
	}

	// From an unrelated repo, it must fall back to projectRoot.
	other := filepath.Join(root, "other")
	if err := exec.Command("git", "init", other).Run(); err != nil {
		t.Fatalf("git init other: %v", err)
	}
	t.Chdir(other)
	if got := resolve(gateCodeRoot(mainRepo)); got != resolve(mainRepo) {
		t.Errorf("gateCodeRoot from unrelated repo = %q, want %q (projectRoot)", got, resolve(mainRepo))
	}
}
