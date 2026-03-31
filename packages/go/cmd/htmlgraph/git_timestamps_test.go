package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// setupGitRepo initialises a throwaway git repo in a temp dir, creates an
// initial commit, and returns the repo root path.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	// Create an initial commit so git log works in all tests.
	run("commit", "--allow-empty", "-m", "init")
	return dir
}

// commitFile writes content to path inside repoDir and commits it.
// Returns the commit time (truncated to second for comparison).
func commitFile(t *testing.T, repoDir, relPath, content string) time.Time {
	t.Helper()
	fullPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", relPath)
	run("commit", "--allow-empty-message", "-m", "add "+relPath)

	// Capture the commit timestamp.
	out, err := exec.Command("git", "-C", repoDir, "log", "-1", "--format=%aI").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	ts, err := parseGitTimestamp(string([]byte(out[:len(out)-1]))) // strip newline
	if err != nil {
		t.Fatalf("parseGitTimestamp: %v", err)
	}
	return ts.Truncate(time.Second)
}

// --- parseGitTimestamp ---

func TestParseGitTimestamp_Valid(t *testing.T) {
	cases := []struct {
		input string
		wantY int
		wantM time.Month
		wantD int
	}{
		{"2024-01-15T10:30:00+00:00", 2024, time.January, 15},
		{"2025-06-01T00:00:00+05:30", 2025, time.June, 1},
		{"2023-12-31T23:59:59-08:00", 2023, time.December, 31},
	}
	for _, tc := range cases {
		got, err := parseGitTimestamp(tc.input)
		if err != nil {
			t.Errorf("parseGitTimestamp(%q) error: %v", tc.input, err)
			continue
		}
		local := got.In(time.UTC)
		// Allow for timezone offset shifting the date.
		if local.Year() < 2023 || local.Year() > 2025 {
			t.Errorf("parseGitTimestamp(%q): year %d out of range", tc.input, local.Year())
		}
	}
}

func TestParseGitTimestamp_Invalid(t *testing.T) {
	_, err := parseGitTimestamp("not-a-date")
	if err == nil {
		t.Error("expected error for invalid timestamp, got nil")
	}
}

// --- gitLastModified ---

func TestGitLastModified_UntrackedFile(t *testing.T) {
	repoDir := setupGitRepo(t)
	// Write a file but don't commit it.
	path := filepath.Join(repoDir, "untracked.html")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts, err := gitLastModified(repoDir, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time for untracked file, got %v", ts)
	}
}

func TestGitLastModified_CommittedFile(t *testing.T) {
	repoDir := setupGitRepo(t)
	commitTime := commitFile(t, repoDir, "feat.html", "v1")

	path := filepath.Join(repoDir, "feat.html")
	got, err := gitLastModified(repoDir, path)
	if err != nil {
		t.Fatalf("gitLastModified: %v", err)
	}
	if got.IsZero() {
		t.Fatal("expected non-zero time for committed file")
	}
	if got.Truncate(time.Second) != commitTime {
		t.Errorf("got %v, want ~%v", got, commitTime)
	}
}

// --- gitFirstAdded ---

func TestGitFirstAdded_UntrackedFile(t *testing.T) {
	repoDir := setupGitRepo(t)
	path := filepath.Join(repoDir, "new.html")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts, err := gitFirstAdded(repoDir, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time for untracked file, got %v", ts)
	}
}

func TestGitFirstAdded_SingleCommit(t *testing.T) {
	repoDir := setupGitRepo(t)
	addTime := commitFile(t, repoDir, "item.html", "initial")

	path := filepath.Join(repoDir, "item.html")
	got, err := gitFirstAdded(repoDir, path)
	if err != nil {
		t.Fatalf("gitFirstAdded: %v", err)
	}
	if got.IsZero() {
		t.Fatal("expected non-zero time")
	}
	if got.Truncate(time.Second) != addTime {
		t.Errorf("got %v, want %v", got, addTime)
	}
}

func TestGitFirstAdded_MultipleCommits_ReturnsOldest(t *testing.T) {
	repoDir := setupGitRepo(t)
	addTime := commitFile(t, repoDir, "item.html", "v1")
	// Second commit modifies the same file.
	_ = commitFile(t, repoDir, "item.html", "v2")

	path := filepath.Join(repoDir, "item.html")
	got, err := gitFirstAdded(repoDir, path)
	if err != nil {
		t.Fatalf("gitFirstAdded: %v", err)
	}
	if got.Truncate(time.Second) != addTime {
		t.Errorf("gitFirstAdded should return oldest commit: got %v, want %v", got, addTime)
	}
}

