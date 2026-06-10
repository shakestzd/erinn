package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/shakestzd/wipnote/observe/otel/indexer"
	"github.com/spf13/cobra"
)

func cleanupOrphanSessionsCmd() *cobra.Command {
	var deleteFlag bool
	var yesFlag bool

	cmd := &cobra.Command{
		Use:   "orphan-sessions",
		Short: "List or delete session NDJSON directories with no DB row",
		Long: `Scans .wipnote/sessions/ for directories that contain an events.ndjson
but have no corresponding row in the sessions table (orphan directories).

Without --delete: prints candidate orphan directories with their age and
last-write time. No files are modified.

With --delete: removes orphan directories that are both:
  1. Older than the retention period (` + fmt.Sprintf("%d", indexer.OrphanRetentionDays) + ` days).
  2. Not written to within the last 24 hours.

Requires --yes (or --force) to confirm deletion. Idempotent; safe to re-run.

Retention constant: OrphanRetentionDays = ` + fmt.Sprintf("%d", indexer.OrphanRetentionDays) + ` days.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCleanupOrphanSessions(deleteFlag, yesFlag)
		},
	}

	cmd.Flags().BoolVar(&deleteFlag, "delete", false, "delete eligible orphan directories")
	cmd.Flags().BoolVar(&yesFlag, "yes", false, "skip confirmation prompt (required with --delete)")
	cmd.Flags().BoolVar(&yesFlag, "force", false, "alias for --yes")

	return cmd
}

func runCleanupOrphanSessions(delete, yes bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	printProjectHeaderIfDifferent(wipnoteDir)

	database, err := openDB(wipnoteDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	orphans, err := indexer.FindOrphanSessions(wipnoteDir, database)
	if err != nil {
		return fmt.Errorf("scan orphan sessions: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Println("No orphan session directories found.")
		return nil
	}

	// Always print the list header and candidates.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION ID\tAGE\tLAST WRITE\tELIGIBLE\tPATH")
	fmt.Fprintln(w, "----------\t---\t----------\t--------\t----")
	for _, o := range orphans {
		eligible := indexer.IsEligibleForDeletion(o)
		eligStr := "no"
		if eligible {
			eligStr = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			truncate(o.SessionID, 36),
			formatOrphanAge(o.Age),
			o.LastWriteAt.UTC().Format(time.RFC3339),
			eligStr,
			o.DirPath,
		)
	}
	w.Flush()

	if !delete {
		fmt.Printf("\n%d orphan session director(ies) found. Use --delete --yes to remove eligible ones.\n", len(orphans))
		return nil
	}

	// Deletion mode.
	if !yes {
		return fmt.Errorf("--delete requires --yes (or --force) to confirm; re-run with --delete --yes")
	}

	var eligible []indexer.OrphanInfo
	for _, o := range orphans {
		if indexer.IsEligibleForDeletion(o) {
			eligible = append(eligible, o)
		}
	}

	if len(eligible) == 0 {
		fmt.Printf("\nNo orphans are eligible for deletion (retention=%d days, no-write window=24h).\n",
			indexer.OrphanRetentionDays)
		return nil
	}

	fmt.Printf("\nDeleting %d eligible orphan director(ies)...\n", len(eligible))
	deleted := 0
	for _, o := range eligible {
		if err := os.RemoveAll(o.DirPath); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN: failed to remove %s: %v\n", o.DirPath, err)
			continue
		}
		fmt.Printf("  deleted: %s\n", o.SessionID)
		deleted++
	}
	fmt.Printf("Deleted %d orphan session director(ies).\n", deleted)
	return nil
}

// formatOrphanAge formats a duration for the orphan-sessions list output.
func formatOrphanAge(d time.Duration) string {
	days := d.Hours() / 24
	if days >= 1 {
		return fmt.Sprintf("%.0fd", days)
	}
	hours := d.Hours()
	if hours >= 1 {
		return fmt.Sprintf("%.1fh", hours)
	}
	return fmt.Sprintf("%.0fm", d.Minutes())
}
