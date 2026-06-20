package hooks

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/shakestzd/wipnote/core/ingest"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/provenance"
)

// SessionEvent holds the data needed to write a <li> element to a session's
// HTML activity log. Kept minimal — only fields that appear in the HTML output.
// Status and Reason are optional and emit data-status/data-reason attributes
// when set; they are used by the orphan sweep to write synthetic aborted
// entries while leaving the live PostToolUse append path unchanged.
type SessionEvent struct {
	Timestamp time.Time
	ToolName  string
	Success   bool
	EventID   string
	FeatureID string
	Summary   string
	Status    string
	Reason    string
}

// CreateSessionHTML writes the initial session HTML file to
// .wipnote/sessions/{session-id}.html. It creates the sessions directory if
// needed. Errors are silently logged via debugLog — HTML is non-critical.
func CreateSessionHTML(projectDir string, s *models.Session) {
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		debugLog(projectDir, "[session-html] mkdir sessions: %v", err)
		return
	}

	// data-project-dir is the canonical attribution for the session and
	// must reflect the TRANSCRIPT'S source project, not the destination
	// repo that the HTML is being written into. RenderIngestedSessionHTML
	// sets s.ProjectDir to the decoded source path before calling here,
	// so prefer that over the function argument when set. The function
	// argument is only used as a fallback for the live hook path where
	// projectDir IS the source project. (bug found in roborev review of
	// d2731bec — without this guard, ingested sessions would canonicalize
	// the wrong project_dir and reindex would propagate it forward.)
	//
	// Normalize to canonical root for local sessions (e.g. "."); mark with
	// "unresolved:" prefix for sessions ingested from foreign machines where
	// the canonical root differs from the local repo, preserving the foreign
	// origin so it remains queryable without silently mangling the path.
	raw := s.ProjectDir
	if raw == "" {
		raw = projectDir
	}
	canonicalProjectDir := paths.NormalizeProjectDir(raw)

	isSubagent := "false"
	if s.IsSubagent {
		isSubagent = "true"
	}

	startedAt := s.CreatedAt.UTC().Format(time.RFC3339)

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="wipnote-version" content="1.0">
    <title>Session `)
	b.WriteString(html.EscapeString(startedAt))
	b.WriteString(`</title>
    <link rel="stylesheet" href="../styles.css">
</head>
<body>
    <article id="`)
	b.WriteString(html.EscapeString(s.SessionID))
	b.WriteString(`"
             data-type="session"
             data-status="active"
             data-agent="`)
	b.WriteString(html.EscapeString(s.AgentAssigned))
	b.WriteString(`"
             data-project-dir="`)
	b.WriteString(html.EscapeString(canonicalProjectDir))
	b.WriteString(`"
             data-started-at="`)
	b.WriteString(html.EscapeString(startedAt))
	b.WriteString(`"
             data-event-count="0"
             data-is-subagent="`)
	b.WriteString(isSubagent)
	b.WriteString(`"
             data-start-commit="`)
	b.WriteString(html.EscapeString(s.StartCommit))
	b.WriteString(`"`)
	if s.ActiveFeatureID != "" {
		b.WriteString(`
             data-active-feature-id="`)
		b.WriteString(html.EscapeString(s.ActiveFeatureID))
		b.WriteString(`"`)
	}
	// Provenance attributes — emitted only when populated so older sessions
	// continue to round-trip with the same byte shape.
	prov := provenance.Provenance{
		Agent:      s.CreatedByAgent,
		Model:      s.CreatedByModel,
		Role:       s.CreatedByRole,
		CLIVersion: s.CreatedByCLIVersion,
	}
	b.WriteString(prov.HTMLAttrs())
	b.WriteString(`>

        <header>
            <h1>Session `)
	b.WriteString(html.EscapeString(startedAt))
	b.WriteString(`</h1>
            <div class="metadata">
                <span class="badge status-active">Active</span>
                <span class="badge">`)
	b.WriteString(html.EscapeString(s.AgentAssigned))
	b.WriteString(`</span>
                <span class="badge">0 events</span>
            </div>
        </header>

        <nav data-graph-edges>
        </nav>

        <section data-activity-log>
            <ol reversed>
            </ol>
        </section>
    </article>
</body>
</html>
`)

	htmlPath := filepath.Join(sessDir, s.SessionID+".html")

	// Guard: do not clobber an existing session HTML that is already
	// populated or belongs to a different session. Only write when:
	//   (a) the file does not yet exist, or
	//   (b) the file exists, encodes the SAME session ID, AND is an empty
	//       shell (data-event-count=0 and not completed/aborted) — i.e. a
	//       safe same-session re-initialisation before any events have been
	//       recorded.
	// Cases that MUST NOT overwrite:
	//   • different session ID on disk (UUID collision guard — unchanged)
	//   • same session ID but already has recorded events (data-event-count>0)
	//   • same session ID but already finalized (status != "active")
	// This fixes the SessionStart REPLAY / resume / re-init data-loss bug
	// (roborev job 272, commit 2d54416ed): the previous guard only checked
	// for a different id, so a same-session replay could reset a 732-event
	// session back to an empty shell.
	if existing, readErr := os.ReadFile(htmlPath); readErr == nil {
		existingContent := string(existing)
		existingID := extractArticleID(existingContent)
		if existingID != "" && existingID != s.SessionID {
			// Different session id on disk — preserve it (UUID collision guard).
			debugLog(projectDir,
				"[session-html] skipping write: %s already belongs to session %q (new=%q)",
				htmlPath, existingID, s.SessionID)
			return
		}
		// Same session id (or no id found): only overwrite when the shell is
		// empty — i.e. no events recorded and not finalized.
		//
		// IMPORTANT: data-event-count on the <article> tag is only updated by
		// FinalizeSessionHTML. During a live session, AppendEventToSessionHTML
		// inserts <li> elements but leaves data-event-count="0". So we must
		// detect population by counting actual <li> activity entries in the
		// section, not by reading data-event-count alone.
		if extractEventCount(existingContent) > 0 || countActivityEntries(existingContent) > 0 {
			debugLog(projectDir,
				"[session-html] skipping write: %s has recorded events (same session %q)",
				htmlPath, s.SessionID)
			return
		}
		existingStatus := extractArticleStatus(existingContent)
		if existingStatus != "" && existingStatus != "active" {
			debugLog(projectDir,
				"[session-html] skipping write: %s is already %q (same session %q)",
				htmlPath, existingStatus, s.SessionID)
			return
		}
	}

	if err := os.WriteFile(htmlPath, []byte(b.String()), 0o644); err != nil {
		debugLog(projectDir, "[session-html] write %s: %v", htmlPath, err)
	}
}

