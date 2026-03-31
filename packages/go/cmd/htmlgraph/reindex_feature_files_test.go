package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// initBareGitRepo creates a minimal git repo in dir with one commit touching files.
// Returns the commit hash.
func initBareGitRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		run("add", name)
	}

	run("commit", "-m", "initial commit")

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// insertGitCommitRow directly inserts a git_commits row for testing.
func insertGitCommitRow(t *testing.T, db *sql.DB, featureID, commitHash string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT OR IGNORE INTO git_commits (commit_hash, session_id, feature_id, message, timestamp)
		VALUES (?, 'test-session', ?, 'test commit', ?)`,
		commitHash, featureID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert git_commit: %v", err)
	}
}

// insertFeatureRow inserts a minimal feature row so FK constraints are satisfied.
func insertFeatureRow(t *testing.T, db *sql.DB, featureID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := dbpkg.UpsertFeature(db, &dbpkg.Feature{
		ID: featureID, Type: "feature", Title: fmt.Sprintf("Feature %s", featureID),
		Status: "todo", Priority: "medium", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertFeature %s: %v", featureID, err)
	}
}

// TestReindexFeatureFiles_PopulatesFromCommit verifies that files touched by a
// commit linked to a feature are upserted into feature_files.
func TestReindexFeatureFiles_PopulatesFromCommit(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a git repo with two source files.
	commitHash := initBareGitRepo(t, tmpDir, map[string]string{
		"src/foo.go": "package foo\n",
		"src/bar.go": "package bar\n",
	})

	// Open an in-memory DB with schema.
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	featureID := "feat-test-001"
	insertFeatureRow(t, database, featureID)
	insertGitCommitRow(t, database, featureID, commitHash)

	count, err := reindexFeatureFiles(database, tmpDir)
	if err != nil {
		t.Fatalf("reindexFeatureFiles: %v", err)
	}
	if count < 2 {
		t.Errorf("expected >= 2 file associations, got %d", count)
	}

	rows, err := dbpkg.ListFilesByFeature(database, featureID)
	if err != nil {
		t.Fatalf("ListFilesByFeature: %v", err)
	}

	paths := make(map[string]bool)
	for _, r := range rows {
		paths[r.FilePath] = true
	}
	if !paths["src/foo.go"] {
		t.Errorf("src/foo.go not found in feature_files; got %v", paths)
	}
	if !paths["src/bar.go"] {
		t.Errorf("src/bar.go not found in feature_files; got %v", paths)
	}
}

// TestReindexFeatureFiles_OperationIsCommit verifies that git-derived entries
// have operation="commit".
func TestReindexFeatureFiles_OperationIsCommit(t *testing.T) {
	tmpDir := t.TempDir()
	commitHash := initBareGitRepo(t, tmpDir, map[string]string{
		"main.go": "package main\n",
	})

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	featureID := "feat-test-002"
	insertFeatureRow(t, database, featureID)
	insertGitCommitRow(t, database, featureID, commitHash)

	if _, err := reindexFeatureFiles(database, tmpDir); err != nil {
		t.Fatalf("reindexFeatureFiles: %v", err)
	}

	rows, err := dbpkg.ListFilesByFeature(database, featureID)
	if err != nil {
		t.Fatalf("ListFilesByFeature: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no feature_files rows found")
	}
	for _, r := range rows {
		if r.Operation != "commit" {
			t.Errorf("operation: got %q, want %q", r.Operation, "commit")
		}
	}
}

// TestReindexFeatureFiles_MissingCommitSkipped verifies that a non-existent
// commit hash is skipped without returning an error.
func TestReindexFeatureFiles_MissingCommitSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	// Init a valid repo but reference a bogus hash.
	initBareGitRepo(t, tmpDir, map[string]string{"placeholder": "x"})

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	featureID := "feat-test-003"
	insertFeatureRow(t, database, featureID)
	insertGitCommitRow(t, database, featureID, "deadbeefdeadbeefdeadbeefdeadbeef12345678")

	count, err := reindexFeatureFiles(database, tmpDir)
	if err != nil {
		t.Fatalf("reindexFeatureFiles returned error for missing commit: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 associations for missing commit, got %d", count)
	}
}

// TestReindexFeatureFiles_EmptyTable verifies no error when git_commits is empty.
func TestReindexFeatureFiles_EmptyTable(t *testing.T) {
	tmpDir := t.TempDir()

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	count, err := reindexFeatureFiles(database, tmpDir)
	if err != nil {
		t.Fatalf("reindexFeatureFiles on empty table: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

// TestReindexFeatureFiles_MultipleFeatures verifies correct feature_id linkage
// when multiple features have commits in the same repo.
func TestReindexFeatureFiles_MultipleFeatures(t *testing.T) {
	tmpDir := t.TempDir()

	// First commit.
	hash1 := initBareGitRepo(t, tmpDir, map[string]string{
		"alpha.go": "package alpha\n",
	})

	// Second commit on the same repo.
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Run()
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "beta.go"), []byte("package beta\n"), 0o644); err != nil {
		t.Fatalf("write beta.go: %v", err)
	}
	run("add", "beta.go")
	run("commit", "-m", "second commit")
	out, _ := exec.Command("git", "-C", tmpDir, "rev-parse", "HEAD").Output()
	hash2 := strings.TrimSpace(string(out))

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	insertFeatureRow(t, database, "feat-alpha")
	insertFeatureRow(t, database, "feat-beta")
	insertGitCommitRow(t, database, "feat-alpha", hash1)
	insertGitCommitRow(t, database, "feat-beta", hash2)

	count, err := reindexFeatureFiles(database, tmpDir)
	if err != nil {
		t.Fatalf("reindexFeatureFiles: %v", err)
	}
	if count < 2 {
		t.Errorf("expected >= 2 total associations, got %d", count)
	}

	// feat-alpha should have alpha.go; feat-beta should have beta.go.
	alphaFiles, _ := dbpkg.ListFilesByFeature(database, "feat-alpha")
	betaFiles, _ := dbpkg.ListFilesByFeature(database, "feat-beta")

	hasFile := func(rows []models.FeatureFile, name string) bool {
		for _, r := range rows {
			if r.FilePath == name {
				return true
			}
		}
		return false
	}

	if !hasFile(alphaFiles, "alpha.go") {
		t.Errorf("feat-alpha missing alpha.go")
	}
	if !hasFile(betaFiles, "beta.go") {
		t.Errorf("feat-beta missing beta.go")
	}
}

// TestSanitizePathID verifies the path sanitizer produces safe, truncated tokens.
func TestSanitizePathID(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"src/foo.go", "src-foo-go"},
		{"a/b/c.go", "a-b-c-go"},
		{"simple", "simple"},
		{strings.Repeat("x", 40), strings.Repeat("x", 32)},
	}
	for _, tc := range cases {
		got := sanitizePathID(tc.input)
		if got != tc.want {
			t.Errorf("sanitizePathID(%q) = %q, want %q", tc.input, got, tc.want)
		}
		if len(got) > 32 {
			t.Errorf("sanitizePathID(%q) too long: %d chars", tc.input, len(got))
		}
	}
}
