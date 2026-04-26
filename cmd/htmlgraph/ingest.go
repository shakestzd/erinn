package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/hooks"
	"github.com/shakestzd/htmlgraph/internal/ingest"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/paths"
	"github.com/spf13/cobra"
)

func ingestCmd() *cobra.Command {
	var (
		sessionID string
		project   string
		all       bool
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Ingest Claude Code session transcripts from JSONL files",
		Long: `Reads Claude Code session JSONL files from ~/.claude/projects/ and
stores structured messages and tool calls in the HtmlGraph database.

By default, discovers sessions for the current project. Use --all to
ingest all projects, or --session to target a specific session.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runIngest(sessionID, project, all, force)
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "ingest a specific session ID")
	cmd.Flags().StringVar(&project, "project", "", "filter by project name (substring match)")
	cmd.Flags().BoolVar(&all, "all", false, "ingest all discovered sessions")
	cmd.Flags().BoolVar(&force, "force", false, "re-ingest even if already synced")

	cmd.AddCommand(ingestCommitsCmd())
	cmd.AddCommand(ingestGeminiCmd())

	return cmd
}

func runIngest(sessionID, project string, all, force bool) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	printProjectHeaderIfDifferent(htmlgraphDir)

	database, err := openDB(htmlgraphDir)
	if err != nil {
		return err
	}
	defer database.Close()

	// Single session mode: find the file by scanning all projects.
	if sessionID != "" {
		return ingestBySessionID(database, sessionID, force)
	}

	// Resolve the git remote URL for the current project to use as a filter.
	// When --all is set or --project is explicitly provided, skip the remote filter.
	var gitRemote string
	if project == "" && !all {
		gitRemote = paths.GetGitRemoteURL(filepath.Dir(htmlgraphDir))
	}

	files, err := ingest.DiscoverSessions(project)
	if err != nil {
		return fmt.Errorf("discover sessions: %w", err)
	}

	// Apply git remote filter when we have a resolved remote and no explicit
	// project name or --all flag was provided.
	if gitRemote != "" {
		files = ingest.FilterByGitRemote(files, gitRemote)
	}

	if len(files) == 0 {
		fmt.Println("No session files found.")
		return nil
	}

	fmt.Printf("Found %d session files", len(files))
	switch {
	case gitRemote != "":
		fmt.Printf(" (git remote filter: %q)", gitRemote)
	case project != "":
		fmt.Printf(" (project filter: %q)", project)
	}
	fmt.Println()

	var ingested, skipped, errCount int
	var subIngested, subSkipped int
	for _, sf := range files {
		if !force {
			count, _ := dbpkg.CountMessages(database, sf.SessionID)
			if count > 0 {
				skipped++
				continue
			}
		}

		n, toolN, err := ingestFile(database, sf, force)
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

		si, ss, se := ingestSubagents(database, sf, force)
		subIngested += si
		subSkipped += ss
		errCount += se
	}

	fmt.Printf("\nDone: %d ingested, %d skipped, %d errors", ingested, skipped, errCount)
	if subIngested > 0 || subSkipped > 0 {
		fmt.Printf("  (subagents: %d ingested, %d skipped)", subIngested, subSkipped)
	}
	fmt.Println()
	return nil
}

func ingestBySessionID(database *sql.DB, sessionID string, force bool) error {
	files, err := ingest.DiscoverSessions("")
	if err != nil {
		return err
	}
	for _, sf := range files {
		if sf.SessionID == sessionID {
			n, toolN, err := ingestFile(database, sf, force)
			if err != nil {
				return err
			}
			fmt.Printf("Ingested %d messages, %d tool calls from %s\n", n, toolN, sf.Path)
			return nil
		}
	}
	return fmt.Errorf("session %s not found in ~/.claude/projects/\nSession IDs are full UUIDs from Claude Code. Run 'htmlgraph ingest' without --session to discover available sessions.", sessionID)
}

func ingestFile(database *sql.DB, sf ingest.SessionFile, force bool) (int, int, error) {
	return ingestFileWithAgent(database, sf, "", force)
}

// ingestFileWithAgent ingests a single JSONL file, tagging messages with agentID
// when non-empty (used for subagent transcripts).
func ingestFileWithAgent(database *sql.DB, sf ingest.SessionFile, agentID string, force bool) (int, int, error) {
	result, err := ingest.ParseFile(sf.Path)
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

	sessionSourceDir := decodeProjectDirFromSessionFile(sf)
	ensureSession(database, sf.SessionID, result, sessionSourceDir)
	msgCount, toolCount := storeParseResult(database, sf.SessionID, agentID, result)
	_ = dbpkg.UpdateTranscriptSync(database, sf.SessionID, sf.Path)

	if htmlgraphDir, err := findHtmlgraphDir(); err == nil {
		if rerr := hooks.RenderIngestedSessionHTML(htmlgraphDir, sf.SessionID, sessionSourceDir, result, force); rerr != nil {
			fmt.Fprintf(os.Stderr, "  warn: render HTML for %s: %v\n", truncate(sf.SessionID, 12), rerr)
		}
	}

	return msgCount, toolCount, nil
}

// ingestSubagents discovers and ingests subagent JSONL files for a parent session.
// Returns counts of ingested, skipped, and errored subagent files.
func ingestSubagents(database *sql.DB, parent ingest.SessionFile, force bool) (ingested, skipped, errCount int) {
	sessionDir := filepath.Dir(parent.Path)
	subFiles, err := ingest.DiscoverSubagents(sessionDir)
	if err != nil || len(subFiles) == 0 {
		return 0, 0, 0
	}

	for _, sf := range subFiles {
		if !force {
			count, _ := dbpkg.CountMessages(database, sf.SessionID)
			if count > 0 {
				skipped++
				continue
			}
		}
		n, toolN, err := ingestFileWithAgent(database, sf, sf.SessionID, force)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip subagent %s: %v\n", truncate(sf.SessionID, 12), err)
			errCount++
			continue
		}
		if n == 0 {
			skipped++
			continue
		}
		fmt.Printf("  subagent %-8s %3d msgs  %3d tools\n",
			truncate(sf.SessionID, 8), n, toolN)
		ingested++
	}
	return ingested, skipped, errCount
}

func storeParseResult(database *sql.DB, sessionID, agentID string, result *ingest.ParseResult) (int, int) {
	var msgCount, toolCount int

	// Map ordinal → message DB ID for linking tool calls.
	msgIDs := map[int]int64{}

	for _, m := range result.Messages {
		m.SessionID = sessionID
		m.AgentID = agentID
		id, err := dbpkg.InsertMessage(database, &m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    warn: msg ord %d: %v\n", m.Ordinal, err)
			continue
		}
		msgIDs[m.Ordinal] = id
		msgCount++
	}

	// Fetch the session's active_feature_id to tag each tool call and file tracking.
	activeFeatureID := sessionActiveFeature(database, sessionID)
	featureID := activeFeatureID

	for _, tc := range result.ToolCalls {
		tc.SessionID = sessionID
		if mid, ok := msgIDs[tc.MessageOrdinal]; ok {
			tc.MessageID = int(mid)
		}
		if activeFeatureID != "" {
			tc.FeatureID = activeFeatureID
		}
		// bug-cb4918d8: when this transcript belongs to a subagent (agentID
		// non-empty), stamp subagent_session_id so the column stops being
		// latent dead storage. Distinct from session_id, which is the shared
		// orchestrator UUID for all Task-dispatched subagents.
		if agentID != "" {
			tc.SubagentSessionID = agentID
		}
		if err := dbpkg.InsertToolCall(database, &tc); err != nil {
			fmt.Fprintf(os.Stderr, "    warn: tool %s: %v\n", tc.ToolName, err)
			continue
		}
		toolCount++

		// Record file-path touches in feature_files when feature is known.
		if featureID != "" {
			if op := ingestFileOp(tc.ToolName); op != "" {
				if fp := extractIngestFilePath(tc.InputJSON); fp != "" {
					ff := &models.FeatureFile{
						ID:        featureID + "-" + uuid.NewString(),
						FeatureID: featureID,
						FilePath:  fp,
						Operation: op,
						SessionID: sessionID,
					}
					_ = dbpkg.UpsertFeatureFile(database, ff)
				}
			}
		}
	}

	// Derive agent_events from tool_calls to populate the activity feed.
	// Uses UpsertEvent (INSERT OR REPLACE) so re-ingestion is idempotent.
	msgTimestamps := make(map[int]time.Time, len(result.Messages))
	for _, m := range result.Messages {
		msgTimestamps[m.Ordinal] = m.Timestamp
	}
	now := time.Now().UTC()
	resolvedAgent := agentID
	if resolvedAgent == "" {
		resolvedAgent = "claude-code"
	}
	for i, tc := range result.ToolCalls {
		evtID := ingest.EventID(sessionID, tc.ToolUseID, tc.ToolName, i)
		ts := now
		if t, ok := msgTimestamps[tc.MessageOrdinal]; ok {
			ts = t
		}

		// Skip if a hook-written event already exists for this (session, tool, ts).
		// Hooks produce canonical IDs; ingest-derived duplicates would create a
		// second row with a different event_id for the same logical event.
		tsStr := ts.UTC().Format(time.RFC3339)
		if exists, _ := dbpkg.HasHookEventAt(database, sessionID, tc.ToolName, tsStr); exists {
			continue
		}

		ev := &models.AgentEvent{
			EventID:      evtID,
			AgentID:      resolvedAgent,
			EventType:    models.EventToolCall,
			Timestamp:    ts,
			ToolName:     tc.ToolName,
			InputSummary: truncate(tc.InputJSON, 200),
			ToolInput:    tc.InputJSON,
			SessionID:    sessionID,
			FeatureID:    featureID,
			Status:       "completed",
			Source:       "ingest",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		_ = dbpkg.UpsertEvent(database, ev)
	}

	// Update session model if we detected one.
	if result.Model != "" {
		database.Exec(`UPDATE sessions SET model = ? WHERE session_id = ? AND (model IS NULL OR model = '')`,
			result.Model, sessionID)
	}

	// Update session title if the transcript carried one (ai-title or custom-title).
	// Placed here (not in ensureSession) so re-ingestion always fires the UPDATE,
	// even for existing sessions where ensureSession returns early.
	if result.Title != "" {
		database.Exec(
			`UPDATE sessions SET title = ? WHERE session_id = ? AND (title IS NULL OR title = '' OR title = '--' OR title <> ?)`,
			result.Title, sessionID, result.Title,
		)
	}

	return msgCount, toolCount
}

// ensureSession creates a session row if one doesn't already exist.
// This handles sessions discovered from JSONL that predate hook installation.
// projectDir is the filesystem path of the project that owns the transcript —
// it MUST be set to prevent bug-a52d5bf9 where empty project_dir rows polluted
// the sessions table and showed up across every project's dashboard.
func ensureSession(database *sql.DB, sessionID string, result *ingest.ParseResult, projectDir string) {
	var exists int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID).Scan(&exists)
	if exists > 0 {
		// Row already present — backfill project_dir if it's empty so the
		// display filter in sessionsHandler can scope correctly going forward.
		if projectDir != "" {
			database.Exec(
				`UPDATE sessions SET project_dir = ? WHERE session_id = ? AND (project_dir IS NULL OR project_dir = '')`,
				projectDir, sessionID,
			)
		}
		return
	}

	// Create a minimal session from transcript metadata.
	ts := ""
	if len(result.Messages) > 0 {
		ts = result.Messages[0].Timestamp.UTC().Format("2006-01-02T15:04:05Z")
	}

	database.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, created_at, status, model, project_dir)
		VALUES (?, 'claude-code', COALESCE(NULLIF(?, ''), CURRENT_TIMESTAMP), 'completed', ?, ?)`,
		sessionID, ts, nullStrVal(result.Model), projectDir,
	)
}

