package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/shakestzd/wipnote/core/ingest"
)

const (
	aiTitleBackfillMarker = "migrations/ai-title-backfill.done"
	aiTitleWorkerCount    = 4
)

// startAITitleBackfill launches the one-time ai-title backfill in the background.
// It is non-blocking: it spawns a goroutine and returns immediately.
// The backfill is gated by a sentinel file at .wipnote/migrations/ai-title-backfill.done.
func startAITitleBackfill(_ context.Context, database *sql.DB, wipnoteDir string) {
	go func() {
		if err := runAITitleBackfill(database, wipnoteDir, false); err != nil {
			log.Printf("ai-title backfill: %v\n", err)
		}
	}()
}

// runAITitleBackfill enumerates sessions with legacy/empty titles, re-parses
// their JSONL files for the latest ai-title event, and UPDATEs sessions.title
// where an ai-title is present and differs from the current value.
//
// forceRun=true bypasses the sentinel-file gate, used in tests and for
// resumable re-runs after an interrupted backfill.
func runAITitleBackfill(database *sql.DB, wipnoteDir string, forceRun bool) error {
	markerPath := filepath.Join(wipnoteDir, aiTitleBackfillMarker)

	if !forceRun {
		if _, err := os.Stat(markerPath); err == nil {
			// Marker exists — already done.
			return nil
		}
	}

	type sessionRow struct {
		sessionID      string
		transcriptPath string
	}

	rows, err := database.Query(`
		SELECT session_id, COALESCE(transcript_path, '')
		FROM sessions
		WHERE title IS NULL
		   OR title = ''
		   OR title = '--'
		   OR title LIKE '[wipnote-titler]%'`)
	if err != nil {
		return err
	}

	var sessions []sessionRow
	for rows.Next() {
		var sr sessionRow
		if scanErr := rows.Scan(&sr.sessionID, &sr.transcriptPath); scanErr != nil {
			rows.Close()
			return scanErr
		}
		sessions = append(sessions, sr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if len(sessions) == 0 {
		return writeBackfillMarker(markerPath)
	}

	// titleUpdate holds the resolved ai-title for a session.
	type titleUpdate struct {
		sessionID string
		title     string
	}

	work := make(chan sessionRow, len(sessions))
	for _, s := range sessions {
		work <- s
	}
	close(work)

	// Workers parse JSONL files in parallel (file I/O bound).
	updates := make(chan titleUpdate, len(sessions))
	var wg sync.WaitGroup
	for range aiTitleWorkerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sr := range work {
				title := parseBackfillTitle(sr.sessionID, sr.transcriptPath)
				if title != "" {
					updates <- titleUpdate{sessionID: sr.sessionID, title: title}
				}
			}
		}()
	}

	// Close updates channel once all workers are done.
	go func() {
		wg.Wait()
		close(updates)
	}()

	// Single goroutine applies DB writes to avoid concurrent write contention.
	// Track write failures so we can skip the `.done` marker on any error —
	// otherwise transient DB failures would permanently silence the retry path
	// for the affected sessions.
	var writeErrs int
	for u := range updates {
		if _, err := database.Exec(
			`UPDATE sessions SET title = ? WHERE session_id = ?
			  AND (title IS NULL OR title = '' OR title = '--' OR title LIKE '[wipnote-titler]%')
			  AND COALESCE(title, '') <> ?`,
			u.title, u.sessionID, u.title,
		); err != nil {
			writeErrs++
			log.Printf("ai-title backfill: update failed for %s: %v\n", truncate(u.sessionID, 14), err)
		}
	}

	if writeErrs > 0 {
		return fmt.Errorf("ai-title backfill: %d update(s) failed; marker not written to allow retry", writeErrs)
	}
	return writeBackfillMarker(markerPath)
}

// parseBackfillTitle stats and parses a JSONL file, returning the ai-title
// (or "" if absent, missing, or unreadable). Logging is done here so the
// caller (worker goroutine) stays concise.
func parseBackfillTitle(sessionID, transcriptPath string) string {
	if transcriptPath == "" {
		log.Printf("ai-title backfill: skip %s — no transcript_path\n", truncate(sessionID, 14))
		return ""
	}

	if _, err := os.Stat(transcriptPath); err != nil {
		log.Printf("ai-title backfill: skip %s — JSONL not found: %v\n", truncate(sessionID, 14), err)
		return ""
	}

	result, err := ingest.ParseFile(transcriptPath)
	if err != nil {
		log.Printf("ai-title backfill: skip %s — parse error: %v\n", truncate(sessionID, 14), err)
		return ""
	}

	return result.Title
}

// writeBackfillMarker creates the sentinel file (and its parent directory)
// to signal that the backfill has completed.
func writeBackfillMarker(markerPath string) error {
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(markerPath)
	if err != nil {
		return err
	}
	return f.Close()
}
