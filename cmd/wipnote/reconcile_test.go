package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// reconcileTestRepo builds a real git repo under /tmp (so isTestTmpPath does
// not short-circuit reconcile's artifact commit) with an empty .wipnote store
// and an initial commit. It points projectDirFlag + WIPNOTE_DB_PATH at it.
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
	dbPath := filepath.Join(root, ".wipnote", ".db", "wipnote.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpParent)
	projectDirFlag = root
	t.Cleanup(func() { projectDirFlag = "" })
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

	// Insert a done feature row directly into the read index the command
	// opens, plus a matching uncommitted artifact file under .wipnote/.
	id := "feat-cccccccc"
	dbPath := os.Getenv("WIPNOTE_DB_PATH")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	now := time.Now().UTC()
	if err := dbpkg.InsertFeature(database, &dbpkg.Feature{
		ID: id, Type: "feature", Title: "Done Uncommitted",
		Status: "done", Priority: "medium", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		database.Close()
		t.Fatalf("InsertFeature: %v", err)
	}
	database.Close()

	artifact := filepath.Join(root, ".wipnote", "features", id+".html")
	if err := os.WriteFile(artifact, []byte("<html>"+id+" done</html>"), 0o644); err != nil {
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
