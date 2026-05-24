// Package retention implements the session data retention policy for wipnote.
//
// On serve startup and every 24h it walks .wipnote/sessions/, finds sessions
// whose DB status is 'completed' and whose completed_at is older than
// WIPNOTE_SESSION_RETAIN_DAYS (default 30), archives events.ndjson into
// .wipnote/archive/<yyyy-mm>/<sid>.tar.gz, and removes the live session dir.
//
// Dry-run mode (WIPNOTE_RETENTION_DRYRUN=1) logs intended actions without
// moving any files.
package retention

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRetainDays = 30
	runInterval       = 24 * time.Hour
)

// Run executes one retention pass over .wipnote/sessions/.
// It queries the DB for completed sessions older than the retention window,
// archives events.ndjson, and removes the live session directory.
// When dryRun is true, actions are logged but no files are moved.
func Run(database *sql.DB, wipnoteDir string, dryRun bool) error {
	retainDays := retainDaysFromEnv()
	cutoff := time.Now().UTC().Add(-time.Duration(retainDays) * 24 * time.Hour)

	rows, err := database.Query(`
		SELECT session_id, completed_at
		FROM sessions
		WHERE status = 'completed'
		  AND completed_at IS NOT NULL
		  AND completed_at < ?
		ORDER BY completed_at ASC`,
		cutoff.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("retention query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sessionID, completedAtStr string
		if err := rows.Scan(&sessionID, &completedAtStr); err != nil {
			continue
		}
		completedAt, err := time.Parse(time.RFC3339, completedAtStr)
		if err != nil {
			continue
		}
		if err := archiveSession(wipnoteDir, sessionID, completedAt, dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "retention: archive session %s: %v\n", sessionID, err)
		}
	}
	return rows.Err()
}

// Sweep runs a full disk-retention pass over a project's .wipnote/ dir:
//  1. log rotation/cap for serve-auto.log, serve-<id>.log, and debug.log,
//  2. the DB-status='completed' archive pass (Run), and
//  3. the ndjson coverage sweep for inactive+ingested sessions that never
//     reached completed_at (crashed / disconnected / stale-active).
//
// It is cheap, idempotent, and fail-safe — every step degrades to a no-op on
// error and never deletes un-ingested or active-session data. activeSessionID
// may be "" when no live session is known (e.g. manual `wipnote prune`).
// Returns the SweepResult from the ndjson pass for caller reporting.
func Sweep(database *sql.DB, wipnoteDir, activeSessionID string, dryRun bool) (SweepResult, error) {
	projectDir := filepath.Dir(wipnoteDir)
	cfg := LoadConfig(projectDir)

	if n, err := rotateProjectLogs(wipnoteDir, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "retention: log rotation: %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "retention: reclaimed %d bytes from oversized logs\n", n)
	}

	if database != nil {
		if err := Run(database, wipnoteDir, dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "retention: completed-session archive: %v\n", err)
		}
	}

	return SweepNDJSON(wipnoteDir, activeSessionID, cfg, dryRun)
}

// StartLoop runs an initial retention pass at startup, then repeats every 24h.
// It returns immediately and runs in the background until ctx is cancelled.
func StartLoop(ctx context.Context, database *sql.DB, wipnoteDir string) {
	dryRun := os.Getenv("WIPNOTE_RETENTION_DRYRUN") == "1"
	go func() {
		// Run once at startup.
		if _, err := Sweep(database, wipnoteDir, "", dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "retention: startup pass: %v\n", err)
		}
		ticker := time.NewTicker(runInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := Sweep(database, wipnoteDir, "", dryRun); err != nil {
					fmt.Fprintf(os.Stderr, "retention: periodic pass: %v\n", err)
				}
			}
		}
	}()
}

// archiveSession archives events.ndjson for the given session into
// .wipnote/archive/<yyyy-mm>/<sid>.tar.gz, then removes the session dir.
func archiveSession(wipnoteDir, sessionID string, completedAt time.Time, dryRun bool) error {
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	eventsFile := filepath.Join(sessDir, "events.ndjson")

	// Nothing to archive if session dir or events file doesn't exist.
	if _, err := os.Stat(eventsFile); os.IsNotExist(err) {
		// Session dir exists but has no events; just remove it if not dry-run.
		if _, err2 := os.Stat(sessDir); err2 == nil {
			if dryRun {
				fmt.Printf("retention: [dry-run] would remove empty session dir %s\n", sessDir)
				return nil
			}
			return os.RemoveAll(sessDir)
		}
		return nil
	}

	if !indexerCaughtUp(sessDir, eventsFile) {
		return nil // indexer still processing — skip this cycle
	}

	month := completedAt.Format("2006-01")
	archiveDir := filepath.Join(wipnoteDir, "archive", month)
	archivePath := filepath.Join(archiveDir, sessionID+".tar.gz")

	if dryRun {
		fmt.Printf("retention: [dry-run] would archive %s -> %s\n", eventsFile, archivePath)
		fmt.Printf("retention: [dry-run] would remove %s\n", sessDir)
		return nil
	}

	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}

	if err := writeTarGz(archivePath, sessionID, eventsFile); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}

	if err := os.RemoveAll(sessDir); err != nil {
		return fmt.Errorf("remove session dir: %w", err)
	}

	return nil
}

