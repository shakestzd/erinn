package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

func init() {
	// Attribution-fix migration is registered in migrateCmd() via addAttributionFixCmd.
}

// addAttributionFixCmd registers the attribution-fix subcommand onto the parent migrate command.
func addAttributionFixCmd(parent *cobra.Command) {
	parent.AddCommand(migrateAttributionFixCmd())
}

func migrateAttributionFixCmd() *cobra.Command {
	var dryRun bool
	var sessionFilter string

	cmd := &cobra.Command{
		Use:   "attribution-fix",
		Short: "Reassign misattributed tool_call events from agent_id='human' to their subagent",
		Long: `For each tool_call event with agent_id='human', finds the nearest earlier
task_delegation in the same session and reassigns agent_id and parent_event_id.

This fixes events that were misattributed because CLAUDE_ENV_FILE was unset
in worktree subagents, causing HTMLGRAPH_AGENT_ID to be absent from hook
subprocess environments.

Use --dry-run to preview counts without modifying the database.
Use --session <id> to pilot the migration on a single session first.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrateAttributionFix(dryRun, sessionFilter)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print counts without modifying data")
	cmd.Flags().StringVar(&sessionFilter, "session", "", "Limit migration to a single session ID")
	return cmd
}

// attributionRow holds a tool_call event that needs reassignment.
type attributionRow struct {
	EventID   string
	SessionID string
	Timestamp time.Time
}

// delegationRow holds a task_delegation event that can be the new parent.
type delegationRow struct {
	EventID   string
	AgentID   string
	Timestamp time.Time
}

// attributionUpdate holds a single event reassignment to apply.
// NewParentEvID is empty string when no parent delegation is applicable (Rule 2).
type attributionUpdate struct {
	EventID       string
	NewAgentID    string
	NewParentEvID string // empty → set parent_event_id to NULL
}

func runMigrateAttributionFix(dryRun bool, sessionFilter string) error {
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

	// Count before.
	beforeCount, err := countMisattributed(database, sessionFilter)
	if err != nil {
		return fmt.Errorf("count misattributed rows: %w", err)
	}
	fmt.Printf("Before: %d tool_call rows with agent_id='human'\n", beforeCount)

	// Fetch all misattributed rows.
	rows, err := fetchMisattributed(database, sessionFilter)
	if err != nil {
		return fmt.Errorf("fetch misattributed rows: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("Nothing to do.")
		return nil
	}

	var reassigned, skippedRule1, reassignedRule2, skipped, ambiguous int
	updates := make([]attributionUpdate, 0, len(rows))

	// Build a set of sessions that have evidence of Claude Code activity (any
	// claude-code agent event or any task_delegation). Used by Rule 2.
	claudeCodeSessions, err := fetchClaudeCodeSessions(database, sessionFilter)
	if err != nil {
		return fmt.Errorf("fetch claude-code sessions: %w", err)
	}

	// For each misattributed row apply two rules in order:
	//   Rule 1: find nearest-earlier task_delegation → reassign to that subagent.
	//   Rule 2: if no prior delegation but session is a Claude Code session,
	//           reassign to "claude-code" (these are the orchestrator's own tool calls
	//           that ran before the first Task() delegation was emitted).
	for i, row := range rows {
		d, isAmbig, err := nearestDelegation(database, row.SessionID, row.Timestamp)
		if err == nil && d != nil {
			// Rule 1 matched.
			if isAmbig {
				ambiguous++
				fmt.Printf("  [warn] event %s at %s: ambiguous overlap (assigned to first match agent=%s)\n",
					truncate(row.EventID, 12), row.Timestamp.Format("15:04:05"), d.AgentID)
			}
			updates = append(updates, attributionUpdate{
				EventID:       rows[i].EventID,
				NewAgentID:    d.AgentID,
				NewParentEvID: d.EventID,
			})
			reassigned++
			continue
		}

		// Rule 2: no prior delegation — check if this is a Claude Code session.
		if _, isCC := claudeCodeSessions[row.SessionID]; isCC {
			updates = append(updates, attributionUpdate{
				EventID:       rows[i].EventID,
				NewAgentID:    "claude-code",
				NewParentEvID: "", // no parent delegation to attach to
			})
			reassignedRule2++
			continue
		}

		skippedRule1++
	}
	skipped = skippedRule1

	fmt.Printf("Plan: %d rows to reassign via Rule 1 (subagent delegation), %d rows via Rule 2 (pre-delegation orchestrator), %d rows skipped (no claude-code evidence), %d rows in ambiguous overlap\n",
		reassigned, reassignedRule2, skippedRule1, ambiguous)

	if dryRun {
		fmt.Println("Dry run — no changes written.")
		return nil
	}

	if err := applyAttributionUpdates(database, updates); err != nil {
		return fmt.Errorf("apply updates: %w", err)
	}

	afterCount, err := countMisattributed(database, sessionFilter)
	if err != nil {
		return fmt.Errorf("count post-migration: %w", err)
	}
	fmt.Printf("After:  %d tool_call rows with agent_id='human'\n", afterCount)
	fmt.Printf("Done: %d reassigned via Rule 1, %d via Rule 2, %d skipped, %d in ambiguous windows.\n",
		reassigned, reassignedRule2, skipped, ambiguous)
	return nil
}

func countMisattributed(db *sql.DB, sessionFilter string) (int, error) {
	// Exclude UserQuery: those are user prompts stored as tool_call events and must
	// keep agent_id='human'.
	query := `SELECT COUNT(*) FROM agent_events WHERE agent_id='human' AND event_type='tool_call' AND tool_name != 'UserQuery'`
	args := []any{}
	if sessionFilter != "" {
		query += ` AND session_id=?`
		args = append(args, sessionFilter)
	}
	var n int
	err := db.QueryRow(query, args...).Scan(&n)
	return n, err
}

func fetchMisattributed(db *sql.DB, sessionFilter string) ([]attributionRow, error) {
	// Exclude UserQuery: those are user prompts stored as tool_call events and must
	// keep agent_id='human'.
	query := `SELECT event_id, session_id, timestamp FROM agent_events
	          WHERE agent_id='human' AND event_type='tool_call' AND tool_name != 'UserQuery'`
	args := []any{}
	if sessionFilter != "" {
		query += ` AND session_id=?`
		args = append(args, sessionFilter)
	}
	query += ` ORDER BY session_id, timestamp ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []attributionRow
	for rows.Next() {
		var r attributionRow
		var ts string
		if err := rows.Scan(&r.EventID, &r.SessionID, &ts); err != nil {
			return nil, err
		}
		t, err := parseFlexTime(ts)
		if err != nil {
			continue
		}
		r.Timestamp = t
		out = append(out, r)
	}
	return out, rows.Err()
}

// nearestDelegation returns the nearest earlier task_delegation for the session.
// isAmbig is true when two or more delegations are active at the event's timestamp
// (i.e., no delegation has ended before this timestamp, meaning overlapping intervals).
func nearestDelegation(db *sql.DB, sessionID string, eventTS time.Time) (*delegationRow, bool, error) {
	tsStr := eventTS.UTC().Format(time.RFC3339Nano)

	// Find the nearest-earlier delegation (regardless of status).
	rows, err := db.Query(`
		SELECT event_id, agent_id, timestamp FROM agent_events
		WHERE session_id=?
		  AND event_type IN ('task_delegation','delegation')
		  AND timestamp <= ?
		  AND agent_id != 'human'
		ORDER BY timestamp DESC
		LIMIT 2`, sessionID, tsStr)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var results []delegationRow
	for rows.Next() {
		var d delegationRow
		var ts string
		if err := rows.Scan(&d.EventID, &d.AgentID, &ts); err != nil {
			return nil, false, err
		}
		t, err := parseFlexTime(ts)
		if err != nil {
			continue
		}
		d.Timestamp = t
		results = append(results, d)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(results) == 0 {
		return nil, false, nil
	}
	isAmbig := len(results) >= 2
	return &results[0], isAmbig, nil
}

// fetchClaudeCodeSessions returns the set of session IDs that have evidence of
// Claude Code activity: any agent_event with agent_id='claude-code' OR any
// task_delegation event. Sessions that only contain 'human' activity are left
// out so they are not misattributed (they might be genuine CLI sessions).
func fetchClaudeCodeSessions(db *sql.DB, sessionFilter string) (map[string]struct{}, error) {
	query := `SELECT DISTINCT session_id FROM agent_events
	          WHERE (agent_id='claude-code'
	             OR event_type IN ('task_delegation','delegation'))`
	args := []any{}
	if sessionFilter != "" {
		query += ` AND session_id=?`
		args = append(args, sessionFilter)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	set := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = struct{}{}
	}
	return set, rows.Err()
}

func applyAttributionUpdates(db *sql.DB, updates []attributionUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(`UPDATE agent_events SET agent_id=?, parent_event_id=?, updated_at=? WHERE event_id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, u := range updates {
		var parentEvID any
		if u.NewParentEvID != "" {
			parentEvID = u.NewParentEvID
		}
		if _, err := stmt.Exec(u.NewAgentID, parentEvID, now, u.EventID); err != nil {
			return fmt.Errorf("update event %s: %w", u.EventID, err)
		}
	}
	return tx.Commit()
}

// parseFlexTime parses timestamps stored in several formats used in the DB.
func parseFlexTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp: %q", s)
}
