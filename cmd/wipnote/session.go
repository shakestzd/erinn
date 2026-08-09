// Register in main.go: rootCmd.AddCommand(sessionCmd())
package main

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/claimledger"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/gateledger"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/sessionledger"
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
	cmd.AddCommand(sessionLedgerCmd())
	return cmd
}

// sessionListCmd lists sessions from the canonical session ledger.
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

	sessions, err := sessionsFromLedger(dir, activeOnly, limit)
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

func sessionsFromLedger(wipnoteDir string, activeOnly bool, limit int) ([]*models.Session, error) {
	records, err := sessionledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		return nil, err
	}
	sessions := make([]*models.Session, 0, len(records))
	for _, r := range records {
		if activeOnly && !r.IsOpen() {
			continue
		}
		sessions = append(sessions, sessionModelFromLedger(r))
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].CreatedAt.Equal(sessions[j].CreatedAt) {
			return sessions[i].SessionID > sessions[j].SessionID
		}
		return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
	})
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

func sessionModelFromLedger(r sessionledger.Record) *models.Session {
	status := "completed"
	var completedAt *time.Time
	if r.IsOpen() {
		status = "active"
	} else {
		ended := r.EndedAt
		completedAt = &ended
	}
	return &models.Session{
		SessionID:     r.SessionID,
		AgentAssigned: r.Harness,
		CreatedAt:     r.StartedAt,
		CompletedAt:   completedAt,
		TotalEvents:   r.Events,
		Status:        status,
		Harness:       r.Harness,
		ProjectDir:    r.ProjectDir,
	}
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

// runSessionStart opens the session in the CANONICAL sessions ledger.
//
// This was the exact twin of the bug-aa0bbd43 defect fixed in runSessionEnd
// below: it routed via apply.RouteSessionInsert, which lands in the writer
// daemon's own ephemeral projection, and on a daemon miss fell back to
// dbpkg.InsertSession against the local openDB handle that the deferred Close
// discarded. Neither branch wrote the ledger, so `wipnote session start`
// printed an id that no later command — including `wipnote session end` — could
// find. Only the manual CLI path was affected; the hook-driven start
// (core/hooks/session_ledger.go:37) always wrote the ledger.
//
// Store.Open is idempotent and fires the same OnCommit seam the hook uses, so
// there is no git call here.
func runSessionStart(agent string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	record := sessionledger.Record{
		SessionID:  newLedgerSessionID(),
		Harness:    agent,
		ProjectDir: paths.NormalizeProjectDir(filepath.Dir(dir)),
		StartedAt:  time.Now().UTC(),
	}
	if _, err := sessionledger.NewStore(dir).Open(record); err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	fmt.Printf("Started session: %s\n", record.SessionID)
	return nil
}

// newLedgerSessionID mints an id the canonical ledger will accept.
//
// It does NOT use generateSessionID: that produces sess-<8 hex>, which
// graph.IsSessionShapedID rejects (it admits a dashed UUID or 28 undashed hex,
// with an optional sess- prefix), so sessionledger.Record.Validate would refuse
// every row this command wrote. The shape is load-bearing rather than
// cosmetic — it is the only signal the edge-target gate has for telling a
// pruned session from a reference to something that never existed — so the id
// has to be a real UUID, not a shorter token dressed up as one.
func newLedgerSessionID() string { return uuid.NewString() }

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

// runSessionEnd closes the session in the CANONICAL sessions ledger.
//
// bug-aa0bbd43: this used to route the transition through the writer daemon
// (apply.RouteSessionStatus) and, on a daemon miss, fall back to
// dbpkg.UpdateSessionStatus against the throwaway in-memory projection that
// openDB hands out — a projection closed moments later. Neither branch was
// durable once the project DB became process-local: the daemon applies
// OpTypeSessionStatus with db.UpdateSessionStatus against ITS OWN ephemeral
// projection (core/daemon/apply/apply.go:382-389), and the fallback wrote to a
// handle nobody else could see. The command printed "Ended session" and the
// session stayed open forever.
//
// The ledger is the authority for whether a session is open (core/sessionledger
// — one git-tracked row per root session), so the close is written straight
// there. No daemon round-trip: the daemon has nothing durable to offer for this
// op, and the ledger write is already cross-process safe via filelock.Guard.
func runSessionEnd(sessionID string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store := sessionledger.NewStore(dir)

	if sessionID == "" {
		// Most recent OPEN session, straight from canonical state:
		// sessionsFromLedger already filters on Record.IsOpen and sorts by
		// start time descending.
		open, err := sessionsFromLedger(dir, true, 1)
		if err != nil {
			return fmt.Errorf("find active session: %w", err)
		}
		if len(open) == 0 {
			return fmt.Errorf("no active sessions found\nRun 'wipnote session start' to begin tracking, or specify a session ID explicitly.")
		}
		sessionID = open[0].SessionID
	}

	if err := store.Close(sessionID, time.Now().UTC()); err != nil {
		if errors.Is(err, sessionledger.ErrNoRow) {
			return fmt.Errorf("session %q has no row in %s (it predates the sessions ledger)\nRun 'wipnote session list' to see known sessions.",
				sessionID, store.RelPath())
		}
		return fmt.Errorf("end session: %w", err)
	}
	fmt.Printf("Ended session: %s\n", sessionID)
	return nil
}

// openDB returns a private in-memory compatibility projection hydrated from
// canonical .wipnote artifacts. It never creates a per-project SQLite file.
func openDB(wipnoteDir string) (*sql.DB, error) {
	db, err := dbpkg.OpenEphemeralProjection()
	if err != nil {
		return nil, fmt.Errorf("open ephemeral database: %w", err)
	}
	if err := hydrateCompatibilityDB(db, wipnoteDir); err != nil {
		db.Close()
		return nil, fmt.Errorf("hydrate ephemeral database: %w", err)
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
	return openDB(wipnoteDir)
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

	record, found, err := sessionledger.NewStore(dir).Get(sessionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("session %q not found\nRun 'wipnote session list' to see known sessions.", sessionID)
	}
	s := sessionModelFromLedger(record)
	features, err := claimFeaturesForSession(dir, sessionID)
	if err != nil {
		return err
	}
	if s.ActiveFeatureID == "" && len(features) > 0 {
		s.ActiveFeatureID = features[0]
	}
	gates, err := gatesForSession(dir, sessionID)
	if err != nil {
		return err
	}

	return renderSessionShowCanonical(os.Stdout, s, features, gates)
}

func claimFeaturesForSession(wipnoteDir, sessionID string) ([]string, error) {
	episodes, err := claimledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	for _, e := range episodes {
		if e.SessionID != sessionID && e.RootSessionID != sessionID {
			continue
		}
		if seen[e.WorkItemID] {
			continue
		}
		seen[e.WorkItemID] = true
		out = append(out, e.WorkItemID)
	}
	return out, nil
}

func gatesForSession(wipnoteDir, sessionID string) ([]gateledger.Record, error) {
	records, err := gateledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []gateledger.Record
	for _, r := range records {
		if r.SessionID == sessionID {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CheckedAt.After(out[j].CheckedAt)
	})
	return out, nil
}

func renderSessionShowCanonical(w io.Writer, s *models.Session, features []string, gates []gateledger.Record) error {
	if err := renderSessionSummary(w, s); err != nil {
		return err
	}
	if len(features) > 0 {
		fmt.Fprintln(w, "\nFeatures Worked On:")
		for _, f := range features {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	if len(gates) > 0 {
		fmt.Fprintln(w, "\nGates:")
		for _, g := range gates {
			fmt.Fprintf(w, "  %-8s  %-19s  %s\n",
				g.Status, g.CheckedAt.Local().Format("2006-01-02 15:04:05"), g.GateCommand)
		}
	}
	return nil
}

func renderSessionSummary(w io.Writer, s *models.Session) error {
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
	if s.ActiveFeatureID != "" {
		fmt.Fprintf(w, "  Feature   %s\n", s.ActiveFeatureID)
	}
	return nil
}

func renderSessionShow(w io.Writer, db *sql.DB, s *models.Session) error {
	if err := renderSessionSummary(w, s); err != nil {
		return err
	}
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