// SetSessionHTMLActiveFeature persists the session's current root work-item
// attribution into the canonical session HTML article. SQLite is derived, so
// this lets reindex/rebuild faithfully restore propagated family attribution.
func SetSessionHTMLActiveFeature(projectDir, sessionID, featureID string) error {
	if projectDir == "" || sessionID == "" || featureID == "" {
		return nil
	}
	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID+".html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	content := removeArticleDataAttr(string(data), "data-active-feature-id")
	escaped := html.EscapeString(featureID)
	re := regexp.MustCompile(`(<article\b[^>]*)>`)
	updated := re.ReplaceAllStringFunc(content, func(match string) string {
		return strings.TrimSuffix(match, ">") + ` data-active-feature-id="` + escaped + `">`
	})
	if updated == content {
		return nil
	}
	return os.WriteFile(htmlPath, []byte(updated), 0o644)
}

func removeArticleDataAttr(htmlText, attr string) string {
	re := regexp.MustCompile(`\s+` + regexp.QuoteMeta(attr) + `="[^"]*"`)
	return re.ReplaceAllString(htmlText, "")
}

// AppendEventToSessionHTML appends a <li> element to the session's HTML
// activity log. It opens the file with an exclusive flock, reads, modifies,
// and rewrites — preventing lost updates from concurrent hook invocations.
// Errors are silently logged (non-critical path).
func AppendEventToSessionHTML(projectDir, sessionID string, ev SessionEvent) {
	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID+".html")

	f, err := os.OpenFile(htmlPath, os.O_RDWR, 0o644)
	if err != nil {
		// File doesn't exist — silently ignore (non-critical).
		return
	}
	defer f.Close()

	// Exclusive lock — blocks until no other process holds the lock.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		debugLog(projectDir, "[session-html] flock %s: %v", htmlPath, err)
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := io.ReadAll(f)
	if err != nil {
		debugLog(projectDir, "[session-html] read %s: %v", htmlPath, err)
		return
	}

	content := string(data)
	marker := "</ol>"
	idx := strings.LastIndex(content, marker)
	if idx == -1 {
		debugLog(projectDir, "[session-html] no </ol> marker in %s", htmlPath)
		return
	}

	successStr := "true"
	if !ev.Success {
		successStr = "false"
	}

	var li strings.Builder
	li.WriteString(`                <li data-ts="`)
	li.WriteString(ev.Timestamp.UTC().Format(time.RFC3339))
	li.WriteString(`" data-tool="`)
	li.WriteString(html.EscapeString(ev.ToolName))
	li.WriteString(`" data-success="`)
	li.WriteString(successStr)
	li.WriteString(`" data-event-id="`)
	li.WriteString(html.EscapeString(ev.EventID))
	li.WriteString(`"`)
	if ev.FeatureID != "" {
		li.WriteString(` data-feature="`)
		li.WriteString(html.EscapeString(ev.FeatureID))
		li.WriteString(`"`)
	}
	if ev.Status != "" {
		li.WriteString(` data-status="`)
		li.WriteString(html.EscapeString(ev.Status))
		li.WriteString(`"`)
	}
	if ev.Reason != "" {
		li.WriteString(` data-reason="`)
		li.WriteString(html.EscapeString(ev.Reason))
		li.WriteString(`"`)
	}
	li.WriteString(`>`)
	li.WriteString(html.EscapeString(ev.Summary))
	li.WriteString("</li>\n")

	// Insert the <li> just before </ol>.
	newContent := content[:idx] + li.String() + "            " + content[idx:]

	// Truncate and rewrite in place (we already hold the lock).
	if err := f.Truncate(0); err != nil {
		debugLog(projectDir, "[session-html] truncate %s: %v", htmlPath, err)
		return
	}
	if _, err := f.Seek(0, 0); err != nil {
		debugLog(projectDir, "[session-html] seek %s: %v", htmlPath, err)
		return
	}
	if _, err := f.Write([]byte(newContent)); err != nil {
		debugLog(projectDir, "[session-html] write %s: %v", htmlPath, err)
	}
}