// decodeProjectDirFromSessionFile recovers the filesystem project directory
// from a SessionFile. Claude Code stores transcripts at
// ~/.claude/projects/<encoded-path>/<session-id>.jsonl where the encoded path
// replaces slashes with dashes. The Path field is the absolute path to the
// jsonl file; the parent directory name is the encoded project path.
func decodeProjectDirFromSessionFile(sf ingest.SessionFile) string {
	parent := filepath.Base(filepath.Dir(sf.Path))
	return decodeClaudeProjectPath(parent)
}

// decodeClaudeProjectPath reverses the dash-encoding Claude Code applies to
// filesystem paths when creating ~/.claude/projects/<encoded> directories.
// Each dash is replaced by a slash. If the encoding is ambiguous (e.g. a real
// dash in the path), the result may not round-trip exactly — callers should
// treat the result as a best-effort attribution hint.
func decodeClaudeProjectPath(encoded string) string {
	if encoded == "" {
		return ""
	}
	return "/" + strings.ReplaceAll(strings.TrimPrefix(encoded, "-"), "-", "/")
}

func nullStrVal(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// sessionActiveFeature returns the active_feature_id for a session, or "".
func sessionActiveFeature(database *sql.DB, sessionID string) string {
	var featureID string
	database.QueryRow(
		`SELECT COALESCE(active_feature_id, '') FROM sessions WHERE session_id = ?`,
		sessionID,
	).Scan(&featureID)
	return featureID
}

// ingestFileOp maps tool names to feature_files operation labels for ingest.
// Returns "" for tools that don't operate on a specific file path.
func ingestFileOp(toolName string) string {
	switch toolName {
	case "Read":
		return "read"
	case "Edit", "MultiEdit":
		return "edit"
	case "Write":
		return "write"
	case "Glob":
		return "glob"
	case "Grep":
		return "grep"
	}
	return ""
}

// extractIngestFilePath parses the input_json of a tool_call and returns the
// file path using the same key priority as the hook's extractFilePath helper.
func extractIngestFilePath(inputJSON string) string {
	if inputJSON == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inputJSON), &m); err != nil {
		return ""
	}
	for _, key := range []string{"file_path", "path", "file"} {
		if raw, ok := m[key]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && s != "" {
				return s
			}
		}
	}
	return ""
}

