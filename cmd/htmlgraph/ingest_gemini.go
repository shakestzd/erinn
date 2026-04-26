package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/hooks"
	"github.com/shakestzd/htmlgraph/internal/ingest"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

// ingestGeminiCmd returns the `htmlgraph ingest --tool gemini` subcommand.
func ingestGeminiCmd() *cobra.Command {
	var (
		project string
		all     bool
		force   bool
	)

	cmd := &cobra.Command{
		Use:   "gemini",
		Short: "Ingest Gemini CLI session transcripts",
		Long: `Reads Gemini CLI session JSON files from ~/.gemini/tmp/ and stores
structured messages and tool calls in the HtmlGraph database.

By default, discovers sessions for the current project. Use --all to
ingest all projects, or --project to target a specific project slug.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runIngestGemini(project, all, force)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "filter by project slug or path")
	cmd.Flags().BoolVar(&all, "all", false, "ingest all discovered Gemini sessions")
	cmd.Flags().BoolVar(&force, "force", false, "re-ingest even if already synced")

	return cmd
}

func runIngestGemini(project string, all, force bool) error {
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

	// Determine project filter: use CWD-based project path unless --all or --project set.
	projectFilter := project
	if projectFilter == "" && !all {
		projectFilter = filepath.Dir(htmlgraphDir)
	}

	files, err := ingest.DiscoverGeminiSessions(projectFilter)
	if err != nil {
		return fmt.Errorf("discover gemini sessions: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("No Gemini session files found.")
		return nil
	}

	fmt.Printf("Found %d Gemini session files", len(files))
	if projectFilter != "" {
		fmt.Printf(" (project filter: %q)", projectFilter)
	}
	fmt.Println()

	var ingested, skipped, errCount int
	for _, sf := range files {
		if !force {
			count, _ := dbpkg.CountMessages(database, sf.SessionID)
			if count > 0 {
				skipped++
				continue
			}
		}

		n, toolN, err := ingestGeminiFileWithDB(database, htmlgraphDir, sf, force)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", truncate(sf.SessionID, 12), err)
			errCount++
			continue
		}
		if n == 0 {
			skipped++
			continue
		}

		fmt.Printf("  %-14s %3d msgs  %3d tools  (%s)\n",
			truncate(sf.SessionID, 14), n, toolN, sf.Project)
		ingested++
	}

	fmt.Printf("\nDone: %d ingested, %d skipped, %d errors\n", ingested, skipped, errCount)
	return nil
}

// ingestGeminiFileWithDB ingests a single Gemini session JSON file into the database.
func ingestGeminiFileWithDB(database *sql.DB, htmlgraphDir string, sf ingest.GeminiSessionFile, force bool) (int, int, error) {
	result, err := ingest.ParseGeminiFile(sf.Path)
	if err != nil {
		return 0, 0, err
	}
	if len(result.Messages) == 0 {
		return 0, 0, nil
	}

	if force {
		_ = dbpkg.DeleteSessionMessages(database, sf.SessionID)
		_ = dbpkg.DeleteSessionIngestEvents(database, sf.SessionID)
	}

	ensureGeminiSession(database, sf.SessionID, result, filepath.Dir(htmlgraphDir))
	msgCount, toolCount := storeGeminiParseResult(database, sf.SessionID, result)

	if rerr := hooks.RenderIngestedSessionHTML(htmlgraphDir, sf.SessionID, filepath.Dir(htmlgraphDir), result, force); rerr != nil {
		fmt.Fprintf(os.Stderr, "  warn: render HTML for %s: %v\n", truncate(sf.SessionID, 12), rerr)
	}

	return msgCount, toolCount, nil
}

func ensureGeminiSession(database *sql.DB, sessionID string, result *ingest.ParseResult, projectDir string) {
	var exists int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID).Scan(&exists)
	if exists > 0 {
		if projectDir != "" {
			database.Exec(
				`UPDATE sessions SET project_dir = ? WHERE session_id = ? AND (project_dir IS NULL OR project_dir = '')`,
				projectDir, sessionID,
			)
		}
		return
	}

	ts := ""
	if len(result.Messages) > 0 {
		ts = result.Messages[0].Timestamp.UTC().Format("2006-01-02T15:04:05Z")
	}

	database.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, created_at, status, model, project_dir)
		VALUES (?, 'gemini', COALESCE(NULLIF(?, ''), CURRENT_TIMESTAMP), 'completed', ?, ?)`,
		sessionID, ts, nullStrVal(result.Model), projectDir,
	)
}

func storeGeminiParseResult(database *sql.DB, sessionID string, result *ingest.ParseResult) (int, int) {
	var msgCount, toolCount int

	msgIDs := map[int]int64{}
	for _, m := range result.Messages {
		m.SessionID = sessionID
		id, err := dbpkg.InsertMessage(database, &m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    warn: msg ord %d: %v\n", m.Ordinal, err)
			continue
		}
		msgIDs[m.Ordinal] = id
		msgCount++
	}

	activeFeatureID := sessionActiveFeature(database, sessionID)

	msgTimestamps := make(map[int]time.Time, len(result.Messages))
	for _, m := range result.Messages {
		msgTimestamps[m.Ordinal] = m.Timestamp
	}

	now := time.Now().UTC()
	for i, tc := range result.ToolCalls {
		tc.SessionID = sessionID
		if mid, ok := msgIDs[tc.MessageOrdinal]; ok {
			tc.MessageID = int(mid)
		}
		if activeFeatureID != "" {
			tc.FeatureID = activeFeatureID
		}
		if err := dbpkg.InsertToolCall(database, &tc); err != nil {
			fmt.Fprintf(os.Stderr, "    warn: tool %s: %v\n", tc.ToolName, err)
			continue
		}
		toolCount++

		evtID := ingest.EventID(sessionID, tc.ToolUseID, tc.ToolName, i)
		ts := now
		if t, ok := msgTimestamps[tc.MessageOrdinal]; ok {
			ts = t
		}
		tsStr := ts.UTC().Format(time.RFC3339)
		if exists, _ := dbpkg.HasHookEventAt(database, sessionID, tc.ToolName, tsStr); exists {
			continue
		}

		ev := &models.AgentEvent{
			EventID:      evtID,
			AgentID:      "gemini",
			EventType:    models.EventToolCall,
			Timestamp:    ts,
			ToolName:     tc.ToolName,
			InputSummary: truncate(tc.InputJSON, 200),
			ToolInput:    tc.InputJSON,
			SessionID:    sessionID,
			FeatureID:    activeFeatureID,
			Status:       "completed",
			Source:       "ingest",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		_ = dbpkg.UpsertEvent(database, ev)
	}

	if result.Model != "" {
		database.Exec(`UPDATE sessions SET model = ? WHERE session_id = ? AND (model IS NULL OR model = '')`,
			result.Model, sessionID)
	}

	return msgCount, toolCount
}
