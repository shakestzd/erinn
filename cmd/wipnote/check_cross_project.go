package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/paths"
	"github.com/spf13/cobra"
)

func checkCrossProjectCmd() *cobra.Command {
	var fix bool

	cmd := &cobra.Command{
		Use:   "cross-project",
		Short: "Find sessions from other projects",
		Long: `Scan all sessions in the database for entries that belong to a different
project (identified by git remote URL or project directory path).

Sessions with a different git_remote_url than the current project are reported
as cross-project items. When git_remote_url is empty, the project_dir column is
used as a fallback comparison.

Use --fix to delete the reported sessions and their associated events.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCheckCrossProject(fix)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "Delete cross-project sessions and their events")
	return cmd
}

// crossProjectSession holds identifying info for a foreign session row.
type crossProjectSession struct {
	sessionID    string
	projectDir   string
	gitRemoteURL string
	status       string
}

func runCheckCrossProject(fix bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	projectRoot := filepath.Dir(wipnoteDir)
	currentRemote := paths.GetGitRemoteURL(projectRoot)

	database, err := openDB(wipnoteDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	foreign, total, err := queryForeignSessions(database, projectRoot, currentRemote)
	if err != nil {
		return err
	}

	if len(foreign) == 0 {
		fmt.Printf("Checked %d session(s) — no cross-project sessions found.\n", total)
		return nil
	}

	printForeignSessions(foreign, currentRemote)

	if !fix {
		fmt.Printf("\nRun with --fix to delete these %d session(s) and their events.\n", len(foreign))
		return nil
	}

	deleted, err := deleteForeignSessions(database, foreign)
	if err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	fmt.Printf("\nDeleted %d cross-project session(s) and their events.\n", deleted)
	return nil
}

// queryForeignSessions scans all session rows and returns those that don't
// belong to the current project, plus the total count of rows inspected.
func queryForeignSessions(database *sql.DB, projectRoot, currentRemote string) ([]crossProjectSession, int, error) {
	rows, err := database.Query(`
		SELECT session_id, COALESCE(project_dir,''), COALESCE(git_remote_url,''), status
		FROM sessions
		ORDER BY session_id`)
	if err != nil {
		return nil, 0, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var foreign []crossProjectSession
	total := 0

	for rows.Next() {
		total++
		var s crossProjectSession
		if err := rows.Scan(&s.sessionID, &s.projectDir, &s.gitRemoteURL, &s.status); err != nil {
			return nil, 0, fmt.Errorf("scan session: %w", err)
		}
		if isForeignSession(s, projectRoot, currentRemote) {
			foreign = append(foreign, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate sessions: %w", err)
	}
	return foreign, total, nil
}

// isForeignSession returns true when the session clearly belongs to a different project.
func isForeignSession(s crossProjectSession, projectRoot, currentRemote string) bool {
	if s.gitRemoteURL != "" && currentRemote != "" {
		return s.gitRemoteURL != currentRemote
	}
	// Fallback: compare project_dir when no remote URL is available.
	if s.projectDir != "" && projectRoot != "" {
		return s.projectDir != projectRoot
	}
	// Cannot determine project ownership — treat as belonging here.
	return false
}

func printForeignSessions(foreign []crossProjectSession, currentRemote string) {
	fmt.Printf("Found %d cross-project session(s):\n", len(foreign))
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("  %-38s  %-10s  %s\n", "session_id", "status", "project_dir / remote")
	fmt.Println(strings.Repeat("-", 80))
	for _, s := range foreign {
		location := s.gitRemoteURL
		if location == "" {
			location = s.projectDir
		}
		fmt.Printf("  %-38s  %-10s  %s\n", s.sessionID, s.status, truncate(location, 28))
	}
	if currentRemote != "" {
		fmt.Printf("\nCurrent project remote: %s\n", currentRemote)
	}
}

func deleteForeignSessions(database *sql.DB, foreign []crossProjectSession) (int, error) {
	tx, err := database.Begin()
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, s := range foreign {
		if _, err := tx.Exec(`DELETE FROM agent_events WHERE session_id = ?`, s.sessionID); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("delete events for %s: %w", s.sessionID, err)
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE session_id = ?`, s.sessionID); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("delete session %s: %w", s.sessionID, err)
		}
		deleted++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return deleted, nil
}