// ingestCommitsCmd returns a subcommand that bulk-imports git log history into
// git_commits. It uses "backfill" as the session_id sentinel since the original
// session that produced each commit is unknown.
func ingestCommitsCmd() *cobra.Command {
	var since string
	var limit int

	cmd := &cobra.Command{
		Use:   "commits",
		Short: "Bulk-import git commit history into git_commits table",
		Long: `Reads git log from the current repository and inserts commits into the
git_commits table. Each backfilled commit uses session_id="backfill".

Feature attribution is extracted from commit messages using two patterns:
  - Parenthetical: (feat-abc12345)
  - Closing keywords: completes/closes/fixes/resolves feat-abc12345`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runIngestCommits(since, limit)
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "only import commits after this date (e.g. 2024-01-01)")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of commits to import (0 = no limit)")

	return cmd
}

func runIngestCommits(since string, limit int) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	database, err := openDB(htmlgraphDir)
	if err != nil {
		return err
	}
	defer database.Close()

	repoDir := filepath.Dir(htmlgraphDir)

	inserted, attributed, err := ingestCommitsFromRepo(database, repoDir, since, limit)
	if err != nil {
		return err
	}

	fmt.Printf("Done: %d commits ingested (%d with feature attribution)\n", inserted, attributed)
	return nil
}

