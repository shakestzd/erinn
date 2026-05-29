package hooks

import (
	"database/sql"
	"testing"

	"github.com/shakestzd/wipnote/internal/db"
)

// TestExtractBranchFromWorktreePath verifies the worktree path → branch name extraction.
func TestExtractBranchFromWorktreePath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/repo/.claude/worktrees/trk-abc12345", "trk-abc12345"},
		{"/repo/.claude/worktrees/feat-abc12345", "feat-abc12345"},
		{"/repo/.claude/worktrees/bug-abc12345", "bug-abc12345"},
		{"/repo/worktrees/my-branch", "my-branch"},
		{"trk-abc12345", "trk-abc12345"},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractBranchFromWorktreePath(tt.path)
		if got != tt.want {
			t.Errorf("extractBranchFromWorktreePath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestLooksLikeGitMerge checks the merge command detector.
func TestLooksLikeGitMerge(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"git merge trk-abc12345", true},
		{"git merge --no-ff feat-abc12345", true},
		{"git-merge main", true},
		{"git commit -m msg", false},
		{"git log", false},
	}
	for _, tt := range tests {
		if got := looksLikeGitMerge(tt.cmd); got != tt.want {
			t.Errorf("looksLikeGitMerge(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

// TestExtractMergeBranch verifies branch extraction from merge commands.
func TestExtractMergeBranch(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"git merge trk-abc12345", "trk-abc12345"},
		{"git merge --no-ff trk-abc12345", "trk-abc12345"},
		{"git merge feat-abc12345", "feat-abc12345"},
		{"git merge main", "main"},
		{"git merge --squash --no-ff feat-abc12345", "feat-abc12345"},
		{"git log", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractMergeBranch(tt.cmd)
		if got != tt.want {
			t.Errorf("extractMergeBranch(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

// TestWorkItemBranchRe verifies the regex that identifies direct work item branches.
func TestWorkItemBranchRe(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"feat-aabbccdd", true},
		{"bug-12345678", true},
		{"spk-abcdef01", true},
		{"trk-abc12345", false}, // track, not direct work item
		{"main", false},
		{"feature/my-feature", false},
		{"feat-abc", false}, // too short
	}
	for _, tt := range tests {
		got := workItemBranchRe.MatchString(tt.branch)
		if got != tt.want {
			t.Errorf("workItemBranchRe.MatchString(%q) = %v, want %v", tt.branch, got, tt.want)
		}
	}
}

// TestTrackBranchRe verifies the regex that identifies track branches.
func TestTrackBranchRe(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"trk-abc12345", true},
		{"trk-deadbeef", true},
		{"feat-abc12345", false},
		{"main", false},
		{"trk-abc", false}, // too short
	}
	for _, tt := range tests {
		got := trackBranchRe.MatchString(tt.branch)
		if got != tt.want {
			t.Errorf("trackBranchRe.MatchString(%q) = %v, want %v", tt.branch, got, tt.want)
		}
	}
}

// TestAutoCompleteByBranch_GenericBranch verifies that generic branch names
// (like "main") don't trigger any completion.
func TestAutoCompleteByBranch_GenericBranch(t *testing.T) {
	td := setupTestDB(t)
	td.addFeature("feat-aabbccdd", "feature", "Some feature", "in-progress")

	// "main" doesn't match any work item or track pattern → nil immediately.
	completed := autoCompleteByBranch("main", td.DB)
	if len(completed) != 0 {
		t.Errorf("expected no completions for branch 'main', got %v", completed)
	}

	// Verify feature is untouched.
	var status string
	if err := td.DB.QueryRow(`SELECT status FROM features WHERE id = ?`, "feat-aabbccdd").Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "in-progress" {
		t.Errorf("feature should remain in-progress, got %q", status)
	}
}

// TestAutoCompleteByBranch_EmptyBranch verifies graceful handling of empty branches.
func TestAutoCompleteByBranch_EmptyBranch(t *testing.T) {
	td := setupTestDB(t)
	completed := autoCompleteByBranch("", td.DB)
	if len(completed) != 0 {
		t.Errorf("expected no completions for empty branch, got %v", completed)
	}
}

// TestCompleteInProgressByTrack_Query verifies that the SQL query used by
// completeInProgressByTrack selects the correct set of in-progress items.
func TestCompleteInProgressByTrack_Query(t *testing.T) {
	td := setupTestDB(t)
	td.addTrack("trk-abc12345", "Auth Track")
	td.addTrack("trk-xxxxxxxx", "Other Track")

	insertWithTrack := func(id, status, trackID string) {
		t.Helper()
		feat := &db.Feature{
			ID:       id,
			Type:     "feature",
			Title:    id,
			Status:   status,
			Priority: "medium",
			TrackID:  trackID,
		}
		if err := db.InsertFeature(td.DB, feat); err != nil {
			t.Fatalf("InsertFeature(%s): %v", id, err)
		}
	}

	insertWithTrack("feat-11111111", "in-progress", "trk-abc12345")
	insertWithTrack("feat-22222222", "in-progress", "trk-abc12345")
	insertWithTrack("feat-33333333", "todo", "trk-abc12345")        // not in-progress
	insertWithTrack("feat-44444444", "in-progress", "trk-xxxxxxxx") // different track

	// Query the same SQL used by completeInProgressByTrack.
	rows, err := td.DB.Query(
		`SELECT id FROM features WHERE track_id = ? AND status = 'in-progress'`,
		"trk-abc12345",
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	if len(ids) != 2 {
		t.Errorf("expected 2 in-progress items for track, got %d: %v", len(ids), ids)
	}
	for _, id := range ids {
		if id != "feat-11111111" && id != "feat-22222222" {
			t.Errorf("unexpected id in track query result: %q", id)
		}
	}
}

// TestWorktreeRemove_AutoCompletes verifies WorktreeRemove auto-completes the
// in-progress work item for the removed worktree's branch and records the
// checkpoint event. (feat-cd937fc9: the dead additionalContext return was
// removed — CC ignores stdout for this Observational event.)
func TestWorktreeRemove_AutoCompletes(t *testing.T) {
	// Stub out the CLI shell-out so the test does not require a real wipnote
	// binary or a project DB at a known path. The stub marks the item done
	// in-process by updating the DB directly, matching what the CLI would do.
	orig := completeIfInProgressFn
	completeIfInProgressFn = func(id string, database *sql.DB) bool {
		var status string
		if err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, id).Scan(&status); err != nil {
			return false
		}
		if status != "in-progress" {
			return false
		}
		_, err := database.Exec(`UPDATE features SET status = 'done' WHERE id = ?`, id)
		return err == nil
	}
	t.Cleanup(func() { completeIfInProgressFn = orig })

	database, projectDir := setupLifecycleDB(t)

	sessionID := "wt-remove-test-001"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES (?, ?, ?, datetime('now'))`,
		sessionID, "claude-code", "active",
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert a track and an in-progress feature on it.
	_, err = database.Exec(
		`INSERT INTO tracks (id, title, status, created_at, updated_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))`,
		"trk-deadbeef", "Test Track", "in-progress",
	)
	if err != nil {
		t.Fatalf("insert track: %v", err)
	}
	feat := &db.Feature{
		ID:       "feat-cafebabe",
		Type:     "feature",
		Title:    "Test Feature",
		Status:   "in-progress",
		Priority: "medium",
		TrackID:  "trk-deadbeef",
	}
	if err := db.InsertFeature(database, feat); err != nil {
		t.Fatalf("insert feature: %v", err)
	}

	event := &CloudEvent{
		SessionID:    sessionID,
		CWD:          projectDir,
		WorktreePath: "/repo/.claude/worktrees/trk-deadbeef",
	}

	result, err := WorktreeRemove(event, database)
	if err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// CC ignores stdout for the Observational WorktreeRemove event, so the
	// handler must NOT emit an additionalContext (dead return removed).
	if result.AdditionalContext != "" {
		t.Errorf("expected no additionalContext, got: %s", result.AdditionalContext)
	}
	// The real side effect: the in-progress feature on the branch is completed.
	var status string
	if err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, "feat-cafebabe").Scan(&status); err != nil {
		t.Fatalf("query feature status: %v", err)
	}
	if status != "done" {
		t.Errorf("expected feature auto-completed (status=done), got %q", status)
	}
}

// TestWorktreeRemove_UnknownBranch verifies WorktreeRemove handles a non-work-item
// branch without error and still provides the standard relocation context.
func TestWorktreeRemove_UnknownBranch(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)

	sessionID := "wt-remove-unknown-001"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES (?, ?, ?, datetime('now'))`,
		sessionID, "claude-code", "active",
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	event := &CloudEvent{
		SessionID:    sessionID,
		CWD:          projectDir,
		WorktreePath: "/repo/.claude/worktrees/some-feature-branch",
	}

	result, err := WorktreeRemove(event, database)
	if err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// CC ignores stdout for the Observational WorktreeRemove event; the handler
	// no longer emits an additionalContext (feat-cd937fc9 cleanup).
	if result.AdditionalContext != "" {
		t.Errorf("expected no additionalContext, got: %s", result.AdditionalContext)
	}
}
