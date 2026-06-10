package main

import (
	"database/sql"
	"fmt"
	"path/filepath"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/observe/otel/retention"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

// pruneCmd reclaims disk in the current project's .wipnote/ by applying the
// retention policy on demand: rotate/cap oversized logs and archive+remove raw
// events.ndjson for sessions that are inactive AND durably ingested into
// SQLite. It is the same policy the serve background loop applies every 24h,
// exposed for manual reclaim. Conservative by default: --dry-run reports what
// would be reclaimed without touching any files.
func pruneCmd() *cobra.Command {
	var dryRun bool

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
     ingested into the SQLite read index (.index-offset >= file size). Sessions
     older than ndjson_retain_days (default 30) — or beyond ndjson_max_sessions
     when set — are pruned; everything else is kept.

Knobs live in .wipnote/config.json: log_max_bytes, log_keep, ndjson_retain_days,
ndjson_max_sessions. No un-ingested or active-session data is ever removed.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPrune(cmd, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Report what would be reclaimed without modifying any files")
	return cmd
}

func runPrune(cmd *cobra.Command, dryRun bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(wipnoteDir)

	// Read-only handle is sufficient: Run only SELECTs completed sessions and
	// archives files; the ndjson sweep reads .index-offset, not the DB. A nil
	// DB is tolerated by Sweep — log rotation + the ndjson coverage sweep still
	// run, only the completed-session archive pass is skipped.
	var sqlDB *sql.DB
	if dbPath, dberr := storage.CanonicalDBPath(projectDir); dberr == nil {
		if db, oerr := dbpkg.OpenReadOnly(dbPath); oerr == nil {
			sqlDB = db
			defer sqlDB.Close()
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
