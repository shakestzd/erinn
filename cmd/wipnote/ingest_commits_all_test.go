package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// initTestRepo creates a minimal git repo in dir with an initial commit on main.
func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "-C", dir, "init", "-q", "-b", "main"},
		{"git", "-C", dir, "config", "user.email", "test@wipnote.test"},
		{"git", "-C", dir, "config", "user.name", "Wipnote Test"},
	}
	for _, args := range cmds {
		if err := exec.Command(args[0], args[1:]...).Run(); err != nil {
			t.Skipf("git setup failed (%v), skipping", err)
		}
	}
}

func gitCommitFile(t *testing.T, dir, filename, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", dir, "add", filename).Run() //nolint
	if out, err := exec.Command("git", "-C", dir, "commit", "-q", "-m", msg).CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}
}

// TestIngestCommitsFromRepo_AllBranches verifies that ingestCommitsFromRepo
// picks up commits on a non-HEAD branch (simulating a worktree-branch commit).
func TestIngestCommitsFromRepo_AllBranches(t *testing.T) {
	dir := t.TempDir()
	initTestRepo(t, dir)

	// Initial commit on main.
	gitCommitFile(t, dir, "README.md", "main", "initial commit on main")

	// Create and switch to a feature branch, add a commit there.
	if out, err := exec.Command("git", "-C", dir, "checkout", "-q", "-b", "feat-aabbccdd").CombinedOutput(); err != nil {
		t.Skipf("git checkout -b failed: %v\n%s", err, out)
	}
	gitCommitFile(t, dir, "impl.go", "package p", "feat(feat-aabbccdd): implement feature (#121)")

	// Switch back to main -- the feature branch commit is now non-HEAD.
	if err := exec.Command("git", "-C", dir, "checkout", "-q", "main").Run(); err != nil {
		t.Skip("git checkout main failed")
	}

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	inserted, attributed, err := ingestCommitsFromRepo(database, dir, "", 0)
	if err != nil {
		t.Fatalf("ingestCommitsFromRepo: %v", err)
	}

	// Should have at least 2 commits (initial + feature branch commit).
	if inserted < 2 {
		t.Errorf("expected >=2 inserted, got %d", inserted)
	}
	// The feature branch commit carries a feat-aabbccdd attribution.
	if attributed < 1 {
		t.Errorf("expected >=1 attributed commit (from non-HEAD branch), got %d", attributed)
	}

	// Confirm the feature-attributed commit is queryable.
	commits, err := dbpkg.GetCommitsByFeature(database, "feat-aabbccdd")
	if err != nil {
		t.Fatalf("GetCommitsByFeature: %v", err)
	}
	if len(commits) == 0 {
		t.Error("non-HEAD branch commit not found via GetCommitsByFeature -- provenance gate would still block")
	}
}

// TestIngestCommitsFromRepo_Attribution_DirectMessage verifies attribution
// extraction works correctly via parenthetical commit message pattern.
func TestIngestCommitsFromRepo_Attribution_DirectMessage(t *testing.T) {
	dir := t.TempDir()
	initTestRepo(t, dir)

	gitCommitFile(t, dir, "a.go", "package a", "(bug-12345678) fix the thing")

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	inserted, attributed, err := ingestCommitsFromRepo(database, dir, "", 0)
	if err != nil {
		t.Fatalf("ingestCommitsFromRepo: %v", err)
	}
	if inserted < 1 {
		t.Errorf("expected >=1 inserted, got %d", inserted)
	}
	if attributed < 1 {
		t.Errorf("expected >=1 attributed, got %d", attributed)
	}

	var count int
	database.QueryRow(
		`SELECT COUNT(*) FROM git_commits WHERE feature_id = 'bug-12345678'`,
	).Scan(&count)
	if count < 1 {
		t.Error("expected feature_id=bug-12345678 in git_commits")
	}
}

// openInMemoryDB is a helper to open an in-memory DB in tests.
func openInMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}
