package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/hooks"
	"github.com/shakestzd/htmlgraph/internal/ingest"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

func migrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "One-shot data migrations",
	}
	cmd.AddCommand(migrateSessionsCmd())
	addAttributionFixCmd(cmd)
	return cmd
}

func migrateSessionsCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Backfill session HTML files for SQLite-only sessions",
		Long: `Finds session rows that have no corresponding HTML file in
.htmlgraph/sessions/ and renders one for each so the reindex round-trip
works. Prefers re-parsing the original JSONL transcript when it is still
available in ~/.claude/projects/; falls back to rendering from the SQLite
rows when the transcript has been pruned.

Idempotent — sessions that already have an HTML file are left alone.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrateSessions(dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list orphan sessions without writing files")
	return cmd
}

func runMigrateSessions(dryRun bool) error {
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

	present, err := existingSessionHTMLSet(htmlgraphDir)
	if err != nil {
		return fmt.Errorf("scan session html files: %w", err)
	}

	orphans, err := selectOrphanSessions(database, present)
	if err != nil {
		return fmt.Errorf("query orphan sessions: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Println("No orphan sessions — every SQLite session row has a matching HTML file.")
		return nil
	}

	// Build a session-id → JSONL path index once so we don't rescan for every orphan.
	jsonlIndex := discoverJSONLIndex()

	var migratedJSONL, migratedSQLite, skipped, errCount int
	for _, sessionID := range orphans {
		source := "sqlite"
		if _, ok := jsonlIndex[sessionID]; ok {
			source = "jsonl"
		}

		if dryRun {
			fmt.Printf("  %s: source=%s -> %s\n", truncate(sessionID, 14), source,
				filepath.Join(htmlgraphDir, "sessions", sessionID+".html"))
			continue
		}

		err := migrateOneSession(database, htmlgraphDir, sessionID, source, jsonlIndex)
		switch {
		case err == nil && source == "jsonl":
			migratedJSONL++
			fmt.Printf("  %s: source=jsonl -> rendered\n", truncate(sessionID, 14))
		case err == nil:
			migratedSQLite++
			fmt.Printf("  %s: source=sqlite -> rendered\n", truncate(sessionID, 14))
		case err == errNoData:
			skipped++
			fmt.Printf("  %s: SKIPPED — no data to render\n", truncate(sessionID, 14))
		default:
			errCount++
			fmt.Printf("  %s: ERROR %v\n", truncate(sessionID, 14), err)
		}
	}

	if dryRun {
		fmt.Printf("\nDry run: %d orphan sessions would be migrated\n", len(orphans))
		return nil
	}

	fmt.Printf("\nMigrated %d sessions (%d from JSONL, %d from SQLite fallback, %d skipped, %d errors)\n",
		migratedJSONL+migratedSQLite, migratedJSONL, migratedSQLite, skipped, errCount)
	return nil
}

// errNoData signals that an orphan session has neither a JSONL transcript nor
// SQLite rows, so there is nothing meaningful to render.
var errNoData = fmt.Errorf("no data available to render session")

// existingSessionHTMLSet returns the set of session IDs that already have an
// HTML file in .htmlgraph/sessions/.
func existingSessionHTMLSet(htmlgraphDir string) (map[string]struct{}, error) {
	pattern := filepath.Join(htmlgraphDir, "sessions", "*.html")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(files))
	for _, f := range files {
		id := filepath.Base(f)
		id = id[:len(id)-len(".html")]
		set[id] = struct{}{}
	}
	return set, nil
}

// selectOrphanSessions returns every session_id in the sessions table that
// does not have a corresponding HTML file.
func selectOrphanSessions(database *sql.DB, present map[string]struct{}) ([]string, error) {
	rows, err := database.Query(`SELECT session_id FROM sessions ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orphans []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if _, ok := present[id]; ok {
			continue
		}
		orphans = append(orphans, id)
	}
	return orphans, rows.Err()
}

// discoverJSONLIndex scans ~/.claude/projects/ once and returns a map from
// session_id to the JSONL file path. Used by migrateOneSession to avoid
// re-scanning for every orphan.
func discoverJSONLIndex() map[string]ingest.SessionFile {
	idx := map[string]ingest.SessionFile{}
	files, err := ingest.DiscoverSessions("")
	if err != nil {
		return idx
	}
	for _, sf := range files {
		idx[sf.SessionID] = sf
	}
	return idx
}

// migrateOneSession renders a single orphan session's HTML file. Prefers
// re-parsing the JSONL transcript for maximum fidelity; falls back to SQLite
// when the transcript is gone.
func migrateOneSession(database *sql.DB, htmlgraphDir, sessionID, source string, jsonlIndex map[string]ingest.SessionFile) error {
	projectDir := sessionProjectDir(database, sessionID)

	if source == "jsonl" {
		sf, ok := jsonlIndex[sessionID]
		if ok {
			result, err := ingest.ParseFile(sf.Path)
			if err == nil && len(result.Messages) > 0 {
				return hooks.RenderIngestedSessionHTML(htmlgraphDir, sessionID, projectDir, result, false)
			}
			// JSONL parse failed or empty — fall through to SQLite fallback.
		}
	}

	result, err := buildParseResultFromSQLite(database, sessionID)
	if err != nil {
		return err
	}
	if len(result.ToolCalls) == 0 && len(result.Messages) == 0 {
		return errNoData
	}
	return hooks.RenderIngestedSessionHTML(htmlgraphDir, sessionID, projectDir, result, false)
}

// sessionProjectDir returns the project_dir column for a session, or "".
func sessionProjectDir(database *sql.DB, sessionID string) string {
	var dir sql.NullString
	_ = database.QueryRow(
		`SELECT project_dir FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&dir)
	return dir.String
}

// buildParseResultFromSQLite reconstructs a minimal ParseResult from the
// stored messages and tool_calls rows so the renderer can emit an HTML file
// for sessions whose original transcript is no longer available.
func buildParseResultFromSQLite(database *sql.DB, sessionID string) (*ingest.ParseResult, error) {
	msgs, err := listMessagesASC(database, sessionID)
	if err != nil {
		return nil, err
	}
	calls, err := dbpkg.ListToolCalls(database, sessionID)
	if err != nil {
		return nil, err
	}
	return &ingest.ParseResult{
		SessionID: sessionID,
		Messages:  msgs,
		ToolCalls: calls,
	}, nil
}

// listMessagesASC returns all messages for a session in chronological order.
// dbpkg.ListMessages orders DESC and caps at 500 — the backfill needs every
// row in insert order so the renderer's timestamp lookup works correctly.
func listMessagesASC(database *sql.DB, sessionID string) ([]models.Message, error) {
	rows, err := database.Query(`
		SELECT ordinal, role, COALESCE(content, ''), timestamp
		FROM messages
		WHERE session_id = ?
		ORDER BY ordinal ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Message
	for rows.Next() {
		var m models.Message
		var ts string
		if err := rows.Scan(&m.Ordinal, &m.Role, &m.Content, &ts); err != nil {
			return nil, err
		}
		if t, perr := time.Parse(time.RFC3339Nano, ts); perr == nil {
			m.Timestamp = t
		} else if t, perr := time.Parse(time.RFC3339, ts); perr == nil {
			m.Timestamp = t
		} else if t, perr := time.Parse("2006-01-02 15:04:05", ts); perr == nil {
			m.Timestamp = t
		}
		m.SessionID = sessionID
		out = append(out, m)
	}
	return out, rows.Err()
}