// --- gitFileTimestamps ---

func TestGitFileTimestamps_UntrackedFile(t *testing.T) {
	repoDir := setupGitRepo(t)
	path := filepath.Join(repoDir, "new.html")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	created, updated, err := gitFileTimestamps(repoDir, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created.IsZero() || !updated.IsZero() {
		t.Errorf("expected zero times for untracked file, got created=%v updated=%v", created, updated)
	}
}

func TestGitFileTimestamps_SingleCommit(t *testing.T) {
	repoDir := setupGitRepo(t)
	ts := commitFile(t, repoDir, "feat.html", "v1")

	path := filepath.Join(repoDir, "feat.html")
	created, updated, err := gitFileTimestamps(repoDir, path)
	if err != nil {
		t.Fatalf("gitFileTimestamps: %v", err)
	}
	if created.IsZero() || updated.IsZero() {
		t.Fatal("expected non-zero timestamps for committed file")
	}
	if created.Truncate(time.Second) != ts {
		t.Errorf("created: got %v, want %v", created, ts)
	}
	if updated.Truncate(time.Second) != ts {
		t.Errorf("updated: got %v, want %v", updated, ts)
	}
}

func TestGitFileTimestamps_TwoCommits_CreatedIsOldest(t *testing.T) {
	repoDir := setupGitRepo(t)
	firstTime := commitFile(t, repoDir, "feat.html", "v1")
	secondTime := commitFile(t, repoDir, "feat.html", "v2")

	path := filepath.Join(repoDir, "feat.html")
	created, updated, err := gitFileTimestamps(repoDir, path)
	if err != nil {
		t.Fatalf("gitFileTimestamps: %v", err)
	}
	if created.Truncate(time.Second) != firstTime {
		t.Errorf("created: got %v, want %v (first commit)", created, firstTime)
	}
	if updated.Truncate(time.Second) != secondTime {
		t.Errorf("updated: got %v, want %v (second commit)", updated, secondTime)
	}
	if !created.Before(updated) && created != updated {
		t.Errorf("created should not be after updated: created=%v updated=%v", created, updated)
	}
}

// --- applyGitTimestamps ---

func TestApplyGitTimestamps_UntrackedFallsBackToHTML(t *testing.T) {
	repoDir := setupGitRepo(t)
	path := filepath.Join(repoDir, "new.html")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	htmlCreated := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	htmlUpdated := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	created, updated := applyGitTimestamps(repoDir, path, htmlCreated, htmlUpdated)
	if !created.Equal(htmlCreated) {
		t.Errorf("created: got %v, want HTML fallback %v", created, htmlCreated)
	}
	if !updated.Equal(htmlUpdated) {
		t.Errorf("updated: got %v, want HTML fallback %v", updated, htmlUpdated)
	}
}

func TestApplyGitTimestamps_CommittedOverridesHTML(t *testing.T) {
	repoDir := setupGitRepo(t)
	gitTime := commitFile(t, repoDir, "feat.html", "v1")

	path := filepath.Join(repoDir, "feat.html")
	// Provide stale HTML timestamps (simulating drifted data-created/data-updated).
	htmlCreated := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	htmlUpdated := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)

	created, updated := applyGitTimestamps(repoDir, path, htmlCreated, htmlUpdated)

	// Git timestamps should win over stale HTML attributes.
	if created.Truncate(time.Second) != gitTime {
		t.Errorf("created: got %v, want git time %v", created, gitTime)
	}
	if updated.Truncate(time.Second) != gitTime {
		t.Errorf("updated: got %v, want git time %v", updated, gitTime)
	}
}

func TestApplyGitTimestamps_NonGitDir_FallsBackToHTML(t *testing.T) {
	dir := t.TempDir() // not a git repo
	path := filepath.Join(dir, "feat.html")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	htmlCreated := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
	htmlUpdated := time.Date(2024, 3, 16, 0, 0, 0, 0, time.UTC)

	created, updated := applyGitTimestamps(dir, path, htmlCreated, htmlUpdated)
	if !created.Equal(htmlCreated) {
		t.Errorf("non-git dir: created %v, want HTML fallback %v", created, htmlCreated)
	}
	if !updated.Equal(htmlUpdated) {
		t.Errorf("non-git dir: updated %v, want HTML fallback %v", updated, htmlUpdated)
	}
}
