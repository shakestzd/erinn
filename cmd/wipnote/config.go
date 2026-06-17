package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/worktree"
)

const emptySpikeSweepInterval = time.Hour

var completeWorkItemIfInProgressFn = completeWorkItemIfInProgress

func startEmptySpikeWorktreeSweepLoop(ctx context.Context, writeDB *sql.DB, projectRoot string) {
	if writeDB == nil || projectRoot == "" {
		return
	}
	go func() {
		_ = runEmptySpikeWorktreeSweep(writeDB, projectRoot, time.Now().UTC())

		ticker := time.NewTicker(emptySpikeSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				_ = runEmptySpikeWorktreeSweep(writeDB, projectRoot, now.UTC())
			}
		}
	}()
}

func runEmptySpikeWorktreeSweep(database *sql.DB, projectRoot string, now time.Time) error {
	cfg := worktree.LoadCleanupConfig(projectRoot)
	if !cfg.Enabled() {
		return nil
	}

	worktreesDir := filepath.Join(projectRoot, ".claude", "worktrees")
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("empty spike worktree sweep: read %s: %w", worktreesDir, err)
	}

	ttl := cfg.TTL()
	livenessThreshold := dbpkg.LivenessStalenessThreshold(projectRoot)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "spk-") {
			continue
		}
		worktreePath := filepath.Join(worktreesDir, entry.Name())
		info, err := os.Stat(worktreePath)
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < ttl {
			continue
		}
		if workItemHasLiveHeartbeat(database, entry.Name(), livenessThreshold) {
			continue
		}

		state, err := worktree.InspectCleanupState(projectRoot, worktreePath)
		if err != nil {
			continue
		}
		if state.Locked {
			continue
		}
		if !state.Removable() {
			_, _ = worktree.SnapshotPreservedWorktree(projectRoot, worktreePath, entry.Name(), state)
			continue
		}
		if !completeWorkItemIfInProgressFn(entry.Name(), database) {
			continue
		}
		_ = worktree.RemoveManagedWorktree(projectRoot, worktreePath)
	}
	return nil
}

func workItemHasLiveHeartbeat(database *sql.DB, workItemID string, threshold time.Duration) bool {
	if database == nil || workItemID == "" {
		return false
	}
	cutoff := time.Now().UTC().Add(-threshold).Format(time.RFC3339)
	quoted := make([]string, len(models.ActiveClaimStatuses))
	for i, s := range models.ActiveClaimStatuses {
		quoted[i] = "'" + string(s) + "'"
	}
	query := fmt.Sprintf(`
		SELECT 1
		FROM claims c
		LEFT JOIN sessions s ON s.session_id = c.owner_session_id
		WHERE c.work_item_id = ?
		  AND c.status IN (%s)
		  AND c.last_heartbeat_at >= ?
		  AND COALESCE(s.status, '') <> 'completed'
		LIMIT 1`, strings.Join(quoted, ","))
	var one int
	return database.QueryRow(query, workItemID, cutoff).Scan(&one) == nil
}

func completeWorkItemIfInProgress(id string, database *sql.DB) bool {
	if database == nil || id == "" {
		return false
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, id).Scan(&status); err != nil {
		return false
	}
	if status != "in-progress" {
		return false
	}

	typeName := "feature"
	if strings.HasPrefix(id, "bug-") {
		typeName = "bug"
	} else if strings.HasPrefix(id, "spk-") {
		typeName = "spike"
	}

	exe, err := os.Executable()
	if err != nil {
		exe = "wipnote"
	}
	cmd := exec.Command(exe, typeName, "complete", id)
	return cmd.Run() == nil
}
