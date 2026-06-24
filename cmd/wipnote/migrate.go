package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corearch "github.com/shakestzd/wipnote/core/arch"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/ingest"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

func migrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "One-shot data migrations",
	}
	cmd.AddCommand(migrateSessionsCmd())
	cmd.AddCommand(migrateNormalizePathsCmd())
	cmd.AddCommand(migrateRestorePathsCmd())
	cmd.AddCommand(migrateArchCardsCmd())
	addAttributionFixCmd(cmd)
	return cmd
}

func migrateSessionsCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Backfill session HTML files for SQLite-only sessions",
		Long: `Finds session rows that have no corresponding HTML file in
.wipnote/sessions/ and renders one for each so the reindex round-trip
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
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	printProjectHeaderIfDifferent(wipnoteDir)
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(wipnoteDir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	present, err := existingSessionHTMLSet(wipnoteDir)
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
				filepath.Join(wipnoteDir, "sessions", sessionID+".html"))
			continue
		}

		err := migrateOneSession(database, wipnoteDir, sessionID, source, jsonlIndex)
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
// HTML file in .wipnote/sessions/.
func existingSessionHTMLSet(wipnoteDir string) (map[string]struct{}, error) {
	pattern := filepath.Join(wipnoteDir, "sessions", "*.html")
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
func migrateOneSession(database *sql.DB, wipnoteDir, sessionID, source string, jsonlIndex map[string]ingest.SessionFile) error {
	projectDir := sessionProjectDir(database, sessionID)

	if source == "jsonl" {
		sf, ok := jsonlIndex[sessionID]
		if ok {
			result, err := ingest.ParseFile(sf.Path)
			if err == nil && len(result.Messages) > 0 {
				return hooks.RenderIngestedSessionHTML(wipnoteDir, sessionID, projectDir, result, false)
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
	return hooks.RenderIngestedSessionHTML(wipnoteDir, sessionID, projectDir, result, false)
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

// migrateArchCardsCmd wires `wipnote migrate arch-cards` onto the parent
// migrate command. It ingests every .md arch card found under
// .wipnote/arch/ into the canonical architecture.html ledger and removes
// the migrated .md file. The operation is idempotent — cards already present
// in the ledger are skipped.
func migrateArchCardsCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "arch-cards",
		Short: "Ingest legacy arch markdown cards into architecture.html",
		Long: `Walk .wipnote/arch/*.md and migrate each card into the canonical
HTML ledger (architecture.html). Cards whose slug already exists in the
ledger are skipped (idempotent). On success the .md file is removed.

Run twice — the second run is a no-op.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrateArchCards(dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would happen but make no changes")
	return cmd
}

func runMigrateArchCards(dryRun bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	printProjectHeaderIfDifferent(wipnoteDir)

	archDir := filepath.Join(wipnoteDir, "arch")
	entries, err := os.ReadDir(archDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No .wipnote/arch/ directory — nothing to migrate.")
			return nil
		}
		return fmt.Errorf("read arch dir: %w", err)
	}

	var mdFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".md" {
			mdFiles = append(mdFiles, filepath.Join(archDir, e.Name()))
		}
	}

	if len(mdFiles) == 0 {
		fmt.Println("No .md arch cards found — nothing to migrate.")
		return nil
	}

	// Build the set of slugs already in the ledger for idempotency checks.
	// We read this once upfront so we don't re-scan on every card.
	ledgerSlugs := make(map[string]bool)
	if existing, readErr := corearch.ReadLedger(corearch.LedgerPath(wipnoteDir)); readErr == nil {
		for _, c := range existing {
			ledgerSlugs[c.Name] = true
		}
	}
	// If ReadLedger errors (e.g. no ledger yet), we proceed with an empty map —
	// all cards will be treated as needing migration.

	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return fmt.Errorf("open arch store: %w", err)
	}

	var migrated, skipped, errCount int
	for _, path := range mdFiles {
		card, parseErr := corearch.ParseFile(path)
		if parseErr != nil {
			errCount++
			fmt.Printf("  %s: ERROR parsing: %v\n", filepath.Base(path), parseErr)
			continue
		}

		// Emit advisory path warnings (non-blocking).
		if ws := corearch.ValidatePaths(card.Paths); len(ws) > 0 {
			for _, w := range ws {
				fmt.Printf("  %s: WARNING %s\n", card.Name, w)
			}
		}

		// Idempotency: if the slug is already in the ledger, skip.
		if ledgerSlugs[card.Name] {
			skipped++
			fmt.Printf("  %s: skipped (already in ledger)\n", card.Name)
			continue
		}

		if dryRun {
			fmt.Printf("  %s: would migrate -> architecture.html\n", card.Name)
			migrated++
			continue
		}

		// Remove the .md file BEFORE calling store.Create so the store's
		// duplicate-file check (resolveCardPath) does not mistake the source
		// .md for a pre-existing card. The card content is already parsed
		// into `card`, so removal is safe. On store.Create failure we attempt
		// to restore the file so data is not silently lost.
		mdContent, readErr := os.ReadFile(path)
		if readErr != nil {
			errCount++
			fmt.Printf("  %s: ERROR reading .md for backup: %v\n", card.Name, readErr)
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			errCount++
			fmt.Printf("  %s: ERROR removing .md before create: %v\n", card.Name, removeErr)
			continue
		}

		createErr := store.Create(card)
		if createErr != nil {
			// Restore the .md file so the operator can retry.
			if restoreErr := os.WriteFile(path, mdContent, 0o644); restoreErr != nil {
				fmt.Printf("  %s: ERROR %v (also failed to restore .md: %v)\n", card.Name, createErr, restoreErr)
			} else {
				fmt.Printf("  %s: ERROR %v (.md restored)\n", card.Name, createErr)
			}
			errCount++
			continue
		}

		migrated++
		ledgerSlugs[card.Name] = true // update for subsequent iterations
		fmt.Printf("  %s: migrated\n", card.Name)
	}

	if dryRun {
		fmt.Printf("\nDry run: %d card(s) would be migrated\n", migrated)
		return nil
	}
	fmt.Printf("\nMigrated %d / skipped %d / errors %d\n", migrated, skipped, errCount)
	return nil
}
