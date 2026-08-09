package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// reconcileTestRepo builds a real git repo under /tmp (so isTestTmpPath does
// not short-circuit reconcile's artifact commit) with an empty .wipnote store
// and an initial commit. It points projectDirFlag at it.
func reconcileTestRepo(t *testing.T) string {
	t.Helper()
	tmpParent, err := os.MkdirTemp("/tmp", "wipnote-reconcile-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpParent) })
	root := setupWorktreeGitRepoIn(t, tmpParent)

	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans"} {
		if err := os.MkdirAll(filepath.Join(root, ".wipnote", sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	t.Setenv("WIPNOTE_CACHE_DIR", tmpParent)
	projectDirFlag = root
	t.Cleanup(func() { projectDirFlag = "" })
	// Mandatory here above all: reconcile AUTO-COMMITS artifacts, so a test that
	// escapes isolation does not merely write into the real .wipnote/, it commits
	// there. projectDirFlag covers findWipnoteDir; these cover every resolver that
	// consults the environment instead.
	isolateProjectDir(t, root)
	return root
}

// TestReconcileCmd_NothingToReconcile_ExitsZero verifies the report-only
// happy path: a clean repo yields "nothing to reconcile" and no error.
func TestReconcileCmd_NothingToReconcile_ExitsZero(t *testing.T) {
	reconcileTestRepo(t)

	cmd := reconcileCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("reconcile (clean repo) should not error, got: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "nothing to reconcile") {
		t.Fatalf("expected 'nothing to reconcile', got: %q", out.String())
	}
}

// TestReconcileCmd_DoneButUncommitted_AutoCommitsAndReports drives the full
// CLI: a done feature with a dirty artifact is auto-committed and reported,
// proving the cmd → internal/hooks.Reconcile wiring (TDD-1 at the CLI seam).
func TestReconcileCmd_DoneButUncommitted_AutoCommitsAndReports(t *testing.T) {
	root := reconcileTestRepo(t)

	// The canonical artifact IS the record: a "done" work item whose HTML is
	// uncommitted. No read-index row is seeded, because the command no longer
	// consults one — that gate (`if database != nil`) is exactly what used to
	// make this class silently never run.
	id := "feat-cccccccc"
	artifact := filepath.Join(root, ".wipnote", "features", id+".html")
	if err := os.WriteFile(artifact, []byte(
		`<html><body><article id="`+id+`" data-type="feature" data-status="done">`+
			`<header><h1>Done Uncommitted</h1></header></article></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []string{artifact}

	cmd := reconcileCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("reconcile errored: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "auto-committed artifact for "+id) {
		t.Fatalf("expected auto-commit report for %s, got: %q", id, out.String())
	}
	st, _ := exec.Command("git", "-C", root, "status", "--porcelain", "--", files[0]).CombinedOutput()
	if strings.TrimSpace(string(st)) != "" {
		t.Fatalf("artifact still dirty after reconcile: %q", st)
	}
}
