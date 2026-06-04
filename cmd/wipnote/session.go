// Register in main.go: rootCmd.AddCommand(sessionCmd())
package main

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/internal/daemon/apply"
	dbpkg "github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/models"
	"github.com/shakestzd/wipnote/internal/storage"
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
	cmd.AddCommand(sessionPruneCmd())
	cmd.AddCommand(sessionArchiveCmd())
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
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	// bug-af107c36: session list is purely read-only; switch to
	// OpenReadOnlyMigrated (bootstrap+migrate then read-only) so it neither
	// holds the writer lock nor surfaces SQLITE_BUSY under contention.
	db, err := openReadOnlyDB(dir)
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
	dir, err := findWipnoteDir()
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

	// feat-075c110d MVP-4: route the session-row insert through the per-project
	// writer daemon first (bounded ~2s, auto-spawn). On ANY daemon miss
	// (unavailable / forbidden / timeout / error-ack) fall back to the direct
	// insert — surfacing its error exactly as before. The session row is never
	// lost: the fallback always runs the direct insert when the daemon misses.
	if !apply.RouteSessionInsert(filepath.Dir(dir), s) {
		if err := dbpkg.InsertSession(db, s); err != nil {
			return fmt.Errorf("start session: %w", err)
		}
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
	dir, err := findWipnoteDir()
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
			return fmt.Errorf("no active sessions found\nRun 'wipnote session start' to begin tracking, or specify a session ID explicitly.")
		}
	}

	// feat-075c110d MVP-4: route the session status transition through the
	// per-project writer daemon first (bounded ~2s, auto-spawn). On ANY daemon
	// miss fall back to the direct UpdateSessionStatus, surfacing its error
	// exactly as before.
	if !apply.RouteSessionStatus(filepath.Dir(dir), sessionID, "completed") {
		if err := dbpkg.UpdateSessionStatus(db, sessionID, "completed"); err != nil {
			return fmt.Errorf("end session: %w", err)
		}
	}
	fmt.Printf("Ended session: %s\n", sessionID)
	return nil
}

