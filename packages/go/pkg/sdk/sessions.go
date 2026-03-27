package sdk

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/models"
)

// SessionCollection provides read operations for sessions.
// Sessions are primarily created by hooks, not by the SDK directly.
type SessionCollection struct {
	*Collection
}

// NewSessionCollection creates a SessionCollection bound to the SDK.
func NewSessionCollection(s *SDK) *SessionCollection {
	return &SessionCollection{Collection: newCollection(s, "sessions", "session")}
}

// GetLatest returns the N most recent sessions.
func (sc *SessionCollection) GetLatest(limit int) ([]*models.Node, error) {
	if limit <= 0 {
		limit = 1
	}

	nodes, err := sc.List()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].CreatedAt.After(nodes[j].CreatedAt)
	})

	if len(nodes) > limit {
		nodes = nodes[:limit]
	}
	return nodes, nil
}

// Handoff marks a session as handed off with continuity notes.
// It injects data-handoff-notes and data-handoff-at attributes into the
// session HTML file. These are used when resuming work between agents.
func (sc *SessionCollection) Handoff(sessionID string, notes string) error {
	if sessionID == "" {
		return fmt.Errorf("sessionID must not be empty")
	}
	path, err := sc.sessionHTMLPath(sessionID)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read session HTML %s: %w", sessionID, err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	updated := injectHandoffAttrs(string(content), notes, now)

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write session HTML %s: %w", sessionID, err)
	}
	return nil
}

// GetHandoffNotes retrieves handoff notes from a session HTML file.
// Returns an empty string (no error) if no handoff notes are present.
func (sc *SessionCollection) GetHandoffNotes(sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("sessionID must not be empty")
	}
	path, err := sc.sessionHTMLPath(sessionID)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read session HTML %s: %w", sessionID, err)
	}

	return extractDataAttr(string(content), "data-handoff-notes"), nil
}

// sessionHTMLPath resolves the HTML file path for a session ID.
// Tries both <id>.html and sess-<id>.html naming conventions.
func (sc *SessionCollection) sessionHTMLPath(sessionID string) (string, error) {
	dir := filepath.Join(sc.base.ProjectDir, "sessions")
	direct := filepath.Join(dir, sessionID+".html")
	if _, err := os.Stat(direct); err == nil {
		return direct, nil
	}
	return "", fmt.Errorf("session HTML not found for %s in %s", sessionID, dir)
}

// injectHandoffAttrs adds or replaces data-handoff-notes and data-handoff-at
// attributes on the <article> element in session HTML.
func injectHandoffAttrs(html, notes, timestamp string) string {
	// Remove any existing handoff attributes to avoid duplication.
	html = removeDataAttr(html, "data-handoff-notes")
	html = removeDataAttr(html, "data-handoff-at")

	escaped := strings.ReplaceAll(notes, `"`, `&quot;`)
	inject := fmt.Sprintf(
		` data-handoff-notes="%s" data-handoff-at="%s"`,
		escaped, timestamp,
	)

	// Append new attributes before the closing `>` of the <article> opening tag.
	re := regexp.MustCompile(`(<article[^>]*)>`)
	return re.ReplaceAllStringFunc(html, func(m string) string {
		return m[:len(m)-1] + inject + ">"
	})
}

// removeDataAttr strips a single data-* attribute (with its value) from HTML.
func removeDataAttr(html, attr string) string {
	re := regexp.MustCompile(` ` + regexp.QuoteMeta(attr) + `="[^"]*"`)
	return re.ReplaceAllString(html, "")
}

// extractDataAttr reads the value of a data-* attribute from HTML.
// Returns "" if the attribute is absent.
func extractDataAttr(html, attr string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(attr) + `="([^"]*)"`)
	m := re.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	return strings.ReplaceAll(m[1], `&quot;`, `"`)
}
