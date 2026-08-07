package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// seedOverlap wires up the DB state shared by the advisory tests: an active
// claim for "test-sess" (so the write guards allow), plus ANOTHER live session
// that touched the same repo-relative file recently in feature_files.
func seedOverlap(t *testing.T, tdb *testDB, targetRel string) {
	t.Helper()

	tdb.addFeature("feat-mine", "feature", "Mine", "in-progress")
	claim := &models.Claim{
		ClaimID:          "clm-mine",
		WorkItemID:       "feat-mine",
		OwnerSessionID:   "test-sess",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(tdb.DB, claim, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItemOrRenew(self): %v", err)
	}

	// Other session, fresh heartbeat => live per Tier 3 primitive.
	now := time.Now().UTC()
	if _, err := tdb.DB.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES ('sess-other','claude-code','active',?)`,
		now.Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("insert other session: %v", err)
	}
	otherClaim := &models.Claim{
		ClaimID:        "clm-other",
		WorkItemID:     "feat-other",
		OwnerSessionID: "sess-other",
		OwnerAgent:     "claude-code",
		Status:         models.ClaimInProgress,
	}
	tdb.addFeature("feat-other", "feature", "Other", "in-progress")
	if err := db.ClaimItemOrRenew(tdb.DB, otherClaim, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItemOrRenew(other): %v", err)
	}

	// Other session touched the same file 1 minute ago (within the 15m window).
	if _, err := tdb.DB.Exec(`
		INSERT INTO feature_files (id, feature_id, file_path, operation, session_id, last_seen)
		VALUES ('ff-other','feat-other',?,'modify','sess-other',?)`,
		targetRel, now.Add(-1*time.Minute).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("insert overlapping feature_file: %v", err)
	}
}

func TestPreToolUseOverlapAdvisory(t *testing.T) {
	// Repo-relative path: paths.MustNormalize keeps already-relative paths
	// as-is, so feature_files stores and the advisory matches the same string.
	const targetRel = "internal/hooks/pretooluse.go"

	t.Run("warn non-blocking by default", func(t *testing.T) {
		tdb := setupTestDB(t)
		seedOverlap(t, tdb, targetRel)

		os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
		defer os.Unsetenv("WIPNOTE_SESSION_ID")

		event := &CloudEvent{
			AgentID:   "claude-code",
			SessionID: "test-sess",
			CWD:       t.TempDir(),
			ToolName:  "Edit",
			ToolInput: map[string]any{"file_path": targetRel},
			ToolUseID: "overlap-warn",
		}
		result, err := PreToolUse(event, tdb.DB)
		if err != nil {
			t.Fatalf("PreToolUse returned error (must be non-blocking): %v", err)
		}
		if result == nil {
			t.Fatal("nil result")
		}
		if result.Decision == "block" {
			t.Fatalf("default mode must NOT block, got block: %s", result.Reason)
		}
		if !strings.Contains(result.AdditionalContext, "sess-other") {
			t.Fatalf("expected advisory naming sess-other, got AdditionalContext=%q",
				result.AdditionalContext)
		}
		if !strings.Contains(result.AdditionalContext, targetRel) {
			t.Fatalf("expected advisory naming the file, got %q", result.AdditionalContext)
		}
	})

	t.Run("block mode returns exit-2 with recovery hint", func(t *testing.T) {
		tdb := setupTestDB(t)

		projectDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
			t.Fatalf("mkdir .wipnote: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(projectDir, ".wipnote", "config.json"),
			[]byte(`{"block_on_file_overlap": true}`), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
		t.Setenv("CLAUDE_PROJECT_DIR", "")
		t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

		seedOverlap(t, tdb, targetRel)
		os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
		defer os.Unsetenv("WIPNOTE_SESSION_ID")

		event := &CloudEvent{
			AgentID:   "claude-code",
			SessionID: "test-sess",
			CWD:       projectDir,
			ToolName:  "Edit",
			ToolInput: map[string]any{"file_path": targetRel},
			ToolUseID: "overlap-block",
		}
		_, err := PreToolUse(event, tdb.DB)
		if err == nil {
			t.Fatal("block mode must return a BlockExit2Error, got nil")
		}
		var blockErr *BlockExit2Error
		if !errors.As(err, &blockErr) {
			t.Fatalf("expected *BlockExit2Error, got %T: %v", err, err)
		}
		if !strings.Contains(blockErr.Message, "Recovery:") {
			t.Fatalf("block message missing recovery hint: %q", blockErr.Message)
		}
		if !strings.Contains(blockErr.Message, "sess-other") {
			t.Fatalf("block message missing other session id: %q", blockErr.Message)
		}
		// PINNED (bug-c6b550fa): Codex treats exit code 2 with EMPTY stderr as
		// ALLOW, not deny. runHookNamed writes blockErr.Message verbatim to
		// stderr before exiting 2, so this checks the invariant directly
		// (not just via the substrings above, which could both be removed
		// independently without anyone noticing Message went empty). One of
		// three audited BlockExit2Error call sites.
		if strings.TrimSpace(blockErr.Message) == "" {
			t.Fatal("expected non-empty block message (Codex fails open on exit-2 + empty stderr)")
		}
	})

	t.Run("no overlap => no advisory", func(t *testing.T) {
		tdb := setupTestDB(t)

		tdb.addFeature("feat-mine", "feature", "Mine", "in-progress")
		claim := &models.Claim{
			ClaimID:        "clm-mine",
			WorkItemID:     "feat-mine",
			OwnerSessionID: "test-sess",
			OwnerAgent:     "claude-code",
			Status:         models.ClaimInProgress,
		}
		if err := db.ClaimItemOrRenew(tdb.DB, claim, 30*time.Minute); err != nil {
			t.Fatalf("ClaimItemOrRenew: %v", err)
		}

		os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
		defer os.Unsetenv("WIPNOTE_SESSION_ID")

		event := &CloudEvent{
			AgentID:   "claude-code",
			SessionID: "test-sess",
			CWD:       t.TempDir(),
			ToolName:  "Edit",
			ToolInput: map[string]any{"file_path": "internal/unrelated.go"},
			ToolUseID: "overlap-none",
		}
		result, err := PreToolUse(event, tdb.DB)
		if err != nil {
			t.Fatalf("PreToolUse: %v", err)
		}
		if result.AdditionalContext != "" {
			t.Fatalf("expected no advisory, got %q", result.AdditionalContext)
		}
	})
}
