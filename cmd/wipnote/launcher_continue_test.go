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

// continueTestUUID is a valid Claude Code session UUID used in continue tests.
// isClaudeCodeSessionID must pass for TranscriptResumeID to be set.
const continueTestUUID = "019ee378-abcd-7000-8000-000000000001"

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
			seedContinueFixture(t, database, projectRoot, tc.previousHarness, "feat-continue", continueTestUUID, ".claude/worktrees/feat-continue")

			got, err := resolveContinueLaunchContext(projectRoot, projectRoot, tc.currentHarness, launcher.ContinueWorkIntent(
				"feat-continue", tc.previousHarness, continueTestUUID, ".claude/worktrees/feat-continue", true,
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
			if tc.wantResumeID && got.TranscriptResumeID != continueTestUUID {
				t.Fatalf("TranscriptResumeID = %q, want %q", got.TranscriptResumeID, continueTestUUID)
			}
			if !tc.wantResumeID && got.TranscriptResumeID != "" {
				t.Fatalf("TranscriptResumeID = %q, want empty", got.TranscriptResumeID)
			}
			if got.ContinuedFrom != continueTestUUID {
				t.Fatalf("ContinuedFrom = %q, want %q", got.ContinuedFrom, continueTestUUID)
			}
			env := strings.Join(got.ExtraEnv(), "\n")
			if !strings.Contains(env, continuedFromEnvVar+"="+continueTestUUID) {
				t.Fatalf("continue env missing continued_from: %v", got.ExtraEnv())
			}
			if !strings.Contains(got.HandoffMarkdown, "Continued Session Handoff") {
				t.Fatalf("handoff markdown missing header: %q", got.HandoffMarkdown)
			}
		})
	}
}

func TestResolveContinueLaunchContext_MissingWorktreeFallsBackFresh(t *testing.T) {
	const sessMissing = "019ee378-abcd-7000-8000-000000000002"
	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "claude", "feat-missing", sessMissing, ".claude/worktrees/feat-missing")
	if err := os.RemoveAll(filepath.Join(projectRoot, ".claude", "worktrees", "feat-missing")); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "claude", launcher.ContinueWorkIntent(
		"feat-missing", "claude", sessMissing, ".claude/worktrees/feat-missing", true,
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
	const (
		sessLiveBase  = "019ee378-abcd-7000-8000-000000000003"
		sessLiveOther = "019ee378-abcd-7000-8000-000000000004"
	)
	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "claude", "feat-live", sessLiveBase, ".claude/worktrees/feat-live")

	now := time.Now().UTC().Format(time.RFC3339)
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:       sessLiveOther,
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
		"claim-live-001", "feat-live", sessLiveOther, "claude-code", "claimed",
		now, now, now, now, now,
	); err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "claude", launcher.ContinueWorkIntent(
		"feat-live", "claude", sessLiveBase, ".claude/worktrees/feat-live", true,
	))
	if err != nil {
		t.Fatalf("resolveContinueLaunchContext: %v", err)
	}
	if got.TranscriptResumeID != "" {
		t.Fatalf("TranscriptResumeID = %q, want empty on live collision", got.TranscriptResumeID)
	}
	if !containsWarning(got.Warnings, "still live in session "+sessLiveOther) {
		t.Fatalf("warnings = %v, want live-collision warning", got.Warnings)
	}
}

func TestResolveContinueLaunchContext_HonorsSelectedResumeSessionID(t *testing.T) {
	const (
		sessPicked     = "019ee378-abcd-7000-8000-000000000005"
		sessNewerCross = "019ee378-abcd-7000-8000-000000000006"
	)
	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "codex", "feat-picked", sessPicked, ".claude/worktrees/feat-picked")
	seedContinueFixture(t, database, projectRoot, "claude", "feat-picked", sessNewerCross, ".claude/worktrees/feat-cross")
	if _, err := database.Exec(`UPDATE sessions SET created_at = ? WHERE session_id = ?`,
		time.Now().UTC().Add(time.Minute).Format(time.RFC3339), sessNewerCross); err != nil {
		t.Fatalf("update cross session created_at: %v", err)
	}

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "codex", launcher.ContinueWorkIntent(
		"feat-picked", "codex", sessPicked, ".claude/worktrees/feat-picked", true,
	))
	if err != nil {
		t.Fatalf("resolveContinueLaunchContext: %v", err)
	}
	if got.ContinuedFrom != sessPicked {
		t.Fatalf("ContinuedFrom = %q, want %q", got.ContinuedFrom, sessPicked)
	}
	if got.TranscriptResumeID != sessPicked {
		t.Fatalf("TranscriptResumeID = %q, want %q", got.TranscriptResumeID, sessPicked)
	}
	if strings.Contains(got.HandoffMarkdown, sessNewerCross) {
		t.Fatalf("handoff markdown used newer cross-harness session:\n%s", got.HandoffMarkdown)
	}
}

