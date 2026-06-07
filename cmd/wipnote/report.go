// Register in main.go: rootCmd.AddCommand(reportCmd())
package main

import (
	"fmt"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

func reportCmd() *cobra.Command {
	var summaryOnly bool

	cmd := &cobra.Command{
		Use:   "report [session-id]",
		Short: "What Did Claude Do? — timeline of a session's activity",
		Long: `Display a timeline of tool calls and delegations for a session.

If no session-id is given, the most recent session is used.

Example:
  wipnote report
  wipnote report sess-abc123
  wipnote report --summary`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sessionID := ""
			if len(args) > 0 {
				sessionID = args[0]
			}
			return runReport(sessionID, summaryOnly)
		},
	}
	cmd.Flags().BoolVar(&summaryOnly, "summary", false, "Print only summary stats (no timeline)")
	return cmd
}

// runReport orchestrates session lookup, event fetch, and display.
func runReport(sessionID string, summaryOnly bool) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	database, err := openDB(dir)
	if err != nil {
		return err
	}
	defer database.Close()

	if sessionID == "" {
		sessionID, err = dbpkg.MostRecentSession(database)
		if err != nil {
			return fmt.Errorf("find most recent session: %w", err)
		}
		if sessionID == "" {
			return fmt.Errorf("no sessions found in the database\nRun 'wipnote ingest' to import Claude Code session transcripts, or 'wipnote session start' to begin tracking.")
		}
	}

	session, err := dbpkg.GetSession(database, sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}

	events, err := dbpkg.ListEventsBySessionAsc(database, sessionID, 0)
	if err != nil {
		return fmt.Errorf("load events: %w", err)
	}

	if summaryOnly {
		printReportSummary(session, events)
		return nil
	}

	printReportHeader(session)
	printTimeline(events)
	printReportSummary(session, events)
	return nil
}

// printReportHeader prints the session header block.
func printReportHeader(s *models.Session) {
	start := s.CreatedAt.Local().Format("2006-01-02 15:04")
	end := "active"
	dur := "ongoing"

	if s.CompletedAt != nil {
		end = s.CompletedAt.Local().Format("15:04")
		dur = fmtDuration(s.CompletedAt.Sub(s.CreatedAt))
	}

	fmt.Printf("Session: %s  (%s — %s, %s)\n", s.SessionID, start, end, dur)
	if s.ActiveFeatureID != "" {
		fmt.Printf("Feature: %s\n", s.ActiveFeatureID)
	}
	if s.AgentAssigned != "" {
		fmt.Printf("Agent:   %s\n", s.AgentAssigned)
	}
	fmt.Println()
}

// printTimeline displays the hierarchical event timeline.
func printTimeline(events []models.AgentEvent) {
	childrenOf := buildChildIndex(events)
	printed := make(map[string]bool)

	for _, e := range events {
		if e.ParentEventID != "" {
			continue // printed recursively as a child
		}
		printEventRow(e, 0, childrenOf, printed)
	}
	fmt.Println()
}

// buildChildIndex maps parent_event_id → slice of child events.
func buildChildIndex(events []models.AgentEvent) map[string][]models.AgentEvent {
	m := make(map[string][]models.AgentEvent, len(events))
	for _, e := range events {
		if e.ParentEventID != "" {
			m[e.ParentEventID] = append(m[e.ParentEventID], e)
		}
	}
	return m
}

// printEventRow prints one event at the given indent depth, then recurses into children.
func printEventRow(
	e models.AgentEvent,
	depth int,
	childrenOf map[string][]models.AgentEvent,
	printed map[string]bool,
) {
	if printed[e.EventID] {
		return
	}
	printed[e.EventID] = true

	indent := strings.Repeat("  ", depth)
	ts := e.Timestamp.Local().Format("15:04")
	label := eventLabel(e)
	extra := eventExtra(e)

	if extra != "" {
		fmt.Printf("%s%s  %-16s  %s\n", indent, ts, label, extra)
	} else {
		fmt.Printf("%s%s  %s\n", indent, ts, label)
	}

	for _, child := range childrenOf[e.EventID] {
		printEventRow(child, depth+1, childrenOf, printed)
	}
}

// eventLabel returns the display label for an event (tool name or type).
func eventLabel(e models.AgentEvent) string {
	if e.ToolName != "" {
		return e.ToolName
	}
	return string(e.EventType)
}

// eventExtra returns supplemental info to display after the label.
func eventExtra(e models.AgentEvent) string {
	switch {
	case e.SubagentType != "":
		return fmt.Sprintf("→ %s", e.SubagentType)
	case e.FeatureID != "":
		return fmt.Sprintf("[%s]", e.FeatureID)
	default:
		return ""
	}
}

// printReportSummary prints the summary stats footer.
func printReportSummary(s *models.Session, events []models.AgentEvent) {
	stats := tallyReportStats(events)
	fmt.Printf("Summary: %d tool calls", stats.toolCalls)
	if stats.delegations > 0 {
		fmt.Printf(", %d delegation(s)", stats.delegations)
	}
	if stats.directEdits > 0 {
		fmt.Printf(", %d direct edit(s)", stats.directEdits)
	}
	if stats.elapsed > 0 {
		fmt.Printf(", session duration %s", fmtDuration(stats.elapsed))
	}
	fmt.Println()
	_ = s // session available for future enrichment
}

// reportStats holds aggregated stats for the summary line.
type reportStats struct {
	toolCalls   int
	delegations int
	directEdits int
	elapsed     time.Duration
}

// tallyReportStats counts tool calls, delegations, and direct edits.
func tallyReportStats(events []models.AgentEvent) reportStats {
	var s reportStats
	for _, e := range events {
		switch {
		case e.ToolName == "Task" || e.SubagentType != "":
			s.delegations++
			s.toolCalls++
		case e.ToolName == "Edit" || e.ToolName == "Write":
			s.directEdits++
			s.toolCalls++
		case e.ToolName != "":
			s.toolCalls++
		}
	}
	if len(events) >= 2 {
		first := events[0].Timestamp
		last := events[len(events)-1].Timestamp
		if last.After(first) {
			s.elapsed = last.Sub(first)
		}
	}
	return s
}
