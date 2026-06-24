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
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/worktree"
)

const emptySpikeSweepInterval = time.Hour

// orphanDrainInterval is how often the serve-side writer daemon runs the
// UNCAPPED project-wide orphan sweep. The session-start hook only drains a
// small capped batch (SessionStartSweepCap) so the launcher stays fast
// (bug-504095f2); this out-of-band loop clears any remaining backlog from
// crashed sessions without blocking an interactive launch.
const orphanDrainInterval = 5 * time.Minute

// reconcileDrainInterval is how often the serve-side writer daemon auto-commits
// done-but-uncommitted work-item artifacts (feat-c08d1ba1 slice-6). The Stop
// hook stopped running this class synchronously because its per-item git fork
// loop was a ~5.45s per-turn cost; this out-of-band loop performs the same
// deterministic auto-commit off the interactive path so the durable record is
// still committed, just not on a model-response boundary.
const reconcileDrainInterval = 5 * time.Minute

// startDrainLoop runs fn once at startup and then on every interval tick inside
// the headless writer daemon, where wall-clock cost is not user-visible. A panic
// in fn is recovered so one malformed artifact can never crash the writer daemon.
// The loop stops on ctx cancellation. Shared by the orphan- and reconcile-drain
// loops (bug-504095f2, feat-c08d1ba1).
func startDrainLoop(ctx context.Context, interval time.Duration, fn func()) {
	drain := func() {
		defer func() { _ = recover() }()
		fn()
	}
	go func() {
		drain()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				drain()
			}
		}
	}()
}

// startOrphanDrainLoop runs the uncapped project-wide orphan sweep on a low
// frequency inside the headless writer daemon, where its wall-clock cost is not
// user-visible. Best-effort: it shares the daemon's single writable handle, runs
// once at startup and then on a ticker, and stops on ctx cancellation.
func startOrphanDrainLoop(ctx context.Context, writeDB *sql.DB, projectRoot string) {
	if writeDB == nil || projectRoot == "" {
		return
	}
	startDrainLoop(ctx, orphanDrainInterval, func() {
		hooks.SweepOrphanedEventsForProject(writeDB, projectRoot)
	})
}

// startReaperLoop runs the session/collector reaper on a low frequency inside the
// headless writer daemon. currentSessionID="" — the daemon owns no interactive
// session (excludes nothing extra). includeCollectors=true — the daemon is the
// ONLY place orphaned collectors are reaped. reportOnly is driven by the
// reaper_daemon_report_only config knob (default false ⇒ remediate). Best-effort;
// shares the single writable handle; stops on ctx cancellation.
func startReaperLoop(ctx context.Context, writeDB *sql.DB, projectRoot string) {
	if writeDB == nil || projectRoot == "" {
		return
	}
	startDrainLoop(ctx, orphanDrainInterval, func() {
		hooks.ReapStaleSessionsAndCollectors(writeDB, projectRoot, "", true, hooks.ReaperDaemonReportOnly(projectRoot), 0 /*unbounded*/)
	})
}

// startReconcileDrainLoop auto-commits done-but-uncommitted work-item artifacts
// off the hot path (feat-c08d1ba1 slice-6). The Stop hook no longer runs this
// per-item git fork loop synchronously (it was the ~5.45s per-turn cost); this
// loop guarantees the deferred deterministic auto-commit still completes, using
// the daemon's single writable handle. Best-effort and idempotent: a pass with
// nothing dirty is a no-op, and an already-committed artifact does not re-commit.
func startReconcileDrainLoop(ctx context.Context, writeDB *sql.DB, projectRoot string) {
	if writeDB == nil || projectRoot == "" {
		return
	}
	startDrainLoop(ctx, reconcileDrainInterval, func() {
		hooks.ReconcileDoneButUncommittedForProject(writeDB, projectRoot)
	})
}

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
