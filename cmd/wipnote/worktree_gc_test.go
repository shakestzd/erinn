package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/worktree"
)

func TestGC_PreservesDirtyWorktree_WithSnapshot(t *testing.T) {
	repo := setupWorktreeGitRepo(t)
	spikeID := "spk-deadbeef"
	worktreePath := filepath.Join(repo, ".claude", "worktrees", spikeID)
	createGCWorktree(t, repo, worktreePath, "yolo-"+spikeID)

	tracked := filepath.Join(worktreePath, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write tracked seed: %v", err)
	}
	commitGCWorktreeFile(t, worktreePath, "tracked.txt")

	f, err := os.OpenFile(tracked, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open tracked file: %v", err)
	}
	if _, err := f.WriteString("dirty change\n"); err != nil {
		t.Fatalf("write tracked change: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close tracked file: %v", err)
	}

	state, err := worktree.InspectCleanupState(repo, worktreePath)
	if err != nil {
		t.Fatalf("InspectCleanupState: %v", err)
	}
	if !state.HasTrackedChanges {
		t.Fatal("expected tracked changes to block removal")
	}
	if state.Removable() {
		t.Fatal("dirty worktree must not be removable")
	}

	state, err = worktree.SnapshotPreservedWorktree(repo, worktreePath, spikeID, state)
	if err != nil {
		t.Fatalf("SnapshotPreservedWorktree: %v", err)
	}
	if state.SnapshotRef == "" {
		t.Fatal("expected retained snapshot ref")
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree should be preserved, stat err=%v", err)
	}
}

func TestGC_TTLSweep_RespectsLockAndLiveness(t *testing.T) {
	repo := setupWorktreeGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repo, ".wipnote", "config.json"),
		[]byte(`{"empty_spike_worktree_cleanup":true,"empty_spike_worktree_ttl_days":1}`),
		0o644,
	); err != nil {
		t.Fatalf("write config: %v", err)
	}

	database, err := dbpkg.Open(filepath.Join(repo, ".wipnote", "wipnote.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	for _, spikeID := range []string{"spk-lock0001", "spk-live0002", "spk-old00003"} {
		if err := dbpkg.InsertFeature(database, &dbpkg.Feature{
			ID:        spikeID,
			Type:      "spike",
			Title:     spikeID,
			Status:    "in-progress",
			Priority:  "medium",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("InsertFeature(%s): %v", spikeID, err)
		}
	}

	lockedPath := filepath.Join(repo, ".claude", "worktrees", "spk-lock0001")
	livePath := filepath.Join(repo, ".claude", "worktrees", "spk-live0002")
	oldPath := filepath.Join(repo, ".claude", "worktrees", "spk-old00003")
	createGCWorktree(t, repo, lockedPath, "yolo-spk-lock0001")
	createGCWorktree(t, repo, livePath, "yolo-spk-live0002")
	createGCWorktree(t, repo, oldPath, "yolo-spk-old00003")
	lockGCWorktree(t, repo, lockedPath)

	old := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{lockedPath, livePath, oldPath} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("Chtimes(%s): %v", p, err)
		}
	}

	now := time.Now().UTC()
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     "sess-live-gc",
		AgentAssigned: "codex",
		Status:        "active",
		CreatedAt:     now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("InsertSession live: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO claims
		(claim_id, work_item_id, owner_session_id, owner_agent, status, lease_expires_at, last_heartbeat_at)
		VALUES (?, ?, ?, ?, 'in_progress', ?, ?)`,
		"claim-live-gc", "spk-live0002", "sess-live-gc", "codex",
		now.Add(time.Hour).Format(time.RFC3339), now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert live claim: %v", err)
	}

	origComplete := completeWorkItemIfInProgressFn
	completeWorkItemIfInProgressFn = func(id string, database *sql.DB) bool {
		_, err := database.Exec(`UPDATE features SET status = 'done' WHERE id = ?`, id)
		return err == nil
	}
	t.Cleanup(func() { completeWorkItemIfInProgressFn = origComplete })

	if err := runEmptySpikeWorktreeSweep(database, repo, now); err != nil {
		t.Fatalf("runEmptySpikeWorktreeSweep: %v", err)
	}

	if _, err := os.Stat(lockedPath); err != nil {
		t.Fatalf("locked worktree should survive sweep: %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live worktree should survive sweep: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("stale empty worktree should be removed, stat err=%v", err)
	}

	assertFeatureStatus(t, database, "spk-lock0001", "in-progress")
	assertFeatureStatus(t, database, "spk-live0002", "in-progress")
	assertFeatureStatus(t, database, "spk-old00003", "done")
}

func createGCWorktree(t *testing.T, repoRoot, worktreePath, branch string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, "-b", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
}

func lockGCWorktree(t *testing.T, repoRoot, worktreePath string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "lock", "--reason", "test-live", worktreePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree lock: %v\n%s", err, out)
	}
}

func commitGCWorktreeFile(t *testing.T, worktreePath, path string) {
	t.Helper()
	cmd := exec.Command("git", "-C", worktreePath, "add", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", path, err, out)
	}
	cmd = exec.Command("git", "-C", worktreePath, "commit", "-m", "seed tracked file")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit tracked file: %v\n%s", err, out)
	}
}

func assertFeatureStatus(t *testing.T, database *sql.DB, id, want string) {
	t.Helper()
	var got string
	if err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatalf("feature status %s: %v", id, err)
	}
	if got != want {
		t.Fatalf("feature %s status = %q, want %q", id, got, want)
	}
}
