package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/htmlgraph/internal/models"
)

func TestIsSweepCommit(t *testing.T) {
	tests := []struct {
		subject string
		want    bool
	}{
		{"chore: catch up work-item state", true},
		{"chore(otel): nudge metadata", true},
		{"1e4ba7d0 roborev metadata fix", true},
		{"metadata sweep across all work items", true},
		{"feat(stale): commit-activity heuristic (feat-bb06e3f6)", false},
		{"fix(otel): drop spans on sqlite busy (bug-6ab963d3)", false},
		{"docs: update README", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isSweepCommit(tt.subject)
		if got != tt.want {
			t.Errorf("isSweepCommit(%q) = %v, want %v", tt.subject, got, tt.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{2 * time.Hour, "2h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
		{360 * time.Hour, "15d"},
	}
	for _, tt := range tests {
		got := formatAge(tt.d)
		if got != tt.want {
			t.Errorf("formatAge(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// initTempGitRepo creates a temp directory with an initialized git repo
// and applies the given commits in order. Each commit is empty
// (--allow-empty) so the test does not need to manage tracked files.
func initTempGitRepo(t *testing.T, commits []testCommit) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "--initial-branch=main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	for _, c := range commits {
		ts := c.when.UTC().Format(time.RFC3339)
		cmd := exec.Command("git", "-C", dir,
			"commit", "--allow-empty", "-m", c.subject)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+ts,
			"GIT_COMMITTER_DATE="+ts,
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit %q: %v\n%s", c.subject, err, out)
		}
	}
	return dir
}

type testCommit struct {
	subject string
	when    time.Time
}

func TestGitCommitsReferencing_FindsByID(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dir := initTempGitRepo(t, []testCommit{
		{"feat(other): unrelated work", now.Add(-10 * 24 * time.Hour)},
		{"feat(stale): real work (feat-aaa11111)", now.Add(-2 * 24 * time.Hour)},
		{"chore: catch up work-item state for feat-aaa11111", now.Add(-1 * 24 * time.Hour)},
	})

	commits, err := gitCommitsReferencing(dir, "feat-aaa11111")
	if err != nil {
		t.Fatalf("gitCommitsReferencing: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2: %+v", len(commits), commits)
	}
	// Newest first
	if !strings.Contains(commits[0].Subject, "chore:") {
		t.Errorf("expected newest commit (sweep) first, got %q", commits[0].Subject)
	}
	if !strings.Contains(commits[1].Subject, "real work") {
		t.Errorf("expected oldest commit (real work) second, got %q", commits[1].Subject)
	}
}

func TestComputeStaleItems(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dir := initTempGitRepo(t, []testCommit{
		// fresh real work — should NOT be stale
		{"feat(fresh): work (feat-fresh01)", now.Add(-10 * time.Hour)},
		// old real work + recent sweep — should be stale (sweep ignored)
		{"feat(old): work (feat-old00001)", now.Add(-15 * 24 * time.Hour)},
		{"chore: catch up work-item state for feat-old00001", now.Add(-2 * time.Hour)},
		// no commits at all for feat-noref0001 — should be stale
	})

	items := []*models.Node{
		{ID: "feat-fresh01", Type: "feature", Title: "Fresh", Status: models.StatusInProgress, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "feat-old00001", Type: "feature", Title: "Old", Status: models.StatusInProgress, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "feat-noref0001", Type: "feature", Title: "No Refs", Status: models.StatusInProgress, UpdatedAt: now.Add(-200 * time.Hour)},
	}

	stale := computeStaleItems(dir, items, 72*time.Hour, false, now)

	gotIDs := map[string]bool{}
	for _, s := range stale {
		gotIDs[s.ItemID] = true
	}

	if gotIDs["feat-fresh01"] {
		t.Errorf("feat-fresh01 should NOT be stale (recent real commit)")
	}
	if !gotIDs["feat-old00001"] {
		t.Errorf("feat-old00001 should be stale (only sweep is recent)")
	}
	if !gotIDs["feat-noref0001"] {
		t.Errorf("feat-noref0001 should be stale (no commits at all)")
	}

	for _, s := range stale {
		if s.ItemID == "feat-old00001" {
			if s.Age < 14*24*time.Hour {
				t.Errorf("feat-old00001 age = %v, expected >= 14d (real commit, not sweep)", s.Age)
			}
			if !strings.Contains(s.Reason, "real commit") {
				t.Errorf("feat-old00001 reason = %q, want it to mention 'real commit'", s.Reason)
			}
		}
	}
}

func TestComputeStaleItems_IncludeSweeps(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dir := initTempGitRepo(t, []testCommit{
		{"feat(old): work (feat-old00001)", now.Add(-15 * 24 * time.Hour)},
		{"chore: catch up work-item state for feat-old00001", now.Add(-2 * time.Hour)},
	})

	items := []*models.Node{
		{ID: "feat-old00001", Type: "feature", Title: "Old", Status: models.StatusInProgress, UpdatedAt: now.Add(-2 * time.Hour)},
	}

	// With include-sweeps=true, the recent chore commit counts as activity → not stale
	stale := computeStaleItems(dir, items, 72*time.Hour, true, now)
	if len(stale) != 0 {
		t.Errorf("expected 0 stale items when including sweeps, got %d: %+v", len(stale), stale)
	}
}
