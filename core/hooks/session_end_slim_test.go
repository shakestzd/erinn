package hooks

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestSessionEnd_SetsStatusCompleted asserts that SessionEnd writes
// status=completed on the sessions row — the first critical write that must
// survive even if Claude Code cancels the handler mid-flight.
func TestSessionEnd_SetsStatusCompleted(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	sessionID := "slim-sess-001"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	if err := db.InsertSession(database, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	evt := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	res, err := SessionEnd(evt, database, projectDir)
	if err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}
	if !res.Continue {
		t.Error("expected Continue=true")
	}

	sess, err := db.GetSession(database, sessionID)
	if err != nil || sess == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Status != "completed" {
		t.Errorf("status = %q, want completed", sess.Status)
	}
	if sess.CompletedAt == nil {
		t.Error("completed_at must be set after SessionEnd")
	}
}

func TestGC_SessionEnd_RemovesEmptySpike(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()
	initSessionEndGitRepo(t, projectDir)

	origComplete := completeIfInProgressFn
	completeIfInProgressFn = func(id string, database *sql.DB) bool {
		_, err := database.Exec(`UPDATE features SET status = 'done' WHERE id = ?`, id)
		return err == nil
	}
	t.Cleanup(func() { completeIfInProgressFn = origComplete })

	sessionID := "slim-spike-gc-001"
	spikeID := "spk-a1b2c3d4"
	worktreeRel := filepath.ToSlash(filepath.Join(".claude", "worktrees", spikeID))
	worktreePath := filepath.Join(projectDir, filepath.FromSlash(worktreeRel))
	createManagedSpikeWorktree(t, projectDir, worktreePath, "yolo-"+spikeID)

	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(projectDir, ".wipnote", "config.json"),
		[]byte(`{"empty_spike_worktree_cleanup":true,"empty_spike_worktree_ttl_days":7}`),
		0o644,
	); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := db.InsertSession(database, &models.Session{
		SessionID:        sessionID,
		AgentAssigned:    "claude-code",
		Status:           "active",
		CreatedAt:        time.Now().UTC(),
		ProjectDir:       projectDir,
		ExecWorktreePath: worktreeRel,
		Branch:           "yolo-" + spikeID,
		Harness:          "claude",
		ActiveFeatureID:  spikeID,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	td.addFeature(spikeID, "spike", "Empty spike", "in-progress")

	if _, err := SessionEnd(&CloudEvent{SessionID: sessionID, CWD: projectDir, Reason: "prompt_input_exit"}, database, projectDir); err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}

	var status string
	if err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, spikeID).Scan(&status); err != nil {
		t.Fatalf("feature status: %v", err)
	}
	if status != "done" {
		t.Fatalf("spike status = %q, want done", status)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree removed, stat err=%v", err)
	}
}

// TestGC_SessionEnd_SkipsCleanupWhenSpikeRunsInTrackWorktree asserts that a
// spike session running inside a shared track worktree (ExecWorktreePath
// contains "trk-" not the spike ID) does NOT delete the track worktree when
// the session ends (Finding D fix).
func TestGC_SessionEnd_SkipsCleanupWhenSpikeRunsInTrackWorktree(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()
	initSessionEndGitRepo(t, projectDir)

	origComplete := completeIfInProgressFn
	completeIfInProgressFn = func(id string, database *sql.DB) bool {
		_, err := database.Exec(`UPDATE features SET status = 'done' WHERE id = ?`, id)
		return err == nil
	}
	t.Cleanup(func() { completeIfInProgressFn = origComplete })

	sessionID := "slim-spike-shared-track-001"
	spikeID := "spk-eeee1111"
	trackID := "trk-ffff2222"
	// The session ran in a TRACK worktree, not the spike's own worktree.
	trackWorktreeRel := filepath.ToSlash(filepath.Join(".claude", "worktrees", trackID))
	trackWorktreePath := filepath.Join(projectDir, filepath.FromSlash(trackWorktreeRel))
	createManagedSpikeWorktree(t, projectDir, trackWorktreePath, "trk-"+trackID)

	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(projectDir, ".wipnote", "config.json"),
		[]byte(`{"empty_spike_worktree_cleanup":true,"empty_spike_worktree_ttl_days":7}`),
		0o644,
	); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := db.InsertSession(database, &models.Session{
		SessionID:        sessionID,
		AgentAssigned:    "claude-code",
		Status:           "active",
		CreatedAt:        time.Now().UTC(),
		ProjectDir:       projectDir,
		ExecWorktreePath: trackWorktreeRel, // spike running in a track worktree
		Branch:           "trk-" + trackID,
		Harness:          "claude",
		ActiveFeatureID:  spikeID,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	td.addFeature(spikeID, "spike", "Spike in shared track worktree", "in-progress")

	if _, err := SessionEnd(&CloudEvent{SessionID: sessionID, CWD: projectDir, Reason: "prompt_input_exit"}, database, projectDir); err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}

	// The track worktree must NOT be deleted — it belongs to the track, not the spike.
	if _, err := os.Stat(trackWorktreePath); os.IsNotExist(err) {
		t.Fatal("track worktree was incorrectly deleted by spike cleanup")
	}
}

func initSessionEndGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmd := exec.Command("git", "-C", dir, "add", ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "commit", "-m", "initial")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func createManagedSpikeWorktree(t *testing.T, repoRoot, worktreePath, branch string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, "-b", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
}

// TestSessionEnd_ReleasesClaimsBeforeOtherWork asserts that SessionEnd
// releases all active claims for the session — a critical write that must
// happen early so that claim release is not lost if the handler is cancelled.
func TestSessionEnd_ReleasesClaimsBeforeOtherWork(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	sessionID := "slim-sess-002"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	td.addFeature("feat-slim-0001", "feature", "slim test item", "in-progress")

	c := &models.Claim{
		ClaimID:        "claim-slim-001",
		WorkItemID:     "feat-slim-0001",
		OwnerSessionID: sessionID,
		OwnerAgent:     "claude-code",
		Status:         models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Precondition: claim is active.
	if got, _ := db.GetActiveClaim(database, "feat-slim-0001"); got == nil {
		t.Fatal("precondition: expected active claim before SessionEnd")
	}

	evt := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	if _, err := SessionEnd(evt, database, projectDir); err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}

	// Claim must be released (abandoned).
	if got, _ := db.GetActiveClaim(database, "feat-slim-0001"); got != nil {
		t.Fatalf("claim still active after SessionEnd, status=%s", got.Status)
	}
	var status string
	database.QueryRow(
		`SELECT status FROM claims WHERE claim_id = 'claim-slim-001'`,
	).Scan(&status)
	if status != "abandoned" {
		t.Errorf("claim status = %q, want abandoned", status)
	}
}

