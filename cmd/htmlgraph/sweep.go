package main

import (
	"fmt"
	"path/filepath"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/hooks"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

func sweepCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Reconcile session state (orphaned events, stale rows)",
	}
	cmd.AddCommand(sweepOrphanedEventsCmd())
	return cmd
}

func sweepOrphanedEventsCmd() *cobra.Command {
	var sessionID string
	cmd := &cobra.Command{
		Use:   "orphaned-events",
		Short: "Emit synthetic aborted entries for tool calls that never saw PostToolUse",
		Long: `Finds agent_events rows with status='started' older than the orphan
threshold (5 minutes) and writes synthetic <li data-status="aborted">
entries to the corresponding session HTML files, then transitions the
SQLite rows to status='aborted' with reason='swept'.

Scope defaults to every session in the current project. Pass --session to
limit the sweep to a single session.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			htmlgraphDir, err := findHtmlgraphDir()
			if err != nil {
				return err
			}
			printProjectHeaderIfDifferent(htmlgraphDir)
			dbPath, err := storage.CanonicalDBPath(filepath.Dir(htmlgraphDir))
			if err != nil {
				return fmt.Errorf("resolve db path: %w", err)
			}
			database, err := dbpkg.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer database.Close()

			projectDir := filepath.Dir(htmlgraphDir)
			var appended int
			if sessionID != "" {
				appended = hooks.SweepOrphanedEventsForSession(database, projectDir, sessionID)
			} else {
				appended = hooks.SweepOrphanedEventsForProject(database, projectDir)
			}
			fmt.Printf("Swept %d orphaned tool call(s)\n", appended)
			return nil
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "limit sweep to a single session ID")
	return cmd
}