// writeTarGz writes a .tar.gz containing events.ndjson from sessDir.
// The archive contains a single entry: <sessionID>/events.ndjson.
func writeTarGz(archivePath, sessionID, eventsFile string) error {
	f, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	success := false
	defer func() {
		if !success {
			f.Close()
			os.Remove(archivePath)
		}
	}()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	src, err := os.Open(eventsFile)
	if err != nil {
		return fmt.Errorf("open events file: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat events file: %w", err)
	}

	hdr := &tar.Header{
		Name:    sessionID + "/events.ndjson",
		Mode:    0o644,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	if _, err := io.Copy(tw, src); err != nil {
		return fmt.Errorf("copy events data: %w", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("finalize tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("finalize gzip: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close archive file: %w", err)
	}
	success = true
	return nil
}

// indexerCaughtUp returns true when the indexer has fully processed the
// events.ndjson file (offset == file size). Prevents archiving data that
// hasn't been indexed yet.
func indexerCaughtUp(sessDir, eventsFile string) bool {
	offsetData, err := os.ReadFile(filepath.Join(sessDir, ".index-offset"))
	if err != nil {
		return false // no checkpoint means indexer hasn't started
	}
	offset, err := strconv.ParseInt(string(offsetData), 10, 64)
	if err != nil {
		return false
	}
	info, err := os.Stat(eventsFile)
	if err != nil {
		return false
	}
	return offset >= info.Size()
}

// ArchiveSession is the exported entry-point for archiving a single session on
// demand (e.g. `wipnote session archive <id>`). It delegates to archiveSession
// using the session's mtime as the timestamp for the archive subdirectory, and
// honours dryRun. Returns an error when the session dir does not exist, when the
// indexer has not yet caught up (un-ingested data would be lost), or when the
// underlying tar/remove operations fail.
//
// The caller is responsible for ensuring the session is not the active session
// before calling this function — ArchiveSession does not check.
func ArchiveSession(wipnoteDir, sessionID string, dryRun bool) error {
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	eventsFile := filepath.Join(sessDir, "events.ndjson")

	// Determine the timestamp to use for the archive subdirectory. Prefer the
	// mtime of events.ndjson; fall back to now when the file is absent (empty
	// session dir case — archiveSession will handle clean-up).
	ts := time.Now()
	if info, err := os.Stat(eventsFile); err == nil {
		ts = info.ModTime()
	}

	return archiveSession(wipnoteDir, sessionID, ts, dryRun)
}

// IndexerCaughtUp reports whether the indexer has fully consumed events.ndjson
// for the given session directory (i.e. the .index-offset marker equals the
// file size). Exported for use by `wipnote session prune`.
func IndexerCaughtUp(sessDir, eventsFile string) bool {
	return indexerCaughtUp(sessDir, eventsFile)
}

// SessionDir returns the absolute path of the live session directory for sid.
func SessionDir(wipnoteDir, sid string) string {
	return filepath.Join(wipnoteDir, "sessions", sid)
}

// retainDaysFromEnv reads WIPNOTE_SESSION_RETAIN_DAYS, defaulting to 30.
func retainDaysFromEnv() int {
	if v := os.Getenv("WIPNOTE_SESSION_RETAIN_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultRetainDays
}

// ExtractArchive extracts a .tar.gz archive from .wipnote/archive/ back into
// .wipnote/sessions/<sid>/ so the indexer can pick it up on next replay.
func ExtractArchive(wipnoteDir, sessionID string) error {
	// Search for the archive across month subdirectories.
	archiveRoot := filepath.Join(wipnoteDir, "archive")
	archivePath, err := findArchive(archiveRoot, sessionID)
	if err != nil {
		return fmt.Errorf("find archive for session %s: %w", sessionID, err)
	}

	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	return extractTarGz(archivePath, filepath.Join(wipnoteDir, "sessions"))
}

// findArchive searches month subdirectories under archiveRoot for <sid>.tar.gz.
func findArchive(archiveRoot, sessionID string) (string, error) {
	target := sessionID + ".tar.gz"
	entries, err := os.ReadDir(archiveRoot)
	if err != nil {
		return "", fmt.Errorf("read archive dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(archiveRoot, e.Name(), target)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("archive not found for session %s", sessionID)
}

// extractTarGz extracts all entries from a .tar.gz file into destDir.
func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(destDir, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", hdr.Name, err)
		}

		out, err := os.Create(target)
		if err != nil {
			return fmt.Errorf("create %s: %w", target, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("extract %s: %w", hdr.Name, err)
		}
		out.Close()
	}
	return nil
}
