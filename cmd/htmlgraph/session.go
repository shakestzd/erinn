// Register in main.go: rootCmd.AddCommand(sessionCmd())
package main

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

func sessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage development sessions",
	}
	cmd.AddCommand(sessionListCmd())
	cmd.AddCommand(sessionStartCmd())
	cmd.AddCommand(sessionEndCmd())
	cmd.AddCommand(sessionShowCmd())
	cmd.AddCommand(sessionRestoreCmd())
	return cmd
}

// sessionListCmd lists sessions from the SQLite DB.
func sessionListCmd() *cobra.Command {
	var activeOnly bool
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSessionList(activeOnly, limit)
		},
	}
	cmd.Flags().BoolVar(&activeOnly, "active", false, "Only show active sessions")
	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum number of sessions to show")
	return cmd
}

func runSessionList(activeOnly bool, limit int) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	db, err := openDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	sessions, err := dbpkg.ListSessions(db, activeOnly, limit)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	fmt.Printf("%-16s  %-18s  %-10s  %-22s  %s\n",
		"SESSION", "AGENT", "STATUS", "STARTED", "DURATION")
	fmt.Println(strings.Repeat("-", 85))
	for _, s := range sessions {
		printSessionRow(s)
	}
	fmt.Printf("\n%d session(s)\n", len(sessions))
	return nil
}

func printSessionRow(s *models.Session) {
	id := truncate(s.SessionID, 14)
	agent := truncate(s.AgentAssigned, 18)
	started := s.CreatedAt.Local().Format("2006-01-02 15:04:05")
	duration := sessionDuration(s)
	fmt.Printf("%-16s  %-18s  %-10s  %-22s  %s\n",
		id, agent, s.Status, started, duration)
}

func sessionDuration(s *models.Session) string {
	if s.CompletedAt != nil {
		return fmtDuration(s.CompletedAt.Sub(s.CreatedAt))
	}
	if s.Status == "active" {
		return fmtDuration(time.Since(s.CreatedAt))
	}
	return "-"
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, sec)
	}
	return fmt.Sprintf("%dm%02ds", m, sec)
}

// sessionStartCmd creates a new session row.
func sessionStartCmd() *cobra.Command {
	var agent string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a new session",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSessionStart(agent)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "claude-code", "Agent identifier for this session")
	return cmd
}

func runSessionStart(agent string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	db, err := openDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	s := &models.Session{
		SessionID:     generateSessionID(),
		AgentAssigned: agent,
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
	}

	if err := dbpkg.InsertSession(db, s); err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	fmt.Printf("Started session: %s\n", s.SessionID)
	return nil
}

// sessionEndCmd ends a session by ID (or the most recent active session).
func sessionEndCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "end [session-id]",
		Short: "End a session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return runSessionEnd(id)
		},
	}
}

func runSessionEnd(sessionID string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	db, err := openDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	if sessionID == "" {
		sessionID, err = dbpkg.MostRecentActiveSession(db)
		if err != nil {
			return fmt.Errorf("find active session: %w", err)
		}
		if sessionID == "" {
			return fmt.Errorf("no active sessions found\nRun 'htmlgraph session start' to begin tracking, or specify a session ID explicitly.")
		}
	}

	if err := dbpkg.UpdateSessionStatus(db, sessionID, "completed"); err != nil {
		return fmt.Errorf("end session: %w", err)
	}
	fmt.Printf("Ended session: %s\n", sessionID)
	return nil
}

// openDB is a shared helper to open the SQLite DB from the .htmlgraph dir.
// The DB lives in the OS cache dir (keyed by project-path hash) — never
// inside the project tree. See storage.CanonicalDBPath for details.
func openDB(htmlgraphDir string) (*sql.DB, error) {
	projectDir := filepath.Dir(htmlgraphDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return nil, fmt.Errorf("ensure db dir: %w", err)
	}
	db, err := dbpkg.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

// sessionShowCmd returns a cobra.Command that displays full session details.
func sessionShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show session details",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSessionShow(args[0])
		},
	}
}

func runSessionShow(sessionID string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	db, err := openDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	s, err := dbpkg.GetSession(db, sessionID)
	if err != nil {
		return fmt.Errorf("session %q not found: %w\nRun 'htmlgraph session list' to see known sessions.", sessionID, err)
	}

	sep := strings.Repeat("─", 60)
	shortID := truncate(s.SessionID, 14)
	fmt.Println(sep)
	fmt.Printf("  Session %s\n", shortID)
	fmt.Println(sep)
	fmt.Printf("  ID        %s\n", s.SessionID)
	fmt.Printf("  Status    %s\n", s.Status)
	fmt.Printf("  Agent     %s\n", s.AgentAssigned)
	if s.Model != "" {
		fmt.Printf("  Model     %s\n", s.Model)
	}
	fmt.Printf("  Started   %s\n", s.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Printf("  Duration  %s\n", sessionDuration(s))
	if s.StartCommit != "" {
		fmt.Printf("  Start     %s\n", truncate(s.StartCommit, 10))
	}
	if s.EndCommit != "" {
		fmt.Printf("  End       %s\n", truncate(s.EndCommit, 10))
	}
	if s.ActiveFeatureID != "" {
		fmt.Printf("  Feature   %s\n", s.ActiveFeatureID)
	}
	if s.IsSubagent {
		fmt.Printf("  Subagent  yes (parent: %s)\n", truncate(s.ParentSessionID, 14))
	}

	// Commits made during this session.
	commits, _ := dbpkg.GetCommitsBySession(db, sessionID)
	if len(commits) > 0 {
		fmt.Println("\nCommits:")
		for _, c := range commits {
			hash := truncate(c.CommitHash, 10)
			fmt.Printf("  %s  %s\n", hash, truncate(c.Message, 60))
		}
	}

	// Features worked on (distinct from agent_events).
	feats, _ := dbpkg.DistinctFeatureIDs(db, sessionID)
	if len(feats) > 0 {
		fmt.Println("\nFeatures Worked On:")
		for _, f := range feats {
			fmt.Printf("  %s\n", f)
		}
	}

	// Event summary by tool.
	counts, _ := dbpkg.CountEventsByTool(db, sessionID)
	if len(counts) > 0 {
		total := 0
		for _, c := range counts {
			total += c
		}
		fmt.Printf("\nEvents by Tool (%d total):\n", total)
		// Sort by count descending for display.
		type toolCount struct {
			name  string
			count int
		}
		var sorted []toolCount
		for name, count := range counts {
			sorted = append(sorted, toolCount{name, count})
		}
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].count > sorted[i].count {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		for _, tc := range sorted {
			fmt.Printf("  %-12s %d\n", tc.name, tc.count)
		}
	}

	return nil
}

// generateSessionID produces a collision-resistant session ID using crypto/rand.
// Format: sess-{hex8} matching Python/SDK convention.
func generateSessionID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("sess-%x", b)
}