// ingestCommitsFromRepo reads git log from repoDir and inserts commits into
// the git_commits table. It returns the number of newly inserted rows and the
// subset that carry a work-item feature_id.
//
// Parameters:
//   - since: ISO date string for --after filter (empty = all history)
//   - limit: max commits to read (0 = no limit)
func ingestCommitsFromRepo(database *sql.DB, repoDir, since string, limit int) (inserted, attributed int, err error) {
	args := []string{"log", "--format=%H|%s|%aI"}
	if since != "" {
		args = append(args, "--after="+since)
	}
	if limit > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", limit))
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, gitErr := cmd.Output()
	if gitErr != nil {
		if len(out) == 0 {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("git log: %w", gitErr)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	now := time.Now().UTC()

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 2 {
			continue
		}
		hash := strings.TrimSpace(parts[0])
		msg := strings.TrimSpace(parts[1])
		ts := now
		if len(parts) == 3 {
			if parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(parts[2])); parseErr == nil {
				ts = parsed
			}
		}

		featureID := extractFeatureIDFromCommitMsg(msg)

		gitCommit := &models.GitCommit{
			CommitHash: hash,
			SessionID:  "backfill",
			FeatureID:  featureID,
			Message:    msg,
			Timestamp:  ts,
		}

		n, insertErr := dbpkg.InsertGitCommitResult(database, gitCommit)
		if insertErr != nil {
			fmt.Fprintf(os.Stderr, "warn: insert commit %s: %v\n", truncate(hash, 10), insertErr)
			continue
		}

		if n > 0 {
			inserted++
			if featureID != "" {
				attributed++
			}
		}
	}

	return inserted, attributed, nil
}

// ingestCommitClosingRe matches closing keywords followed by a work item ID.
var ingestCommitClosingRe = regexp.MustCompile(`(?:completes?|closes?|fix(?:es)?|resolves?)\s+((?:feat|bug|spk)-[0-9a-f]{8})`)

// ingestCommitParenRe matches parenthetical work item references, e.g. "(feat-abc12345)".
var ingestCommitParenRe = regexp.MustCompile(`\(\s*((?:feat|bug|spk)-[0-9a-f]{8})\s*\)`)

// extractFeatureIDFromCommitMsg returns the first work-item ID found in a
// commit message. Checks closing-keyword pattern first, then parenthetical.
// Returns "" when no ID is found. Matching is case-insensitive.
func extractFeatureIDFromCommitMsg(msg string) string {
	lower := strings.ToLower(msg)
	if m := ingestCommitClosingRe.FindStringSubmatch(lower); len(m) == 2 {
		return m[1]
	}
	if m := ingestCommitParenRe.FindStringSubmatch(lower); len(m) == 2 {
		return m[1]
	}
	return ""
}
