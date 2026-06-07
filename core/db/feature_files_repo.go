package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// DefaultFileOverlapWindow is the recency window for live file-overlap
// detection (Tier 1 / feat-b5fa9392). Aligned to the claim heartbeat / lease
// cadence: a file touched by another session within this window is treated as
// a potential concurrent-edit collision. Configurable via
// .wipnote/config.json "file_overlap_window_minutes".
const DefaultFileOverlapWindow = 15 * time.Minute

// FileOverlap is one other session that touched the same file_path within the
// recency window. SessionID is the *other* session; the caller filters these
// to LIVE sessions (heartbeat-recency liveness, NOT sessions.status) before
// surfacing them so stale 'active' ghost sessions never false-fire (bug-6c3e8252).
type FileOverlap struct {
	SessionID string
	FeatureID string
	Operation string
	LastSeen  string
}

// FindFileOverlaps returns every OTHER session (session_id != excludeSessionID)
// that touched filePath within `window` of now, as recorded in feature_files.
//
// This is a single indexed SELECT with ZERO writes. It is served by the
// composite index idx_feature_files_path_seen(file_path, last_seen): the
// equality on file_path plus the range on last_seen is an index probe, not a
// per-path range scan (preserving the feat-156e0a1a zero-SQLITE_BUSY hot-path
// guarantee — nothing on this path acquires a write lock).
//
// Recency is computed in SQL via datetime('now', ?) so both sides of the
// comparison are SQLite's UTC 'YYYY-MM-DD HH:MM:SS' string form (the same form
// CURRENT_TIMESTAMP writes into last_seen) — no timezone parsing, no client
// clock skew. Liveness is intentionally NOT filtered here: the caller applies
// SessionLivenessByHeartbeat so the SQL stays a single indexed range probe.
func FindFileOverlaps(db *sql.DB, filePath, excludeSessionID string, window time.Duration) ([]FileOverlap, error) {
	if db == nil || filePath == "" {
		return nil, nil
	}
	if window <= 0 {
		window = DefaultFileOverlapWindow
	}
	// Negative-minutes modifier, e.g. "-15 minutes".
	mod := fmt.Sprintf("-%d minutes", int64(window/time.Minute))
	rows, err := db.Query(`
		SELECT COALESCE(session_id, ''), feature_id,
		       COALESCE(operation, ''), last_seen
		FROM feature_files
		WHERE file_path = ?
		  AND last_seen >= datetime('now', ?)
		  AND COALESCE(session_id, '') != ''
		  AND COALESCE(session_id, '') != ?
		ORDER BY last_seen DESC`,
		filePath, mod, excludeSessionID)
	if err != nil {
		return nil, fmt.Errorf("find file overlaps for %s: %w", filePath, err)
	}
	defer rows.Close()
	var out []FileOverlap
	for rows.Next() {
		var o FileOverlap
		if err := rows.Scan(&o.SessionID, &o.FeatureID, &o.Operation, &o.LastSeen); err != nil {
			continue
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// FindLiveFileOverlaps wraps FindFileOverlaps and keeps only overlaps whose
// other session is honestly LIVE per heartbeat recency (Tier 3 primitive),
// de-duplicated to one entry per other session (newest last_seen wins, which
// the ORDER BY in FindFileOverlaps already guarantees). This is the function
// `wipnote who` and the PreToolUse advisory consume — a stale status='active'
// ghost session never produces a ⚠ (bug-6c3e8252).
func FindLiveFileOverlaps(db *sql.DB, filePath, excludeSessionID string, window time.Duration, livenessThreshold time.Duration) ([]FileOverlap, error) {
	raw, err := FindFileOverlaps(db, filePath, excludeSessionID, window)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []FileOverlap
	for _, o := range raw {
		if seen[o.SessionID] {
			continue
		}
		if !SessionLivenessByHeartbeat(db, o.SessionID, livenessThreshold) {
			continue
		}
		seen[o.SessionID] = true
		out = append(out, o)
	}
	return out, nil
}

// FileOverlapWindow returns the configured live file-overlap recency window.
// Reads .wipnote/config.json under projectDir via the same local os.ReadFile
// pattern as LivenessStalenessThreshold / readTaskCompletionConfig (there is
// NO shared internal/config package). Falls back to DefaultFileOverlapWindow
// when the file is missing/unreadable, the key is absent, or the value is
// non-positive.
func FileOverlapWindow(projectDir string) time.Duration {
	if projectDir == "" {
		return DefaultFileOverlapWindow
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "config.json"))
	if err != nil {
		return DefaultFileOverlapWindow
	}
	var cfg fileOverlapConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultFileOverlapWindow
	}
	if cfg.WindowMinutes <= 0 {
		return DefaultFileOverlapWindow
	}
	return time.Duration(cfg.WindowMinutes) * time.Minute
}

// fileOverlapConfig mirrors the local config.json decode pattern; only the one
// tunable field is read, everything else in config.json is ignored.
type fileOverlapConfig struct {
	WindowMinutes int `json:"file_overlap_window_minutes"`
}

// UpsertFeatureFile inserts a feature_file row or updates last_seen on conflict.
// The UNIQUE constraint is (feature_id, file_path), so re-touching the same file
// within the same feature just refreshes the timestamp and operation.
func UpsertFeatureFile(db *sql.DB, ff *models.FeatureFile) error {
	_, err := db.Exec(`
		INSERT INTO feature_files
			(id, feature_id, file_path, operation, session_id,
			 first_seen, last_seen, created_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(feature_id, file_path) DO UPDATE SET
			last_seen  = CURRENT_TIMESTAMP,
			operation  = excluded.operation,
			session_id = excluded.session_id`,
		ff.ID, ff.FeatureID, ff.FilePath, ff.Operation, nullStr(ff.SessionID),
	)
	if err != nil {
		return fmt.Errorf("upsert feature_file %s/%s: %w", ff.FeatureID, ff.FilePath, err)
	}
	return nil
}

// CountFilesByFeature returns the number of distinct files touched by a feature.
func CountFilesByFeature(db *sql.DB, featureID string) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM feature_files WHERE feature_id = ?`, featureID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count files for %s: %w", featureID, err)
	}
	return count, nil
}

// ListFilesByFeature returns all file paths recorded for a feature.
func ListFilesByFeature(db *sql.DB, featureID string) ([]models.FeatureFile, error) {
	rows, err := db.Query(`
		SELECT id, feature_id, file_path, operation,
		       COALESCE(session_id, ''),
		       first_seen, last_seen, created_at
		FROM feature_files
		WHERE feature_id = ?
		ORDER BY last_seen DESC`, featureID)
	if err != nil {
		return nil, fmt.Errorf("list files for feature %s: %w", featureID, err)
	}
	defer rows.Close()
	return scanFeatureFiles(rows)
}

// ListFeaturesByFile returns all features that have touched a given file path.
func ListFeaturesByFile(db *sql.DB, filePath string) ([]models.FeatureFile, error) {
	rows, err := db.Query(`
		SELECT id, feature_id, file_path, operation,
		       COALESCE(session_id, ''),
		       first_seen, last_seen, created_at
		FROM feature_files
		WHERE file_path = ?
		ORDER BY last_seen DESC`, filePath)
	if err != nil {
		return nil, fmt.Errorf("list features for file %s: %w", filePath, err)
	}
	defer rows.Close()
	return scanFeatureFiles(rows)
}

// RelatedFeature summarises another feature that shares files with a given one.
type RelatedFeature struct {
	FeatureID   string   `json:"feature_id"`
	Title       string   `json:"title"`
	SharedCount int      `json:"shared_count"`
	SharedFiles []string `json:"shared_files"`
}

// FindRelatedFeatures returns features that share at least one file with
// featureID, ordered by shared file count descending.
func FindRelatedFeatures(db *sql.DB, featureID string) ([]RelatedFeature, error) {
	// Step 1: find related feature IDs and their shared counts.
	rows, err := db.Query(`
		SELECT ff2.feature_id, COUNT(DISTINCT ff2.file_path) AS shared_count
		FROM feature_files ff1
		JOIN feature_files ff2 ON ff1.file_path = ff2.file_path
		WHERE ff1.feature_id = ? AND ff2.feature_id != ?
		GROUP BY ff2.feature_id
		ORDER BY shared_count DESC`, featureID, featureID)
	if err != nil {
		return nil, fmt.Errorf("find related features for %s: %w", featureID, err)
	}
	defer rows.Close()

	var related []RelatedFeature
	for rows.Next() {
		var r RelatedFeature
		if err := rows.Scan(&r.FeatureID, &r.SharedCount); err != nil {
			continue
		}
		related = append(related, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Step 2: populate shared file paths and title for each related feature.
	for i := range related {
		rid := related[i].FeatureID

		// Shared file paths.
		fileRows, ferr := db.Query(`
			SELECT ff2.file_path
			FROM feature_files ff1
			JOIN feature_files ff2 ON ff1.file_path = ff2.file_path
			WHERE ff1.feature_id = ? AND ff2.feature_id = ?
			GROUP BY ff2.file_path
			ORDER BY ff2.file_path`, featureID, rid)
		if ferr == nil {
			for fileRows.Next() {
				var fp string
				if fileRows.Scan(&fp) == nil {
					related[i].SharedFiles = append(related[i].SharedFiles, fp)
				}
			}
			fileRows.Close()
		}

		// Title from the features table (empty when not yet indexed).
		var title string
		_ = db.QueryRow(`SELECT COALESCE(title, '') FROM features WHERE id = ?`, rid).Scan(&title)
		related[i].Title = title
	}

	return related, nil
}

// FileTraceResult represents a feature that touched a file, enriched with
// title, status, track ID, and operation metadata for the trace command.
type FileTraceResult struct {
	FeatureID string
	Title     string
	Status    string
	TrackID   string
	Operation string
	LastSeen  string
}

// TraceFile returns all features that touched a given file path, enriched
// with title, status, and parent track. Used by `wipnote trace <file>`.
func TraceFile(database *sql.DB, filePath string) ([]FileTraceResult, error) {
	rows, err := database.Query(`
		SELECT ff.feature_id,
		       COALESCE(f.title, ''),
		       COALESCE(f.status, ''),
		       COALESCE(f.track_id, ''),
		       ff.operation,
		       ff.last_seen
		FROM feature_files ff
		LEFT JOIN features f ON f.id = ff.feature_id
		WHERE ff.file_path = ?
		ORDER BY ff.last_seen DESC`, filePath)
	if err != nil {
		return nil, fmt.Errorf("trace file %s: %w", filePath, err)
	}
	defer rows.Close()

	var results []FileTraceResult
	for rows.Next() {
		var r FileTraceResult
		if err := rows.Scan(&r.FeatureID, &r.Title, &r.Status, &r.TrackID, &r.Operation, &r.LastSeen); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// FileOwner identifies a feature/track that owns a file path.
type FileOwner struct {
	FeatureID  string
	TrackID    string
	Title      string
	TouchCount int
}

// ResolveFileOwner returns the most likely owning feature for a file path,
// based on the most frequent feature_id in feature_files for that path.
// Returns nil if no feature has touched this file.
func ResolveFileOwner(db *sql.DB, filePath string) *FileOwner {
	var featureID string
	var count int
	err := db.QueryRow(`
		SELECT feature_id, COUNT(*) as cnt
		FROM feature_files
		WHERE file_path = ?
		GROUP BY feature_id
		ORDER BY cnt DESC, last_seen DESC
		LIMIT 1`, filePath).Scan(&featureID, &count)
	if err != nil || featureID == "" {
		return nil
	}

	owner := &FileOwner{FeatureID: featureID, TouchCount: count}

	// Resolve title and track from features table.
	db.QueryRow(`SELECT COALESCE(title, ''), COALESCE(track_id, '') FROM features WHERE id = ?`,
		featureID).Scan(&owner.Title, &owner.TrackID) //nolint:errcheck

	return owner
}

// scanFeatureFiles reads rows into a slice of FeatureFile.
func scanFeatureFiles(rows *sql.Rows) ([]models.FeatureFile, error) {
	var out []models.FeatureFile
	for rows.Next() {
		var ff models.FeatureFile
		var firstSeen, lastSeen, createdAt string
		if err := rows.Scan(
			&ff.ID, &ff.FeatureID, &ff.FilePath, &ff.Operation,
			&ff.SessionID, &firstSeen, &lastSeen, &createdAt,
		); err != nil {
			continue
		}
		ff.FirstSeen, _ = time.Parse("2006-01-02 15:04:05", firstSeen)
		ff.LastSeen, _ = time.Parse("2006-01-02 15:04:05", lastSeen)
		ff.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		out = append(out, ff)
	}
	return out, rows.Err()
}