// extractArticleID extracts the id="..." value from the first <article id="...">
// element in an HTML string. Returns "" when not found. Used by CreateSessionHTML
// to detect UUID collisions before overwriting an existing session file.
func extractArticleID(content string) string {
	const needle = `<article id="`
	idx := strings.Index(content, needle)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(needle):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// extractEventCount returns the integer value of data-event-count="..." from
// the session HTML content. Returns 0 when the attribute is absent or cannot
// be parsed — which is the safe default (treat as empty shell).
func extractEventCount(content string) int {
	const needle = `data-event-count="`
	idx := strings.Index(content, needle)
	if idx < 0 {
		return 0
	}
	rest := content[idx+len(needle):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return 0
	}
	n := 0
	for _, ch := range rest[:end] {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// extractArticleStatus returns the value of data-status="..." from the session
// HTML content. Returns "" when the attribute is absent.
func extractArticleStatus(content string) string {
	const needle = `data-status="`
	idx := strings.Index(content, needle)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(needle):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// countActivityEntries counts the number of <li …> elements inside
// <section data-activity-log> in the session HTML content. This is the
// canonical way to detect whether a live (non-finalized) session has recorded
// events: AppendEventToSessionHTML writes <li> entries but does NOT update
// data-event-count (only FinalizeSessionHTML does that). Returns 0 when the
// section is absent or contains no entries.
func countActivityEntries(content string) int {
	const sectionMarker = `<section data-activity-log>`
	sIdx := strings.Index(content, sectionMarker)
	if sIdx < 0 {
		return 0
	}
	section := content[sIdx:]
	return strings.Count(section, "<li ")
}

// articleAttrRe matches data attributes on the <article> tag for replacement.
var articleStatusRe = regexp.MustCompile(`data-status="[^"]*"`)
var articleEventCountRe = regexp.MustCompile(`data-event-count="[^"]*"`)
var badgeStatusRe = regexp.MustCompile(`<span class="badge status-[^"]*">[^<]*</span>`)
var badgeEventsRe = regexp.MustCompile(`<span class="badge">\d+ events?</span>`)

// FinalizeSessionHTML updates the session HTML file with completion data:
// sets data-status, adds data-ended-at, and updates data-event-count.
// Errors are silently logged. Acquires the same exclusive flock as
// AppendEventToSessionHTML so it does not race a concurrent sweep append
// or a late PostToolUse write during SessionEnd.
func FinalizeSessionHTML(projectDir, sessionID, endedAt, status string, eventCount int) {
	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID+".html")
	f, err := os.OpenFile(htmlPath, os.O_RDWR, 0o644)
	if err != nil {
		// File doesn't exist — silently ignore.
		return
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		debugLog(projectDir, "[session-html] finalize flock %s: %v", htmlPath, err)
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := io.ReadAll(f)
	if err != nil {
		debugLog(projectDir, "[session-html] finalize read %s: %v", htmlPath, err)
		return
	}

	content := string(data)

	// Update data-status.
	content = articleStatusRe.ReplaceAllString(content,
		fmt.Sprintf(`data-status="%s"`, html.EscapeString(status)))

	// Add data-ended-at after data-status on the article tag.
	// Find the article's data-status and insert data-ended-at after it.
	endedAtAttr := fmt.Sprintf(`data-ended-at="%s"`, html.EscapeString(endedAt))
	if !strings.Contains(content, "data-ended-at=") {
		// Insert after data-status in the article tag.
		statusAttr := fmt.Sprintf(`data-status="%s"`, html.EscapeString(status))
		content = strings.Replace(content, statusAttr,
			statusAttr+"\n             "+endedAtAttr, 1)
	}

	// Update data-event-count.
	content = articleEventCountRe.ReplaceAllString(content,
		fmt.Sprintf(`data-event-count="%d"`, eventCount))

	// Update badge status text.
	statusTitle := strings.ToUpper(status[:1]) + status[1:]
	content = badgeStatusRe.ReplaceAllString(content,
		fmt.Sprintf(`<span class="badge status-%s">%s</span>`,
			html.EscapeString(status), html.EscapeString(statusTitle)))

	// Update badge event count text.
	evtWord := "events"
	if eventCount == 1 {
		evtWord = "event"
	}
	content = badgeEventsRe.ReplaceAllString(content,
		fmt.Sprintf(`<span class="badge">%d %s</span>`, eventCount, evtWord))

	if err := f.Truncate(0); err != nil {
		debugLog(projectDir, "[session-html] finalize truncate %s: %v", htmlPath, err)
		return
	}
	if _, err := f.Seek(0, 0); err != nil {
		debugLog(projectDir, "[session-html] finalize seek %s: %v", htmlPath, err)
		return
	}
	if _, err := f.Write([]byte(content)); err != nil {
		debugLog(projectDir, "[session-html] finalize write %s: %v", htmlPath, err)
	}
}

// RenderIngestedSessionHTML writes a complete session HTML file for an
// ingested Claude Code transcript. It reuses CreateSessionHTML to scaffold
// the file and AppendEventToSessionHTML (via the flock path) for each
// tool call, so the resulting file is byte-for-byte compatible with a
// live-hook-written session and round-trips through reindexSessions.
//
// wipnoteDir is the .wipnote directory of the project receiving the
// ingest. sessionSourceDir is the decoded Claude-projects project directory
// the session originated from and is recorded on the session model for
// display scoping. The render is a no-op when the target HTML file already
// exists unless force is true — live hook writes are authoritative and
// ingest must not clobber them.
func RenderIngestedSessionHTML(wipnoteDir, sessionID, sessionSourceDir string, result *ingest.ParseResult, force bool) error {
	parentDir := filepath.Dir(wipnoteDir)
	htmlPath := filepath.Join(wipnoteDir, "sessions", sessionID+".html")
	if !force {
		if _, err := os.Stat(htmlPath); err == nil {
			return nil
		}
	} else if _, err := os.Stat(htmlPath); err == nil {
		if rmErr := os.Remove(htmlPath); rmErr != nil {
			return fmt.Errorf("remove existing %s: %w", htmlPath, rmErr)
		}
	}

	createdAt := time.Now().UTC()
	if len(result.Messages) > 0 {
		createdAt = result.Messages[0].Timestamp.UTC()
	}

	s := &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		CreatedAt:     createdAt,
		Status:        "active",
		Model:         result.Model,
		ProjectDir:    sessionSourceDir,
	}
	CreateSessionHTML(parentDir, s)

	if _, err := os.Stat(htmlPath); err != nil {
		return fmt.Errorf("CreateSessionHTML did not produce %s: %w", htmlPath, err)
	}

	msgTimestamps := make(map[int]time.Time, len(result.Messages))
	for _, m := range result.Messages {
		msgTimestamps[m.Ordinal] = m.Timestamp
	}

	endedAt := createdAt
	for i, tc := range result.ToolCalls {
		ts := createdAt
		if t, ok := msgTimestamps[tc.MessageOrdinal]; ok {
			ts = t.UTC()
		}
		if ts.After(endedAt) {
			endedAt = ts
		}

		var inputMap map[string]any
		if tc.InputJSON != "" {
			_ = json.Unmarshal([]byte(tc.InputJSON), &inputMap)
		}

		AppendEventToSessionHTML(parentDir, sessionID, SessionEvent{
			Timestamp: ts,
			ToolName:  tc.ToolName,
			Success:   true,
			EventID:   ingest.EventID(sessionID, tc.ToolUseID, tc.ToolName, i),
			FeatureID: tc.FeatureID,
			Summary:   SummariseInput(tc.ToolName, inputMap),
		})
	}

	FinalizeSessionHTML(parentDir, sessionID,
		endedAt.Format(time.RFC3339), "completed", len(result.ToolCalls))

	return nil
}