func TestResolveContinueLaunchContext_HonorsSelectedResumeSessionIDFromActiveWorkItems(t *testing.T) {
	const (
		sessPickedAWI     = "019ee378-abcd-7000-8000-000000000007"
		sessNewerCrossAWI = "019ee378-abcd-7000-8000-000000000008"
	)
	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "codex", "feat-picked-awi", sessPickedAWI, ".claude/worktrees/feat-picked-awi")
	if _, err := database.Exec(`UPDATE sessions SET active_feature_id = '' WHERE session_id = ?`, sessPickedAWI); err != nil {
		t.Fatalf("clear active_feature_id: %v", err)
	}
	if err := dbpkg.SetActiveWorkItem(database, sessPickedAWI, dbpkg.AgentRootSentinel, "feat-picked-awi"); err != nil {
		t.Fatalf("SetActiveWorkItem: %v", err)
	}
	seedContinueFixture(t, database, projectRoot, "claude", "feat-picked-awi", sessNewerCrossAWI, ".claude/worktrees/feat-cross-awi")
	if _, err := database.Exec(`UPDATE sessions SET created_at = ? WHERE session_id = ?`,
		time.Now().UTC().Add(time.Minute).Format(time.RFC3339), sessNewerCrossAWI); err != nil {
		t.Fatalf("update cross session created_at: %v", err)
	}

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "codex", launcher.ContinueWorkIntent(
		"feat-picked-awi", "codex", sessPickedAWI, ".claude/worktrees/feat-picked-awi", true,
	))
	if err != nil {
		t.Fatalf("resolveContinueLaunchContext: %v", err)
	}
	if got.ContinuedFrom != sessPickedAWI {
		t.Fatalf("ContinuedFrom = %q, want %q", got.ContinuedFrom, sessPickedAWI)
	}
	if got.TranscriptResumeID != sessPickedAWI {
		t.Fatalf("TranscriptResumeID = %q, want %q", got.TranscriptResumeID, sessPickedAWI)
	}
	if strings.Contains(got.HandoffMarkdown, sessNewerCrossAWI) {
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

// TestIsClaudeCodeSessionID verifies the UUID guard helper used by Fix B
// (bug-b262d303): valid Claude Code UUIDs return true; wipnote OTel IDs
// (28-char unhyphenated hex) and other non-UUID strings return false.
func TestIsClaudeCodeSessionID(t *testing.T) {
	valid := []string{
		"019ee378-abcd-7000-8000-000000000001",  // real-looking Claude UUID
		"00000000-0000-0000-0000-000000000000",  // all-zero UUID still valid format
		"ffffffff-ffff-ffff-ffff-ffffffffffff",  // all-f UUID (lowercase)
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF",  // all-f UUID (uppercase — tolerated)
		"d4cc0257-acb4-4c7d-a1c6-9d9ef42668b7",  // observed real session ID
	}
	for _, s := range valid {
		if !isClaudeCodeSessionID(s) {
			t.Errorf("isClaudeCodeSessionID(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		"019ee144e0d5f26e46d6cc07fed9",         // 28-char wipnote OTel hex (no hyphens)
		"sess-abc123",                           // wipnote internal session slug
		"019ee378abcd70008000000000000001",      // UUID without hyphens (32 chars)
		"019ee378-abcd-7000-8000-00000000000",   // too short (35 chars)
		"019ee378-abcd-7000-8000-0000000000011", // too long (37 chars)
		"not-a-uuid-at-all",
	}
	for _, s := range invalid {
		if isClaudeCodeSessionID(s) {
			t.Errorf("isClaudeCodeSessionID(%q) = true, want false", s)
		}
	}
}

// TestResolveContinueLaunchContext_OtelIDBlockedFromResume asserts that when the
// stored session ID is a wipnote OTel ID (28-char hex, no hyphens),
// TranscriptResumeID is NOT set — the guard introduced in bug-b262d303 fires.
func TestResolveContinueLaunchContext_OtelIDBlockedFromResume(t *testing.T) {
	// 28-char hex OTel session ID — the kind the launcher used to stamp into
	// WIPNOTE_SESSION_ID before Fix A, causing "No sessions match" in Claude Code.
	const otelSessionID = "019ee144e0d5f26e46d6cc07fed9"

	projectRoot, database := openContinueTestProject(t)
	seedContinueFixture(t, database, projectRoot, "claude", "feat-otel-guard", otelSessionID, ".claude/worktrees/feat-otel-guard")

	got, err := resolveContinueLaunchContext(projectRoot, projectRoot, "claude", launcher.ContinueWorkIntent(
		"feat-otel-guard", "claude", otelSessionID, ".claude/worktrees/feat-otel-guard", true,
	))
	if err != nil {
		t.Fatalf("resolveContinueLaunchContext: %v", err)
	}
	if got.TranscriptResumeID != "" {
		t.Errorf("TranscriptResumeID = %q, want empty (OTel ID must be blocked)", got.TranscriptResumeID)
	}
	if got.TranscriptResumeOK {
		t.Errorf("TranscriptResumeOK = true, want false for OTel session ID")
	}
	if !containsWarning(got.Warnings, "not a valid Claude Code UUID") {
		t.Errorf("warnings = %v, want UUID-guard warning", got.Warnings)
	}
}