// TestSessionEnd_DoesNotFinalizeHTML asserts that SessionEnd no longer calls
// FinalizeSessionHTML (that step is now owned exclusively by Stop). We verify by
// creating a sentinel session HTML and confirming SessionEnd leaves it unchanged.
// Stop is responsible for updating the event-count badge; SessionEnd must not.
func TestSessionEnd_DoesNotFinalizeHTML(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	sessionID := "slim-no-html-003"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	if err := db.InsertSession(database, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Place a sentinel session HTML in the expected location.
	// FinalizeSessionHTML writes to .wipnote/sessions/<id>/session.html;
	// if SessionEnd calls it, this file would be overwritten.
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const sentinel = `<html><body>SENTINEL-DO-NOT-OVERWRITE</body></html>`
	htmlPath := filepath.Join(sessDir, "session.html")
	if err := os.WriteFile(htmlPath, []byte(sentinel), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	evt := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	if _, err := SessionEnd(evt, database, projectDir); err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}

	// Verify the sentinel HTML is unchanged — SessionEnd must NOT finalize it.
	got, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != sentinel {
		t.Errorf("SessionEnd modified the session HTML (FinalizeSessionHTML must NOT be called here);\ngot:  %q\nwant: %q", string(got), sentinel)
	}
}

func TestSessionEnd_WritesStructuredHandoff(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	sessionID := "slim-handoff-004"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "codex")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	if err := db.InsertSession(database, &models.Session{
		SessionID:     "sess-prev-001",
		AgentAssigned: "codex",
		Status:        "completed",
		CreatedAt:     time.Now().UTC().Add(-time.Hour),
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}

	if err := db.InsertSession(database, &models.Session{
		SessionID:        sessionID,
		AgentAssigned:    "codex",
		ParentSessionID:  "sess-prev-001",
		Status:           "active",
		CreatedAt:        time.Now().UTC(),
		ProjectDir:       projectDir,
		ExecWorktreePath: ".codex/worktrees/feat-b5411a1d",
		Branch:           "feat-b5411a1d",
		Harness:          "codex",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE sessions
		SET active_feature_id = ?, last_user_query = ?, transcript_path = ?
		WHERE session_id = ?`,
		"feat-b5411a1d",
		"Persist the session-end handoff so continue can rehydrate.",
		".wipnote/sessions/slim-handoff-004/transcript.jsonl",
		sessionID,
	); err != nil {
		t.Fatalf("seed session handoff context: %v", err)
	}

	td.addFeature("feat-b5411a1d", "feature", "Session-end handoff capture", "in-progress")

	now := time.Now().UTC()
	events := []*models.AgentEvent{
		{
			EventID:      "evt-handoff-read",
			AgentID:      "codex",
			EventType:    models.EventToolCall,
			Timestamp:    now.Add(-2 * time.Minute),
			ToolName:     "Read",
			InputSummary: "Read core/hooks/session_end.go",
			SessionID:    sessionID,
			FeatureID:    "feat-b5411a1d",
			Status:       "completed",
			Source:       "hook",
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			EventID:       "evt-handoff-bash",
			AgentID:       "codex",
			EventType:     models.EventToolCall,
			Timestamp:     now.Add(-time.Minute),
			ToolName:      "Bash",
			InputSummary:  "go test ./core/hooks/...",
			OutputSummary: "FAIL: expected handoff_notes to be populated",
			SessionID:     sessionID,
			FeatureID:     "feat-b5411a1d",
			Status:        "failed",
			Source:        "hook",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}
	for _, ev := range events {
		if err := db.InsertEvent(database, ev); err != nil {
			t.Fatalf("InsertEvent %s: %v", ev.EventID, err)
		}
	}

	if _, err := database.Exec(`
		INSERT INTO feature_files (id, feature_id, file_path, operation, session_id)
		VALUES (?, ?, ?, ?, ?)`,
		"ff-handoff-001", "feat-b5411a1d", "core/hooks/session_end.go", "read", sessionID,
	); err != nil {
		t.Fatalf("insert feature_files: %v", err)
	}

	if _, err := SessionEnd(&CloudEvent{SessionID: sessionID, CWD: projectDir, Reason: "prompt_input_exit"}, database, projectDir); err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}

	sess, err := db.GetSession(database, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !strings.Contains(sess.HandoffNotes, "feat-b5411a1d") {
		t.Fatalf("handoff_notes missing work item context: %q", sess.HandoffNotes)
	}
	if !strings.Contains(sess.HandoffNotes, "Read core/hooks/session_end.go") {
		t.Fatalf("handoff_notes missing recent activity: %q", sess.HandoffNotes)
	}
	if !strings.Contains(sess.RecommendedNext, ".codex/worktrees/feat-b5411a1d") {
		t.Fatalf("recommended_next missing worktree context: %q", sess.RecommendedNext)
	}

	var blockers []string
	if err := json.Unmarshal(sess.Blockers, &blockers); err != nil {
		t.Fatalf("unmarshal blockers: %v (raw=%s)", err, string(sess.Blockers))
	}
	if len(blockers) == 0 || !strings.Contains(blockers[0], "FAIL: expected handoff_notes to be populated") {
		t.Fatalf("blockers missing failed trace: %v", blockers)
	}

	var recommendedContext map[string]any
	if err := json.Unmarshal(sess.RecommendedContext, &recommendedContext); err != nil {
		t.Fatalf("unmarshal recommended_context: %v (raw=%s)", err, string(sess.RecommendedContext))
	}
	if got := recommendedContext["work_item_id"]; got != "feat-b5411a1d" {
		t.Fatalf("recommended_context.work_item_id = %v, want feat-b5411a1d", got)
	}
	if got := recommendedContext["last_session_id"]; got != sessionID {
		t.Fatalf("recommended_context.last_session_id = %v, want %s", got, sessionID)
	}
	if got := recommendedContext["parent_session_id"]; got != "sess-prev-001" {
		t.Fatalf("recommended_context.parent_session_id = %v, want sess-prev-001", got)
	}
	files, _ := recommendedContext["files"].([]any)
	if len(files) == 0 || files[0] != "core/hooks/session_end.go" {
		t.Fatalf("recommended_context.files = %v, want core/hooks/session_end.go", files)
	}
}
