package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/sessionledger"
	"github.com/shakestzd/wipnote/internal/launcher"
)

// continueTestUUID is a valid Claude Code session UUID used in continue tests.
// isClaudeCodeSessionID must pass for TranscriptResumeID to be set.
const continueTestUUID = "019ee378-abcd-7000-8000-000000000001"

// openContinueTestProject creates a project whose only state is CANONICAL.
//
// It deliberately hands back no *sql.DB. resolveContinueLaunchContext opens its
// own projection, and since the feat-fc3cc9e0 cutover every openDB call returns
// a private in-memory database — so rows seeded into a handle here would be
// invisible to the code under test, which is exactly how the previous fixture
// silently stopped testing anything. Seeding canonical artifacts instead makes
// these tests exercise the real hydration path.
func openContinueTestProject(t *testing.T) string {
	t.Helper()
	projectRoot := t.TempDir()
	for _, sub := range []string{"features", "sessions", "claims"} {
		if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote", sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return projectRoot
}

// seedContinueFixture writes the canonical artifacts a resumable session is
// actually made of: the work item, the session-ledger row carrying its harness,
// an OPEN claim episode tying the two together, and the worktree directory.
// Hydration turns those into the features / sessions / active_work_items rows
// the resumable-session queries read.
//
// startedAt doubles as the session's created_at, which is what the resumable
// query ranks on — so ordering between fixtures is expressed by seeding
// different start times rather than by patching a derived column afterwards.
//
// agentID selects which projection branch supplies work_item_id:
// dbpkg.AgentRootSentinel also populates sessions.active_feature_id (via
// applyActiveFeatureIDFromClaims), whereas any other agent leaves that column
// empty so only the active_work_items branch of the query can match.
func seedContinueFixture(t *testing.T, projectRoot, harness, workItemID, sessionID, worktreeRel string, startedAt time.Time, agentID string) {
	t.Helper()
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")

	html := `<!DOCTYPE html><html><body><article id="` + workItemID +
		`" data-type="feature" data-status="in-progress" data-priority="medium">` +
		`<h1>Continue Test</h1></article></body></html>`
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", workItemID+".html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write feature %s: %v", workItemID, err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, filepath.FromSlash(worktreeRel)), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if _, err := sessionledger.NewStore(wipnoteDir).Open(sessionledger.Record{
		SessionID: sessionID,
		Harness:   harness,
		StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("seed session ledger %s: %v", sessionID, err)
	}
	seedEpisode(t, wipnoteDir, sessionID, sessionID, agentID, workItemID, startedAt, time.Time{}, "")
}

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
			projectRoot := openContinueTestProject(t)
			seedContinueFixture(t, projectRoot, tc.previousHarness, "feat-continue", continueTestUUID,
				".claude/worktrees/feat-continue", time.Now().UTC().Add(-time.Hour), dbpkg.AgentRootSentinel)

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
	projectRoot := openContinueTestProject(t)
	seedContinueFixture(t, projectRoot, "claude", "feat-missing", sessMissing,
		".claude/worktrees/feat-missing", time.Now().UTC().Add(-time.Hour), dbpkg.AgentRootSentinel)
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

