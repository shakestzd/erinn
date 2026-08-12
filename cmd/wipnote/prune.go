package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/observe/otel/retention"
	"github.com/spf13/cobra"
)

// pruneCmd reclaims disk in the current project's .wipnote/ by applying the
// retention policy on demand: rotate/cap oversized logs and archive+remove raw
// events.ndjson for sessions that are inactive AND fully consumed by the
// telemetry indexer. It is the same policy the serve background loop applies every 24h,
// exposed for manual reclaim. Conservative by default: --dry-run reports what
// would be reclaimed without touching any files.
func pruneCmd() *cobra.Command {
	var dryRun bool
	var catchUp bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Reclaim disk: rotate oversized logs and archive ingested session ndjson",
		Long: `Applies the wipnote disk-retention policy on demand.

Two reclamation steps run:
  1. Log rotation — serve-auto.log, serve-<id>.log, and debug.log are capped at
     log_max_bytes (default 50MB), keeping log_keep rotated copies (default 2).
  2. NDJSON sweep — raw events.ndjson is archived into .wipnote/archive/ and the
     live session dir removed, but ONLY for sessions that are BOTH inactive
     (not the active session, not modified within the last 10 minutes) AND fully
     consumed by the telemetry indexer (.index-offset >= file size). Sessions
     older than ndjson_retain_days (default 30) — or beyond ndjson_max_sessions
     when set — are pruned; everything else is kept.

Knobs live in .wipnote/config.json: log_max_bytes, log_keep, ndjson_retain_days,
ndjson_max_sessions. No un-ingested or active-session data is ever removed.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPrune(cmd, dryRun, catchUp)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Report what would be reclaimed without modifying any files")
	cmd.Flags().BoolVar(&catchUp, "catch-up", true,
		"Run the telemetry indexer first so .index-offset is current (without this, only `wipnote serve` ever advances it)")
	return cmd
}

// catchUpDeadline bounds the whole catch-up pass; catchUpIdleWindow stops it
// once the indexer has made no progress for a while, which is the normal exit
// for a corpus whose active session is still being appended to.
const (
	catchUpDeadline   = 10 * time.Minute
	catchUpIdleWindow = 15 * time.Second
)

// catchUpTelemetryIndex runs the dashboard telemetry indexer until every
// session's .index-offset has reached its events.ndjson size, and returns how
// many sessions ended up fully consumed.
//
// It stops early when progress stalls: the active session is appended to
// continuously, so "everything caught up" is not always reachable and waiting
// for it would hang the command.
func catchUpTelemetryIndex(cmd *cobra.Command, wipnoteDir string, database *sql.DB) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), catchUpDeadline)
	defer cancel()

	startDashboardTelemetryIndexer(ctx, database, wipnoteDir)

	var lastTotal int64 = -1
	lastProgress := time.Now()
	for {
		consumed, total, behind := telemetryOffsets(wipnoteDir)
		if behind == 0 {
			return consumed, nil
		}
		if total != lastTotal {
			lastTotal = total
			lastProgress = time.Now()
		} else if time.Since(lastProgress) > catchUpIdleWindow {
			// No bytes consumed for a while — treat as done rather than hang.
			return consumed, nil
		}
		select {
		case <-ctx.Done():
			return consumed, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// telemetryOffsets reports how many sessions are fully consumed, the total
// number of bytes consumed across all sessions, and how many sessions are still
// behind.
func telemetryOffsets(wipnoteDir string) (caughtUp int, totalConsumed int64, behind int) {
	sessions, _ := filepath.Glob(filepath.Join(wipnoteDir, "sessions", "*"))
	for _, sessDir := range sessions {
		eventsFile := filepath.Join(sessDir, "events.ndjson")
		fi, err := os.Stat(eventsFile)
		if err != nil {
			continue
		}
		var off int64
		if b, rerr := os.ReadFile(filepath.Join(sessDir, ".index-offset")); rerr == nil {
			off, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		}
		totalConsumed += off
		if off >= fi.Size() {
			caughtUp++
		} else {
			behind++
		}
	}
	return caughtUp, totalConsumed, behind
}

func runPrune(cmd *cobra.Command, dryRun, catchUp bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	// retention.Sweep's completed-session archive pass takes a *sql.DB and
	// SELECTs sessions whose status is 'completed'. It used to be handed a
	// read-only handle on the per-project SQLite file; that file is gone
	// (feat-fc3cc9e0). The same rows now come from the canonical session
	// ledger, hydrated into a process-local in-memory projection by openDB —
	// so the archive pass keeps working and is sourced from canonical state
	// rather than from a derived file that may or may not have existed.
	//
	// A nil DB is tolerated by Sweep: log rotation and the ndjson coverage
	// sweep still run, only the completed-session archive pass is skipped. So
	// a projection that cannot be built degrades instead of failing the
	// command.
	var sqlDB *sql.DB
	if db, oerr := openDB(wipnoteDir); oerr == nil {
		sqlDB = db
		defer sqlDB.Close()
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"wipnote prune: session projection unavailable (%v) — skipping the completed-session archive pass\n", oerr)
	}

	// Bring .index-offset up to date BEFORE the sweep evaluates it.
	//
	// The ndjson sweep only archives a session the telemetry indexer has fully
	// consumed, but that marker is advanced by exactly one thing: the indexer
	// started inside `wipnote serve`. A user who never opens the dashboard
	// therefore accumulates telemetry forever with retention silently gated
	// off — measured on this repo at ~176k lines / 141MB per hour of
	// multi-agent work. Running the same indexer here makes retention a
	// property of running `wipnote prune`, which is the command whose job it
	// is, rather than of having happened to start a dashboard.
	if catchUp && sqlDB != nil {
		if n, cerr := catchUpTelemetryIndex(cmd, wipnoteDir, sqlDB); cerr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"wipnote prune: telemetry catch-up did not complete (%v) — sessions still behind will be kept\n", cerr)
		} else if n > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "wipnote prune: telemetry catch-up consumed %d session(s)\n", n)
		}
	}

	res, err := retention.Sweep(sqlDB, wipnoteDir, "", dryRun)
	if err != nil {
		return fmt.Errorf("prune sweep: %w", err)
	}

	out := cmd.OutOrStdout()
	verb := "reclaimed"
	if dryRun {
		verb = "would reclaim"
	}
	fmt.Fprintf(out, "wipnote prune: %s %d session(s), %s\n",
		verb, res.Pruned, humanBytes(res.BytesReclaimed))
	if res.Skipped > 0 {
		fmt.Fprintf(out, "  kept %d session(s) (active, recent, or not yet ingested)\n", res.Skipped)
	}
	return nil
}
