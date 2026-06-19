package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/ingest"
	"github.com/shakestzd/wipnote/core/models"
)

const maxInputSummaryLen = 200

// reindexSessions scans sessionDir for *.html session files and parses each
// <li> activity entry into an AgentEvent, upserting via db.UpsertEvent.
// Returns (totalFiles, totalEventsUpserted, errorFiles).
//
// projectDir is the absolute filesystem path of the project that owns
// sessionDir; it is used as the fallback project_dir attribution when the
// parsed HTML predates the data-project-dir attribute (bug-a52d5bf9).
func reindexSessions(database *sql.DB, sessionDir, projectDir string) (int, int, int) {
	pattern := filepath.Join(sessionDir, "*.html")
	files, _ := filepath.Glob(pattern)

	var total, upserted, errCount int
	for _, f := range files {
		total++
		n, err := parseSessionHTML(database, f, projectDir)
		if err != nil {
			errCount++
			continue
		}
		upserted += n
	}
	return total, upserted, errCount
}

// parseSessionHTML reads a single session HTML file, extracts the article
// metadata (session ID, agent, project_dir) and each <li> in the activity
// log, then upserts an AgentEvent per entry. Returns the number of events
// upserted.
//
// fallbackProjectDir is used when the HTML file predates the
// data-project-dir attribute. It should be the filesystem path of the
// project that owns the sessionDir being walked (typically the parent of
// .wipnote/). This prevents bug-a52d5bf9, where sessions were written
// without any project_dir and polluted every project's dashboard.
func parseSessionHTML(database *sql.DB, path, fallbackProjectDir string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return 0, fmt.Errorf("parse HTML %s: %w", path, err)
	}

	article := doc.Find("article[id]").First()
	if article.Length() == 0 {
		return 0, fmt.Errorf("no <article id=...> in %s", path)
	}

	sessionID, _ := article.Attr("id")
	if sessionID == "" {
		return 0, fmt.Errorf("empty article id in %s", path)
	}

	agentID := attrOrDefault(article, "data-agent", "unknown")
	statusAttr := attrOrDefault(article, "data-status", "completed")
	startedAt := parseSessionTimestamp(attrOrDefault(article, "data-started-at", ""))
	isSubagent := attrOrDefault(article, "data-is-subagent", "false") == "true"
	startCommit := attrOrDefault(article, "data-start-commit", "")
	activeFeatureID := attrOrDefault(article, "data-active-feature-id", "")

	// Prefer the article's data-project-dir (HTML canonical) and fall back
	// to the directory the reindex walker is pointing at. The fallback
	// keeps legacy HTML files (from before the attribute was written)
	// attributable instead of silently polluting with empty project_dir.
	projectDir := attrOrDefault(article, "data-project-dir", "")
	if projectDir == "" {
		projectDir = fallbackProjectDir
	}

	// Map session HTML statuses to sessions table CHECK constraint values.
	sessionStatus := "completed"
	switch statusAttr {
	case "active":
		sessionStatus = "active"
	case "stale", "ended":
		sessionStatus = "completed"
	case "failed":
		sessionStatus = "failed"
	}

	now := time.Now().UTC()
	if startedAt.IsZero() {
		startedAt = now
	}

	// Ensure a session row exists (FK constraint on agent_events.session_id).
	_, _ = database.Exec(`
		INSERT OR IGNORE INTO sessions (session_id, agent_assigned, created_at, status, start_commit, is_subagent, project_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, agentID, startedAt.UTC().Format(time.RFC3339),
		sessionStatus, startCommit, isSubagent, projectDir,
	)

	// Backfill project_dir on existing rows that predate this fix
	// (bug-a52d5bf9). Only updates rows where project_dir is empty so we
	// never clobber a more-specific attribution written elsewhere.
	if projectDir != "" {
		_, _ = database.Exec(
			`UPDATE sessions SET project_dir = ? WHERE session_id = ? AND (project_dir IS NULL OR project_dir = '')`,
			projectDir, sessionID,
		)
	}
	if activeFeatureID != "" {
		_, _ = database.Exec(
			`UPDATE sessions SET active_feature_id = ?, updated_at = ? WHERE session_id = ? AND (active_feature_id IS NULL OR active_feature_id = '')`,
			activeFeatureID, now.UTC().Format(time.RFC3339), sessionID,
		)
	}

	var count int

	article.Find("section[data-activity-log] ol li").Each(func(_ int, li *goquery.Selection) {
		eventID := attrOrDefault(li, "data-event-id", "")
		if eventID == "" {
			return // skip entries without an event ID
		}

		tsStr := attrOrDefault(li, "data-ts", "")
		ts := parseSessionTimestamp(tsStr)

		toolName := attrOrDefault(li, "data-tool", "")
		featureID := attrOrDefault(li, "data-feature", "")
		parentEventID := attrOrDefault(li, "data-parent", "")

		successStr := attrOrDefault(li, "data-success", "true")
		status := "completed"
		if successStr == "false" {
			status = "failed"
		}
		// An explicit data-status attribute on the <li> (e.g. "aborted" from
		// the orphan sweep) takes precedence over the success→status mapping
		// so round-trip reindex rebuilds aborted events as aborted.
		if explicit := attrOrDefault(li, "data-status", ""); explicit != "" {
			status = explicit
		}

		summary := strings.TrimSpace(li.Text())
		if len([]rune(summary)) > maxInputSummaryLen {
			summary = string([]rune(summary)[:maxInputSummaryLen-1]) + "\u2026"
		}
		if toolName == "UserQuery" && ingest.IsSystemMessage(summary) {
			return
		}

		evt := &models.AgentEvent{
			EventID:       eventID,
			AgentID:       agentID,
			EventType:     models.EventToolCall,
			Timestamp:     ts,
			ToolName:      toolName,
			InputSummary:  summary,
			SessionID:     sessionID,
			FeatureID:     featureID,
			ParentEventID: parentEventID,
			Status:        status,
			Source:        "reindex",
			CreatedAt:     now,
			UpdatedAt:     now,
		}

		if upsertErr := dbpkg.UpsertEvent(database, evt); upsertErr != nil {
			_ = upsertErr // skip on upsert failure
			return
		}
		count++
	})

	return count, nil
}

// attrOrDefault returns the named attribute value from a goquery selection,
// or the fallback if the attribute is absent or empty.
func attrOrDefault(sel *goquery.Selection, name, fallback string) string {
	if v, ok := sel.Attr(name); ok && v != "" {
		return v
	}
	return fallback
}

// parseSessionTimestamp parses ISO 8601 timestamps found in session HTML.
// Tries multiple layouts to handle both timezone-naive and timezone-aware values.
func parseSessionTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