// TestContinueLiveCollisionDetectsForeignClaimant covers the collision check
// that disables transcript resume when another session still holds the item.
//
// It seeds the handle directly and calls continueLiveCollision rather than
// driving resolveContinueLaunchContext end to end, because the `claims` table
// has NO canonical hydration source (bug-ec1ff126): reindex projects
// claim_episodes and active_work_items, but nothing populates claims, and
// LiveCollision reads claims heartbeats. In a hydrated projection it therefore
// sees nothing. Testing at this seam keeps the collision logic covered
// honestly; the end-to-end assertion becomes possible again only once claims
// hydration is restored.
func TestContinueLiveCollisionDetectsForeignClaimant(t *testing.T) {
	const (
		otherSession = "019ee378-abcd-7000-8000-000000000004"
		workItemID   = "feat-live"
	)
	projectRoot := openContinueTestProject(t)
	// claims.work_item_id is a foreign key, so the work item has to exist. Seed
	// it canonically and let hydration create the row.
	html := `<!DOCTYPE html><html><body><article id="` + workItemID +
		`" data-type="feature" data-status="in-progress" data-priority="medium">` +
		`<h1>Live Claim</h1></article></body></html>`
	if err := os.WriteFile(filepath.Join(projectRoot, ".wipnote", "features", workItemID+".html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	database, err := openDB(filepath.Join(projectRoot, ".wipnote"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, status, created_at, harness, project_dir)
		VALUES (?, ?, ?, ?, ?, ?)`,
		otherSession, "claude-code", "active", now, "claude", ".",
	); err != nil {
		t.Fatalf("insert live session: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO claims (
			claim_id, work_item_id, owner_session_id, owner_agent, status,
			leased_at, lease_expires_at, last_heartbeat_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"claim-live-001", workItemID, otherSession, "claude-code", "claimed",
		now, now, now, now, now,
	); err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	collision, msg := continueLiveCollision(database, projectRoot, workItemID)
	if !collision {
		t.Fatal("expected a live collision for a foreign claimant with a fresh heartbeat")
	}
	if !strings.Contains(msg, "still live in session "+otherSession) {
		t.Fatalf("collision message = %q, want it to name session %s", msg, otherSession)
	}

	// A stale heartbeat is not a live collision: liveness is heartbeat recency,
	// not the mere existence of a claim row.
	stale := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	if _, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`, stale, "claim-live-001"); err != nil {
		t.Fatalf("stale heartbeat: %v", err)
	}
	if collision, _ := continueLiveCollision(database, projectRoot, workItemID); collision {
		t.Fatal("a stale heartbeat must not count as a live collision")
	}
}

func TestResolveContinueLaunchContext_HonorsSelectedResumeSessionID(t *testing.T) {
	const (
		sessPicked     = "019ee378-abcd-7000-8000-000000000005"
		sessNewerCross = "019ee378-abcd-7000-8000-000000000006"
	)
	projectRoot := openContinueTestProject(t)
	base := time.Now().UTC().Add(-time.Hour)
	seedContinueFixture(t, projectRoot, "codex", "feat-picked", sessPicked,
		".claude/worktrees/feat-picked", base, dbpkg.AgentRootSentinel)
	// Newer, cross-harness, same work item: it would win the ranking, so the
	// explicit ResumeSessionID has to override it.
	seedContinueFixture(t, projectRoot, "claude", "feat-picked", sessNewerCross,
		".claude/worktrees/feat-cross", base.Add(30*time.Minute), dbpkg.AgentRootSentinel)

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
	projectRoot := openContinueTestProject(t)
	base := time.Now().UTC().Add(-time.Hour)
	// A NON-root agent holds the claim, so applyActiveFeatureIDFromClaims leaves
	// sessions.active_feature_id empty and only the active_work_items branch of
	// the resumable query can resolve this session's work item.
	seedContinueFixture(t, projectRoot, "codex", "feat-picked-awi", sessPickedAWI,
		".claude/worktrees/feat-picked-awi", base, "agent-awi")
	seedContinueFixture(t, projectRoot, "claude", "feat-picked-awi", sessNewerCrossAWI,
		".claude/worktrees/feat-cross-awi", base.Add(30*time.Minute), dbpkg.AgentRootSentinel)

	// Guard the premise: if active_feature_id were populated the test would pass
	// through the legacy branch and prove nothing about active_work_items.
	database, err := openDB(filepath.Join(projectRoot, ".wipnote"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	if got := dbpkg.GetActiveFeatureIDForSession(database, sessPickedAWI); got != "" {
		t.Fatalf("premise broken: sessions.active_feature_id = %q, want empty", got)
	}
	if got := dbpkg.GetActiveWorkItem(database, sessPickedAWI, "agent-awi"); got != "feat-picked-awi" {
		t.Fatalf("active_work_items = %q, want feat-picked-awi", got)
	}
	_ = database.Close()

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
		"019ee378-abcd-7000-8000-000000000001", // real-looking Claude UUID
		"00000000-0000-0000-0000-000000000000", // all-zero UUID still valid format
		"ffffffff-ffff-ffff-ffff-ffffffffffff", // all-f UUID (lowercase)
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF", // all-f UUID (uppercase — tolerated)
		"d4cc0257-acb4-4c7d-a1c6-9d9ef42668b7", // observed real session ID
	}
	for _, s := range valid {
		if !isClaudeCodeSessionID(s) {
			t.Errorf("isClaudeCodeSessionID(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		"019ee144e0d5f26e46d6cc07fed9",          // 28-char wipnote OTel hex (no hyphens)
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
	// It is session-SHAPED (graph.IsSessionShapedID accepts 28-char hex), so it
	// survives canonical seeding; it is just not a Claude Code UUID.
	const otelSessionID = "019ee144e0d5f26e46d6cc07fed9"

	projectRoot := openContinueTestProject(t)
	seedContinueFixture(t, projectRoot, "claude", "feat-otel-guard", otelSessionID,
		".claude/worktrees/feat-otel-guard", time.Now().UTC().Add(-time.Hour), dbpkg.AgentRootSentinel)

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
