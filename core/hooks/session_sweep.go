package hooks

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/shakestzd/wipnote/core/db"
)

// OrphanThreshold is the minimum age a tool-call row must reach before the
// sweep considers it abandoned. Tool calls complete in under a second in the
// common case; 5 minutes is long enough to never false-positive on genuinely
// slow tools (WebFetch, long Bash) and short enough that crash recovery is
// visible within the same working session.
const OrphanThreshold = 5 * time.Minute

// OrphanHardCutoff is the age beyond which the sweep still processes the
// orphan but logs a warning — useful for users who want visibility into very
// old holes in their session history.
const OrphanHardCutoff = 24 * time.Hour

// SweepOrphanedEventsForSession runs the orphan sweep scoped to a single
// session HTML file. Returns the number of synthetic aborted entries that
// were newly appended. Safe to call from any hook path — uses the same flock
// acquisition as AppendEventToSessionHTML so concurrent live writes and
// concurrent sweeps from other processes do not corrupt the file.
func SweepOrphanedEventsForSession(database *sql.DB, projectDir, sessionID string) int {
	orphans, err := db.FindOrphanedEvents(database, sessionID, OrphanThreshold)
	if err != nil {
		debugLog(projectDir, "[sweep] find orphans for %s: %v", sessionID, err)
		return 0
	}
	return sweepOrphans(database, projectDir, orphans)
}

// SweepOrphanedEventsForProject runs the orphan sweep across every session
// in the project, with NO cap on the number of orphans processed. Intended for
// out-of-band drains (the serve-side periodic loop) where wall-clock latency is
// not user-visible. The latency-sensitive session-start hook path MUST use
// SweepOrphanedEventsForProjectCapped instead.
func SweepOrphanedEventsForProject(database *sql.DB, projectDir string) int {
	orphans, err := db.FindStaleProjectOrphans(database, OrphanThreshold, OrphanHardCutoff, 0)
	if err != nil {
		debugLog(projectDir, "[sweep] find orphans for project: %v", err)
		return 0
	}
	return sweepOrphans(database, projectDir, orphans)
}

// SessionStartSweepCap bounds how many project-wide orphans the synchronous
// session-start hook sweep will process. Each orphan costs a goquery parse +
// flock append + SQL UPDATE, so an uncapped sweep over a large crash backlog
// could block the hook (and thus the launcher) for multiple seconds
// (bug-504095f2). The active session's own orphans are already swept
// incrementally by PostToolUse; this project-wide sweep only cleans OTHER
// (crashed) sessions, so draining a small batch per launch is sufficient — the
// serve-side periodic drain clears any remaining backlog out-of-band.
//
// Sized for the hot path: measured ~200ms per orphan (HTML parse + flock), so a
// cap of 8 bounds the synchronous worst case to ~1.5s even on a cold launch with
// a full crash backlog, while the serve-side drain clears the remainder.
const SessionStartSweepCap = 8

// SweepOrphanedEventsForProjectCapped is the bounded counterpart to
// SweepOrphanedEventsForProject: it processes at most cap orphans (oldest
// first). It returns the number of synthetic aborted entries appended and the
// number of orphans discovered under the cap — when discovered == cap a backlog
// likely remains, which the caller can surface for observability. cap <= 0 is
// treated as unlimited.
func SweepOrphanedEventsForProjectCapped(database *sql.DB, projectDir string, cap int) (appended, discovered int) {
	orphans, err := db.FindStaleProjectOrphans(database, OrphanThreshold, OrphanHardCutoff, cap)
	if err != nil {
		debugLog(projectDir, "[sweep] find orphans for project (capped): %v", err)
		return 0, 0
	}
	return sweepOrphans(database, projectDir, orphans), len(orphans)
}

// sweepOrphans is the shared implementation used by both sweep entry points.
// For each orphan it:
//  1. checks whether a <li data-event-id="..."> already exists in the session
//     HTML file (dedup — the sweep is idempotent on the HTML side),
//  2. appends a synthetic aborted <li> via the existing flock path when the
//     entry is missing,
//  3. transitions the agent_events row to status='aborted', reason='swept'.
//
// Returns the number of newly appended synthetic entries.
func sweepOrphans(database *sql.DB, projectDir string, orphans []db.OrphanEvent) int {
	if len(orphans) == 0 {
		return 0
	}

	var appended int
	for _, o := range orphans {
		if time.Since(o.CreatedAt) > OrphanHardCutoff {
			debugLog(projectDir, "[sweep] orphan %s is older than 24h — sweeping anyway",
				o.EventID)
		}

		// Atomically transition the row from started→aborted. Only the
		// winner of the SQL update proceeds to append, so concurrent sweep
		// goroutines can't double-post the same synthetic entry. The dedup
		// check via goquery is a second line of defense for the case where
		// a crashed earlier sweep already wrote the <li> but never got to
		// the SQLite update.
		rows, err := db.MarkEventAborted(database, o.EventID, "swept")
		if err != nil {
			debugLog(projectDir, "[sweep] mark %s aborted: %v", o.EventID, err)
			continue
		}
		if rows == 0 {
			// Another concurrent sweep already handled this orphan.
			continue
		}

		htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", o.SessionID+".html")
		alreadyPresent, dedupErr := sessionHTMLHasEvent(htmlPath, o.EventID)
		if dedupErr != nil {
			debugLog(projectDir, "[sweep] dedup read %s: %v", htmlPath, dedupErr)
		}
		if alreadyPresent {
			continue
		}

		AppendEventToSessionHTML(projectDir, o.SessionID, SessionEvent{
			Timestamp: o.CreatedAt,
			ToolName:  o.ToolName,
			Success:   false,
			EventID:   o.EventID,
			FeatureID: o.FeatureID,
			Summary:   "[aborted] " + o.ToolName + " never completed",
			Status:    "aborted",
			Reason:    "no-post-hook",
		})
		appended++
	}
	return appended
}

// sessionHTMLHasEvent returns true if the session HTML file already contains
// a <li> with the given data-event-id. Missing files return false, nil —
// the append path will no-op on a missing file and the dedup check should
// not distinguish between "missing file" and "missing event".
func sessionHTMLHasEvent(htmlPath, eventID string) (bool, error) {
	f, err := os.Open(htmlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", htmlPath, err)
	}

	found := false
	doc.Find("li[data-event-id]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if v, ok := s.Attr("data-event-id"); ok && strings.EqualFold(v, eventID) {
			found = true
			return false
		}
		return true
	})
	return found, nil
}
