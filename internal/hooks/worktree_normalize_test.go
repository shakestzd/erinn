package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/paths"
)

// TestWorktreeNormalize_AbsoluteInsideRepo_StoredRelative verifies that a
// WorktreeCreate event with an absolute path inside the repo stores the
// normalized relative path in input_summary.
func TestWorktreeNormalize_AbsoluteInsideRepo_StoredRelative(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)
	repoRoot := setupGitRepoForWorktreeCreate(t)
	basePath := filepath.Join(repoRoot, ".claude", "worktrees")
	worktreeName := "foo-12345"

	// Override the package-level resolver so the test does not shell to git.
	// The resolver returns the repo root for any path under the temp repo.
	paths.ResetNormalizeCacheForTesting()
	old := worktreePathResolver
	worktreePathResolver = func(dir string) string {
		if strings.HasPrefix(dir, repoRoot) {
			return repoRoot
		}
		return ""
	}
	t.Cleanup(func() {
		worktreePathResolver = old
		paths.ResetNormalizeCacheForTesting()
	})

	event := &CloudEvent{
		SessionID:        sessionID,
		CWD:              repoRoot,
		WorktreeBasePath: basePath,
		WorktreeName:     worktreeName,
	}

	got, err := WorktreeCreate(event, td.DB)
	if err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}
	if got != filepath.Join(basePath, worktreeName) {
		t.Fatalf("WorktreeCreate path = %q, want %q", got, filepath.Join(basePath, worktreeName))
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeCreate'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	want := "Worktree created: .claude/worktrees/foo-12345"
	if inputSummary != want {
		t.Errorf("input_summary = %q, want %q", inputSummary, want)
	}
}

// TestWorktreeNormalize_AlreadyRelative_Unchanged verifies that a
// WorktreePath that is already relative is stored unchanged.
func TestWorktreeNormalize_AlreadyRelative_Unchanged(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	paths.ResetNormalizeCacheForTesting()
	old := worktreePathResolver
	worktreePathResolver = func(dir string) string { return "/repo" }
	t.Cleanup(func() {
		worktreePathResolver = old
		paths.ResetNormalizeCacheForTesting()
	})

	event := &CloudEvent{
		SessionID:    sessionID,
		CWD:          t.TempDir(),
		WorktreePath: ".claude/worktrees/feat-already",
	}

	_, err := WorktreeRemove(event, td.DB)
	if err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeRemove'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	want := "Worktree removed: .claude/worktrees/feat-already"
	if inputSummary != want {
		t.Errorf("input_summary = %q, want %q", inputSummary, want)
	}
}

// TestWorktreeNormalize_InputSummaryContainsAbsPath_Normalized verifies that
// when WorktreeRemove is fired, any absolute path embedding in input_summary
// is replaced with the relative form.
func TestWorktreeNormalize_InputSummaryContainsAbsPath_Normalized(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	paths.ResetNormalizeCacheForTesting()
	old := worktreePathResolver
	worktreePathResolver = func(dir string) string {
		if strings.HasPrefix(dir, "/workspaces/repo") {
			return "/workspaces/repo"
		}
		return ""
	}
	t.Cleanup(func() {
		worktreePathResolver = old
		paths.ResetNormalizeCacheForTesting()
	})

	event := &CloudEvent{
		SessionID:    sessionID,
		CWD:          t.TempDir(),
		WorktreePath: "/workspaces/repo/.claude/worktrees/trk-abc12345",
	}

	_, err := WorktreeRemove(event, td.DB)
	if err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeRemove'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	want := "Worktree removed: .claude/worktrees/trk-abc12345"
	if inputSummary != want {
		t.Errorf("input_summary = %q, want %q", inputSummary, want)
	}
}

// TestWorktreeNormalize_ForeignCreatePath_StoredAbsolute verifies that a
// WorktreeCreate path outside the repo but outside known host-path prefixes is
// stored as the created absolute path.
func TestWorktreeNormalize_ForeignPath_MarkedUnresolved(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)
	repoRoot := setupGitRepoForWorktreeCreate(t)
	basePath, err := os.MkdirTemp("/tmp", "worktree-create-foreign-*")
	if err != nil {
		t.Fatalf("MkdirTemp foreign base: %v", err)
	}
	worktreeName := "feat-xyz"
	worktreePath := filepath.Join(basePath, worktreeName)

	// Resolver returns "" — path is outside any known repo.
	paths.ResetNormalizeCacheForTesting()
	old := worktreePathResolver
	worktreePathResolver = func(dir string) string { return "" }
	t.Cleanup(func() {
		worktreePathResolver = old
		paths.ResetNormalizeCacheForTesting()
	})

	event := &CloudEvent{
		SessionID:        sessionID,
		CWD:              repoRoot,
		WorktreeBasePath: basePath,
		WorktreeName:     worktreeName,
	}

	_, err = WorktreeCreate(event, td.DB)
	if err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeCreate'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	want := "Worktree created: " + worktreePath
	if inputSummary != want {
		t.Errorf("input_summary = %q, want %q", inputSummary, want)
	}
}

// TestWorktreeNormalize_EmptyPath_NoOp verifies that missing WorktreeCreate
// replacement fields return an error and do not record a generic checkpoint.
func TestWorktreeNormalize_EmptyPath_NoOp(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	paths.ResetNormalizeCacheForTesting()
	old := worktreePathResolver
	worktreePathResolver = func(dir string) string { return "/repo" }
	t.Cleanup(func() {
		worktreePathResolver = old
		paths.ResetNormalizeCacheForTesting()
	})

	event := &CloudEvent{
		SessionID:    sessionID,
		CWD:          t.TempDir(),
		WorktreePath: "",
	}

	if _, err := WorktreeCreate(event, td.DB); err == nil {
		t.Fatal("expected missing WorktreeCreate fields to error")
	}

	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeCreate'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query agent_events count: %v", err)
	}
	if count != 0 {
		t.Errorf("WorktreeCreate checkpoint count = %d, want 0", count)
	}
}
