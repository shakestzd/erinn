package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/paths"
	"github.com/spf13/cobra"
)

// checkCrossProjectCmd reports sessions that belong to a different project.
//
// The --fix flag was removed in feat-fc3cc9e0. It deleted the reported rows
// from `sessions` and `agent_events`, both of which now live only in the
// per-process projection: the DELETE committed to a throwaway database and
// reindexSessionLedger re-inserted every row from the canonical session ledger
// on the next openDB. The report is unaffected and still accurate — the
// projection's `sessions` table IS hydrated from that ledger, so the detection
// reads real data. Only the remedy was fictional.
//
// The honest remedy is canonical and deliberately not automated here: a
// cross-project session is a real ledger entry written by a real session, and
// removing it means editing .wipnote/sessions/ and the ledger. That is a
// destructive edit to provenance, not a tidy-up, so it stays a human decision.
func checkCrossProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cross-project",
		Short: "Find sessions from other projects",
		Long: `Scan all sessions for entries that belong to a different project
(identified by git remote URL or project directory path).

Sessions with a different git_remote_url than the current project are reported
as cross-project items. When git_remote_url is empty, the project_dir column is
used as a fallback comparison.

Report only. A cross-project session is a real entry in the canonical session
ledger, so removing one means editing .wipnote/ — a deliberate edit to
provenance rather than something this command should do for you.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCheckCrossProject()
		},
	}
	return cmd
}

// crossProjectSession holds identifying info for a foreign session row.
type crossProjectSession struct {
	sessionID    string
	projectDir   string
	gitRemoteURL string
	status       string
}

func runCheckCrossProject() error {
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
	fmt.Printf("\n%d cross-project session(s) reported. Each is a real entry in the\n", len(foreign))
	fmt.Println("canonical session ledger; remove one by editing .wipnote/ deliberately.")
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
