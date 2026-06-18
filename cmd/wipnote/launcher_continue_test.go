package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/internal/launcher"
)

func TestResolveContinueLaunchContext_HarnessPolicies(t *testing.T) {
	for _, tc := range []struct {
		name            string
		currentHarness  string
		previousHarness string
		wantResumeID    bool
	}{
		{name: "claude reuses transcript resume", currentHarness: "claude", previousHarness: "claude", wantResumeID: true},
		{name: "codex reuses transcript resume", currentHarness: "codex", previousHarness: "codex", wantResumeID: true},
		{name: "gemini stays fresh", currentHarness: "gemini", previousHarness: "gemini", wantResumeID: false},
		{name: "antigravity stays fresh", currentHarness: "antigravity", previousHarness: "antigravity", wantResumeID: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, database := openContinueTestProject(t)
			seedContinueFixture(t, database, projectRoot, tc.previousHarness, "feat-continue", "sess-continue", ".claude/worktrees/feat-continue")

			got, err := resolveContinueLaunchContext(projectRoot, projectRoot, tc.currentHarness, launcher.ContinueWorkIntent(
				"feat-continue", tc.previousHarness, "sess-continue", ".claude/worktrees/feat-continue", true,
			))
			if err != nil {
				t.Fatalf("resolveContinueLaunchContext: %v", err)
			}
			if got.WorkItemID != "feat-continue" {
				t.Fatalf("WorkItemID = %q, want feat-continue", got.WorkItemID)
			}
			if want := filepath.Join(projectRoot, ".claude", "worktrees", "feat-continue"); got.WorktreePath != want {
				t.Fatalf("WorktreePath = %q, want %q", got.WorktreePath, want)
			}
			if tc.wantResumeID && got.TranscriptResumeID != "sess-continue" {
				t.Fatalf("TranscriptResumeID = %q, want sess-continue", got.TranscriptResumeID)
			}
			if !tc.wantResumeID && got.TranscriptResumeID != "" {
				t.Fatalf("TranscriptResumeID = %q, want empty", got.TranscriptResumeID)
			}
			if got.ContinuedFrom != "sess-continue" {
				t.Fatalf("ContinuedFrom = %q, want sess-continue", got.ContinuedFrom)
			}
			env := strings.Join(got.ExtraEnv(), "\n")
			if !strings.Contains(env, continuedFromEnvVar+"=sess-continue") {
				t.Fatalf("continue env missing continued_from: %v", got.ExtraEnv())
			}
			if !strings.Contains(got.HandoffMarkdown, "Continued Session Handoff") {
				t.Fatalf("handoff markdown missing header: %q", got.HandoffMarkdown)
			}
		})
	}
}

func TestResolveContinueLaunchContext_MissingWorktreeFallsBackFresh(t *testing.T) {
	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "claude", "feat-missing", "sess-missing", ".claude/worktrees/feat-missing")
	if err := os.RemoveAll(filepath.Join(projectRoot, ".claude", "worktrees", "feat-missing")); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "claude", launcher.ContinueWorkIntent(
		"feat-missing", "claude", "sess-missing", ".claude/worktrees/feat-missing", true,
	))
	if err != nil {
		t.Fatalf("resolveContinueLaunchContext: %v", err)
	}
	if got.WorktreePath != "" {
		t.Fatalf("WorktreePath = %q, want empty", got.WorktreePath)
	}
	if !containsWarning(got.Warnings, "missing or unavailable") {
		t.Fatalf("warnings = %v, want missing-worktree warning", got.Warnings)
	}
}

func TestResolveContinueLaunchContext_LiveCollisionDisablesTranscriptResume(t *testing.T) {
	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "claude", "feat-live", "sess-live-base", ".claude/worktrees/feat-live")

	now := time.Now().UTC().Format(time.RFC3339)
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:       "sess-live-other",
		AgentAssigned:   "claude-code",
		Status:          "active",
		CreatedAt:       time.Now().UTC(),
		ActiveFeatureID: "feat-live",
		ProjectDir:      ".",
		Harness:         "claude",
	}); err != nil {
		t.Fatalf("insert live session: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO claims (
			claim_id, work_item_id, owner_session_id, owner_agent, status,
			leased_at, lease_expires_at, last_heartbeat_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"claim-live-001", "feat-live", "sess-live-other", "claude-code", "claimed",
		now, now, now, now, now,
	); err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "claude", launcher.ContinueWorkIntent(
		"feat-live", "claude", "sess-live-base", ".claude/worktrees/feat-live", true,
	))
	if err != nil {
		t.Fatalf("resolveContinueLaunchContext: %v", err)
	}
	if got.TranscriptResumeID != "" {
		t.Fatalf("TranscriptResumeID = %q, want empty on live collision", got.TranscriptResumeID)
	}
	if !containsWarning(got.Warnings, "still live in session sess-live-other") {
		t.Fatalf("warnings = %v, want live-collision warning", got.Warnings)
	}
}