// openDB is a shared helper to open the SQLite DB from the .wipnote dir.
// The DB lives in the OS cache dir (keyed by project-path hash) — never
// inside the project tree. See storage.CanonicalDBPath for details.
func openDB(wipnoteDir string) (*sql.DB, error) {
	projectDir := filepath.Dir(wipnoteDir)
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

// openReadOnlyDB resolves the canonical DB path exactly like openDB but
// returns a SQLite engine-level read-only handle (mode=ro) for strictly
// read-only CLI surfaces (e.g. `wipnote lineage`) so they cannot hold the
// writer lock and so the engine hard-rejects any accidental write.
//
// bug-7dbaf552 / roborev followup: it bootstraps via dbpkg.OpenReadOnlyMigrated
// (writable Open FIRST so the schema exists / is migrated — that path is
// Fix-1 RetryOnBusy-safe and its handle is closed before the read handle
// opens — THEN mode=ro). This restores the migrate-on-open guarantee that the
// pre-7dbaf552 writable openDB provided, which a bare OpenReadOnly dropped
// (mode=ro never creates a file and never migrates), while keeping the
// contention benefit. Callers still layer dbpkg.RetryOnBusy around individual
// queries.
func openReadOnlyDB(wipnoteDir string) (*sql.DB, error) {
	// bug-4b07fd94: on a fresh clone the SQLite read-index is missing/empty
	// even though .wipnote/*.html has lineage edges. Lazily build the index
	// synchronously before opening the read-only handle. On a warm index this
	// is a near-zero-cost staleness check (one SELECT COUNT).
	if err := ensureIndexPopulated(wipnoteDir); err != nil {
		// Non-fatal: if the lazy build fails (e.g. DB locked by another process)
		// we still open the read-only handle and let the caller get whatever data
		// is available rather than aborting the command entirely.
		fmt.Fprintf(os.Stderr, "[wipnote] warning: lazy index build: %v\n", err)
	}

	projectDir := filepath.Dir(wipnoteDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	db, err := dbpkg.OpenReadOnlyMigrated(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database (read-only): %w", err)
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
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	// bug-af107c36: session show is purely read-only; switch to
	// OpenReadOnlyMigrated (bootstrap+migrate then read-only) so it neither
	// holds the writer lock nor surfaces SQLITE_BUSY under contention.
	db, err := openReadOnlyDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	s, err := dbpkg.GetSession(db, sessionID)
	if err != nil {
		return fmt.Errorf("session %q not found: %w\nRun 'wipnote session list' to see known sessions.", sessionID, err)
	}
	nodes, err := dbpkg.LoadSessionAdherenceNodes(dir)
	if err != nil {
		return err
	}
	adherence, err := dbpkg.DeriveSessionAdherence(db, sessionID, nodes)
	if err != nil {
		return err
	}
	s.Adherence = adherence

	return renderSessionShow(os.Stdout, db, s)
}

func renderSessionShow(w io.Writer, db *sql.DB, s *models.Session) error {
	sep := strings.Repeat("─", 60)
	shortID := truncate(s.SessionID, 14)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  Session %s\n", shortID)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  ID        %s\n", s.SessionID)
	fmt.Fprintf(w, "  Status    %s\n", s.Status)
	fmt.Fprintf(w, "  Agent     %s\n", s.AgentAssigned)
	if s.Model != "" {
		fmt.Fprintf(w, "  Model     %s\n", s.Model)
	}
	fmt.Fprintf(w, "  Started   %s\n", s.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "  Duration  %s\n", sessionDuration(s))
	if s.StartCommit != "" {
		fmt.Fprintf(w, "  Start     %s\n", truncate(s.StartCommit, 10))
	}
	if s.EndCommit != "" {
		fmt.Fprintf(w, "  End       %s\n", truncate(s.EndCommit, 10))
	}
	if s.ActiveFeatureID != "" {
		fmt.Fprintf(w, "  Feature   %s\n", s.ActiveFeatureID)
	}
	if s.IsSubagent {
		fmt.Fprintf(w, "  Subagent  yes (parent: %s)\n", truncate(s.ParentSessionID, 14))
	}
	if s.Adherence != nil {
		fmt.Fprintf(w, "  Adherence %d%% (%d pass, %d warn, %d fail)\n",
			s.Adherence.Score, s.Adherence.Passed, s.Adherence.Warned, s.Adherence.Failed)
		for _, check := range s.Adherence.Checks {
			fmt.Fprintf(w, "    %-18s  %-14s  %s\n",
				check.Key, string(check.Status), check.Summary)
		}
	}

	// Commits made during this session.
	commits, _ := dbpkg.GetCommitsBySession(db, s.SessionID)
	if len(commits) > 0 {
		fmt.Fprintln(w, "\nCommits:")
		for _, c := range commits {
			hash := truncate(c.CommitHash, 10)
			fmt.Fprintf(w, "  %s  %s\n", hash, truncate(c.Message, 60))
		}
	}

	// Features worked on (distinct from agent_events).
	feats, _ := dbpkg.DistinctFeatureIDs(db, s.SessionID)
	if len(feats) > 0 {
		fmt.Fprintln(w, "\nFeatures Worked On:")
		for _, f := range feats {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}

	// Files touched during this session (from feature_files.session_id).
	sessionFiles, _ := dbpkg.ListFilesBySession(db, s.SessionID)
	if len(sessionFiles) > 0 {
		fmt.Fprintln(w, "\nFiles:")
		fmt.Fprintf(w, "  %-10s  %-24s  %s\n", "OPERATION", "LAST SEEN", "PATH")
		fmt.Fprintf(w, "  %s\n", strings.Repeat("-", 70))
		for _, sf := range sessionFiles {
			fmt.Fprintf(w, "  %-10s  %-24s  %s\n", sf.Operation, sf.LastSeen, sf.FilePath)
		}
	}

	// Event summary by tool.
	counts, _ := dbpkg.CountEventsByTool(db, s.SessionID)
	if len(counts) > 0 {
		total := 0
		for _, c := range counts {
			total += c
		}
		fmt.Fprintf(w, "\nEvents by Tool (%d total):\n", total)
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
			fmt.Fprintf(w, "  %-12s %d\n", tc.name, tc.count)
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
