package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func cleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Cleanup operations that respect the HTML-canonical invariant",
	}
	cmd.AddCommand(cleanupGhostSessionsCmd())
	return cmd
}

func cleanupGhostSessionsCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "ghost-sessions",
		Short: "Remove session rows with no HTML file AND no messages/tool_calls/agent_events",
		Long: `Finds session rows where BOTH conditions hold:
  1. No .htmlgraph/sessions/<id>.html file exists on disk
  2. No messages, tool_calls, or agent_events for the session_id

Only rows that meet BOTH conditions are deleted. Sessions with HTML files are
never touched, because HTML is the canonical store — an empty session with a
header file is still a real session.

Idempotent. Safe to re-run.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCleanupGhostSessions(dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list ghost sessions without deleting")
	return cmd
}

func runCleanupGhostSessions(dryRun bool) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	printProjectHeaderIfDifferent(htmlgraphDir)

	database, err := openDB(htmlgraphDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	// 1. Build the set of session_ids that have HTML files on disk.
	htmlIDs, err := collectSessionHTMLIDs(htmlgraphDir)
	if err != nil {
		return fmt.Errorf("scan session HTML files: %w", err)
	}

	// 2. Query the sessions table for candidates: rows with zero content
	//    across messages, tool_calls, and agent_events.
	candidates, err := findContentFreeSessionIDs(database)
	if err != nil {
		return fmt.Errorf("query content-free sessions: %w", err)
	}

	// 3. Filter to TRUE ghosts: candidates that ALSO have no HTML file.
	var ghosts []string
	for _, id := range candidates {
		if _, hasHTML := htmlIDs[id]; !hasHTML {
			ghosts = append(ghosts, id)
		}
	}

	if len(ghosts) == 0 {
		fmt.Println("No ghost sessions — every content-free row has a canonical HTML file.")
		return nil
	}

	if dryRun {
		fmt.Printf("Dry run: %d ghost sessions would be deleted\n", len(ghosts))
		for _, id := range ghosts {
			fmt.Printf("  %s: no HTML file, no events\n", truncate(id, 36))
		}
		return nil
	}

	// 4. Delete. Use a transaction so we don't leave partial state.
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	deleted := 0
	for _, id := range ghosts {
		res, err := tx.Exec(`DELETE FROM sessions WHERE session_id = ?`, id)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("delete %s: %w", id, err)
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
		fmt.Printf("  %s: deleted\n", truncate(id, 36))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	fmt.Printf("Deleted %d ghost sessions\n", deleted)
	return nil
}

// collectSessionHTMLIDs walks .htmlgraph/sessions/ and returns the set of
// session IDs that have a corresponding .html file on disk.
func collectSessionHTMLIDs(htmlgraphDir string) (map[string]struct{}, error) {
	dir := filepath.Join(htmlgraphDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}
	ids := make(map[string]struct{})
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		id := strings.TrimSuffix(name, ".html")
		ids[id] = struct{}{}
	}
	return ids, nil
}

// findContentFreeSessionIDs returns session_ids that have zero associated
// messages, tool_calls, and agent_events. These are candidates for ghost
// cleanup — final determination requires also confirming no HTML file.
func findContentFreeSessionIDs(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`
		SELECT session_id FROM sessions
		WHERE session_id NOT IN (SELECT DISTINCT session_id FROM messages)
		  AND session_id NOT IN (SELECT DISTINCT session_id FROM tool_calls)
		  AND session_id NOT IN (SELECT DISTINCT session_id FROM agent_events)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