func TestResolveContinueLaunchContext_HonorsSelectedResumeSessionID(t *testing.T) {
	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "codex", "feat-picked", "sess-picked", ".claude/worktrees/feat-picked")
	seedContinueFixture(t, database, projectRoot, "claude", "feat-picked", "sess-newer-cross", ".claude/worktrees/feat-cross")
	if _, err := database.Exec(`UPDATE sessions SET created_at = ? WHERE session_id = ?`,
		time.Now().UTC().Add(time.Minute).Format(time.RFC3339), "sess-newer-cross"); err != nil {
		t.Fatalf("update cross session created_at: %v", err)
	}

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "codex", launcher.ContinueWorkIntent(
		"feat-picked", "codex", "sess-picked", ".claude/worktrees/feat-picked", true,
	))
	if err != nil {
		t.Fatalf("resolveContinueLaunchContext: %v", err)
	}
	if got.ContinuedFrom != "sess-picked" {
		t.Fatalf("ContinuedFrom = %q, want sess-picked", got.ContinuedFrom)
	}
	if got.TranscriptResumeID != "sess-picked" {
		t.Fatalf("TranscriptResumeID = %q, want sess-picked", got.TranscriptResumeID)
	}
	if strings.Contains(got.HandoffMarkdown, "sess-newer-cross") {
		t.Fatalf("handoff markdown used newer cross-harness session:\n%s", got.HandoffMarkdown)
	}
}

func TestResolveContinueLaunchContext_HonorsSelectedResumeSessionIDFromActiveWorkItems(t *testing.T) {
	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "codex", "feat-picked-awi", "sess-picked-awi", ".claude/worktrees/feat-picked-awi")
	if _, err := database.Exec(`UPDATE sessions SET active_feature_id = '' WHERE session_id = ?`, "sess-picked-awi"); err != nil {
		t.Fatalf("clear active_feature_id: %v", err)
	}
	if err := dbpkg.SetActiveWorkItem(database, "sess-picked-awi", dbpkg.AgentRootSentinel, "feat-picked-awi"); err != nil {
		t.Fatalf("SetActiveWorkItem: %v", err)
	}
	seedContinueFixture(t, database, projectRoot, "claude", "feat-picked-awi", "sess-newer-cross-awi", ".claude/worktrees/feat-cross-awi")
	if _, err := database.Exec(`UPDATE sessions SET created_at = ? WHERE session_id = ?`,
		time.Now().UTC().Add(time.Minute).Format(time.RFC3339), "sess-newer-cross-awi"); err != nil {
		t.Fatalf("update cross session created_at: %v", err)
	}

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "codex", launcher.ContinueWorkIntent(
		"feat-picked-awi", "codex", "sess-picked-awi", ".claude/worktrees/feat-picked-awi", true,
	))
	if err != nil {
		t.Fatalf("resolveContinueLaunchContext: %v", err)
	}
	if got.ContinuedFrom != "sess-picked-awi" {
		t.Fatalf("ContinuedFrom = %q, want sess-picked-awi", got.ContinuedFrom)
	}
	if got.TranscriptResumeID != "sess-picked-awi" {
		t.Fatalf("TranscriptResumeID = %q, want sess-picked-awi", got.TranscriptResumeID)
	}
	if strings.Contains(got.HandoffMarkdown, "sess-newer-cross-awi") {
		t.Fatalf("handoff markdown used newer cross-harness session:\n%s", got.HandoffMarkdown)
	}
}

func openContinueTestProject(t *testing.T) (string, *sql.DB) {
	t.Helper()
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", filepath.Join(projectRoot, "cache", "wipnote.db"))
	database, err := openDB(filepath.Join(projectRoot, ".wipnote"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return projectRoot, database
}

func seedContinueFixture(t *testing.T, database *sql.DB, projectRoot, harness, workItemID, sessionID, worktreeRel string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.Exec(`
		INSERT INTO features (id, title, type, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		workItemID, "Continue Test", "feature", "in-progress", now, now,
	); err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, filepath.FromSlash(worktreeRel)), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:        sessionID,
		AgentAssigned:    harness + "-cli",
		Status:           "completed",
		CreatedAt:        time.Now().UTC(),
		ActiveFeatureID:  workItemID,
		ProjectDir:       ".",
		ExecWorktreePath: worktreeRel,
		Branch:           workItemID,
		Harness:          harness,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE sessions
		SET handoff_notes = ?, recommended_next = ?, blockers = ?
		WHERE session_id = ?`,
		"Finish the continue path wiring.",
		"Resume in the existing worktree.",
		`["FAIL: stale worktree path"]`,
		sessionID,
	); err != nil {
		t.Fatalf("update session handoff: %v", err)
	}
}

func containsWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return true
		}
	}
	return false
}
