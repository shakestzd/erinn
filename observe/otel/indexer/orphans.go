package indexer

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OrphanRetentionDays is the number of days after which an orphan session
// directory (one with an events.ndjson but no corresponding row in the sessions
// table) is eligible for deletion by "wipnote cleanup orphan-sessions --delete".
//
// Retention policy: q-orphan-retention-days (recommended 14 days).
// Auto-deletion scheduling is owned by Slice 6 (single-writer queue). This
// package only provides the detection helpers and the CLI command; callers are
// responsible for scheduling.
const OrphanRetentionDays = 14

// OrphanInfo describes a session directory that has no matching row in the
// sessions table.
type OrphanInfo struct {
	SessionID   string
	DirPath     string
	Age         time.Duration
	LastWriteAt time.Time
}

// FindOrphanSessions scans wipnoteDir for session directories that have an
// events.ndjson file but no corresponding row in the sessions table.
//
// A directory is considered an orphan when:
//   - It contains an events.ndjson file.
//   - No row exists in sessions WHERE session_id = <dirname>.
//
// The returned slice is ordered by directory enumeration order (OS-dependent).
// This function never modifies the filesystem.
func FindOrphanSessions(wipnoteDir string, database *sql.DB) ([]OrphanInfo, error) {
	sessionsDir := filepath.Join(wipnoteDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	type candidate struct {
		id      string
		dirPath string
	}
	var candidates []candidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ndjson := filepath.Join(sessionsDir, e.Name(), "events.ndjson")
		if _, err := os.Stat(ndjson); err == nil {
			candidates = append(candidates, candidate{
				id:      e.Name(),
				dirPath: filepath.Join(sessionsDir, e.Name()),
			})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
	}
	known, err := queryKnownSessionIDs(database, ids)
	if err != nil {
		return nil, fmt.Errorf("query known sessions: %w", err)
	}

	now := time.Now().UTC()
	var orphans []OrphanInfo
	for _, c := range candidates {
		if known[c.id] {
			continue
		}
		lastWrite, dirAge := inspectOrphanDir(c.dirPath, now)
		orphans = append(orphans, OrphanInfo{
			SessionID:   c.id,
			DirPath:     c.dirPath,
			Age:         dirAge,
			LastWriteAt: lastWrite,
		})
	}
	return orphans, nil
}

// IsEligibleForDeletion reports whether an orphan is safe to delete:
//   - Age exceeds OrphanRetentionDays.
//   - No writes within the last 24 hours (allows slow writers to finish).
func IsEligibleForDeletion(o OrphanInfo) bool {
	if o.Age < time.Duration(OrphanRetentionDays)*24*time.Hour {
		return false
	}
	sinceLastWrite := time.Since(o.LastWriteAt)
	return sinceLastWrite >= 24*time.Hour
}

// orphanMinAge is the minimum directory age before a session lacking a
// DB row may be skipped by the indexer. Below this floor we always
// process the directory — hook-failure, late-plugin-load, and OTel-only
// sessions can produce NDJSON before any session row is written, and
// the indexer or downstream code is what eventually creates the row.
// Set well below OrphanRetentionDays (14d) so the deletion path is
// still owned by the cleanup CLI; this gate only controls what the
// indexer chooses to index per tick.
const orphanMinAge = time.Hour

// orphanQuiescenceWindow is the "no recent writes" window. A session
// directory whose most recent file modification is within this window
// is considered actively producing telemetry and is processed even if
// it has no DB row yet — the row may land momentarily.
const orphanQuiescenceWindow = 5 * time.Minute

// filterSessionsByDB filters candidate session IDs to those the indexer
// should process this tick. Sessions with a corresponding sessions row
// are always kept. Sessions WITHOUT a row are kept when they are recent
// (< orphanMinAge) OR actively producing telemetry (last write within
// orphanQuiescenceWindow). Only sessions that are BOTH stale AND
// quiescent are skipped — these are true orphans whose telemetry will
// be cleaned up by `wipnote cleanup orphan-sessions` per the
// OrphanRetentionDays policy.
//
// This is the fix for roborev #1505 (slice 11): the prior
// implementation skipped every directory that lacked a DB row, which
// permanently discarded valid telemetry from hook-failure,
// late-plugin-load, and OTel-only sessions whose row had not been
// written yet. The writer queue or hook gate creates the row
// eventually; the indexer must wait for that to happen instead of
// silently dropping data.
//
// On query error the function logs and returns the full candidate list
// (fail-open) so a transient DB hiccup never silently drops telemetry.
func filterSessionsByDB(database *sql.DB, wipnoteDir string, candidates []string) []string {
	if database == nil || len(candidates) == 0 {
		return candidates
	}
	known, err := queryKnownSessionIDs(database, candidates)
	if err != nil {
		log.Printf("indexer: orphan filter failed (processing all sessions): %v", err)
		return candidates
	}
	sessionsDir := filepath.Join(wipnoteDir, "sessions")
	now := time.Now().UTC()
	var kept []string
	for _, id := range candidates {
		if known[id] {
			kept = append(kept, id)
			continue
		}
		dirPath := filepath.Join(sessionsDir, id)
		lastWrite, age := inspectOrphanDir(dirPath, now)
		// Recent OR active sessions are NOT orphans yet — keep them
		// in the indexer's working set so the row can land before we
		// give up on the telemetry.
		if age < orphanMinAge {
			kept = append(kept, id)
			continue
		}
		if !lastWrite.IsZero() && now.Sub(lastWrite) < orphanQuiescenceWindow {
			kept = append(kept, id)
			continue
		}
		log.Printf("indexer: skipping orphan session dir %s (age=%s, last-write=%s) — no DB row, stale and quiescent",
			id, formatAge(age), lastWrite.Format(time.RFC3339))
	}
	return kept
}

// queryKnownSessionIDs returns the subset of candidates that exist in the
// sessions table, as a set.
func queryKnownSessionIDs(database *sql.DB, candidates []string) (map[string]bool, error) {
	if len(candidates) == 0 {
		return map[string]bool{}, nil
	}
	placeholders := strings.Repeat("?,", len(candidates))
	placeholders = strings.TrimRight(placeholders, ",")
	query := fmt.Sprintf("SELECT session_id FROM sessions WHERE session_id IN (%s)", placeholders)
	args := make([]any, len(candidates))
	for i, id := range candidates {
		args[i] = id
	}
	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()
	known := make(map[string]bool, len(candidates))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan session_id: %w", err)
		}
		known[id] = true
	}
	return known, rows.Err()
}

// inspectOrphanDir returns the most-recent file modification time in dirPath
// (one level deep) and the age of the directory relative to now.
func inspectOrphanDir(dirPath string, now time.Time) (lastWrite time.Time, age time.Duration) {
	dirInfo, err := os.Stat(dirPath)
	if err != nil {
		return now, 0
	}
	age = now.Sub(dirInfo.ModTime())
	lastWrite = dirInfo.ModTime()

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return lastWrite, age
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(lastWrite) {
			lastWrite = info.ModTime()
		}
	}
	return lastWrite, age
}

// formatAge formats a duration as a human-readable string.
func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}
