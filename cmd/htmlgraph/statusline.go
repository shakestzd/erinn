package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/shakestzd/htmlgraph/internal/workitem"
	"github.com/spf13/cobra"
)

func statuslineCmd() *cobra.Command {
	var sessionID string

	cmd := &cobra.Command{
		Use:   "statusline",
		Short: "Print the active work item for Claude Code status line",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatusline(sessionID)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "Session ID to scope the active work item lookup")
	return cmd
}

func runStatusline(sessionID string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return nil
	}

	// If a session ID is provided, look up the session's active_feature_id from SQLite.
	if sessionID != "" {
		return statuslineFromSession(dir, sessionID)
	}

	// No session ID: return nothing. The global HTML scan has no session context and
	// would leak cross-session state (e.g. show a bug from session B when session A
	// is calling). An empty statusline is correct when no session is scoped.
	return nil
}

func statuslineFromSession(dir, sessionID string) error {
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(dir))
	if err != nil {
		return nil
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return nil
	}
	defer database.Close()

	// Prefer per-agent attribution (active_work_items), fall back to legacy
	// sessions.active_feature_id for sessions that predate the new table.
	agentID := dbpkg.NormaliseAgentID(os.Getenv("HTMLGRAPH_AGENT_ID"))
	featureID := dbpkg.GetActiveWorkItemWithFallback(database, sessionID, agentID)
	if featureID == "" {
		// No active feature for this session — show nothing.
		// A global fallback would leak another terminal's active feature
		// into this status line, which is misleading in multi-session setups.
		return nil
	}

	// Look up the title from the HTML file.
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		// We have the ID but can't get title — still show the ID.
		fmt.Println(featureID)
		return nil
	}
	defer p.Close()

	// Find the feature node.
	var featureType string
	var featureTitle string
	for _, typeName := range []string{"bug", "feature", "spike"} {
		col := collectionFor(p, typeName)
		node, err := col.Get(featureID)
		if err == nil && node != nil {
			if node.Status == "done" || node.Status == "completed" {
				return nil // Feature was completed — don't show it
			}
			featureType = typeName
			featureTitle = node.Title
			break
		}
	}
	if featureTitle == "" {
		return nil
	}

	// Check if feature belongs to a track.
	trackLine := resolveTrackContext(database, dir, featureID)

	if trackLine != "" {
		fmt.Printf("%s → %s %s\n", trackLine, iconFor(featureType), truncate(featureTitle, 25))
	} else {
		fmt.Printf("%s %s\n", iconFor(featureType), truncate(featureTitle, 30))
	}
	return nil
}

// resolveTrackContext returns a formatted track summary if the feature belongs to a track.
// Format: "track_icon Track Title [done/total]"
// Returns empty string if no track.
// dir is the .htmlgraph directory; it is used to read HTML files for accurate counts
// since the SQLite features table may be stale (not all features are indexed).
func resolveTrackContext(database *sql.DB, dir, featureID string) string {
	// Check track_id in SQLite first (fast path).
	var trackID sql.NullString
	database.QueryRow("SELECT track_id FROM features WHERE id = ?", featureID).Scan(&trackID) //nolint:errcheck

	if !trackID.Valid || trackID.String == "" {
		// Check graph_edges for part_of relationship.
		database.QueryRow(`
			SELECT to_node_id FROM graph_edges
			WHERE from_node_id = ? AND relationship_type = 'part_of'
			AND to_node_id LIKE 'trk-%'
			LIMIT 1`, featureID).Scan(&trackID) //nolint:errcheck
	}

	if !trackID.Valid || trackID.String == "" {
		return ""
	}

	// Get track title from SQLite (tracks table is reliably populated).
	var trackTitle sql.NullString
	database.QueryRow("SELECT title FROM tracks WHERE id = ?", trackID.String).Scan(&trackTitle) //nolint:errcheck

	// Count done/total by reading HTML files directly — same source that
	// `htmlgraph track show` uses. SQLite features rows are often incomplete
	// (features indexed in graph_edges but absent from the features table),
	// which caused [0/0] to appear in the status line.
	features := loadLinkedByType(dir, "features", trackID.String)
	total := len(features)
	done := 0
	for _, f := range features {
		if f.Status == "done" || f.Status == "completed" {
			done++
		}
	}

	title := trackID.String
	if trackTitle.Valid && trackTitle.String != "" {
		title = truncate(trackTitle.String, 25)
	}

	return fmt.Sprintf("%s %s [%d/%d]", iconFor("track"), title, done, total)
}


func iconFor(typeName string) string {
	switch typeName {
	case "bug":
		return "\uf188" //  bug
	case "feature":
		return "\uf0eb" //  lightbulb
	case "spike":
		return "\uf0e7" //  bolt
	case "track":
		return "\uf018" //  road
	default:
		return "\uf111" //  circle
	}
}

// WriteStatuslineCache writes the active work item summary to a project-scoped
// cache file. The filename includes a hash of the htmlgraphDir so different
// projects never overwrite each other's cache (bug-95dc78ba).
// Pass empty featureID to clear the cache (on complete).
//
// Writes are atomic (write-to-temp + rename) so parallel agents calling
// feature start cannot produce a torn cache file (bug-d2d3fb3f).
func WriteStatuslineCache(htmlgraphDir, featureID string) {
	cachePath := statuslineCachePath(htmlgraphDir)
	if cachePath == "" {
		return
	}

	var payload []byte
	if featureID != "" {
		payload = []byte(buildCacheLine(htmlgraphDir, featureID))
	}
	atomicWriteFile(cachePath, payload, 0o644)
}

// atomicWriteFile writes data to path via a temp file in the same directory
// followed by os.Rename. Errors are silently dropped — callers treat cache
// writes as best-effort.
func atomicWriteFile(path string, data []byte, mode os.FileMode) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".tmp-*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	_ = os.Chmod(tmpPath, mode)
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
	}
}

// buildCacheLine produces the display string for a work item, including
// its track context if available. Format: "Track [done/total] -> Feature"
func buildCacheLine(htmlgraphDir, featureID string) string {
	p, err := workitem.Open(htmlgraphDir, "claude-code")
	if err != nil {
		return featureID
	}
	defer p.Close()

	var featureType, featureTitle string
	for _, typeName := range []string{"bug", "feature", "spike"} {
		col := collectionFor(p, typeName)
		node, nodeErr := col.Get(featureID)
		if nodeErr == nil && node != nil {
			featureType = typeName
			featureTitle = node.Title
			break
		}
	}
	if featureTitle == "" {
		return featureID
	}

	// Attempt track context via DB.
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(htmlgraphDir))
	if err != nil {
		return fmt.Sprintf("%s %s", iconFor(featureType), truncate(featureTitle, 30))
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Sprintf("%s %s", iconFor(featureType), truncate(featureTitle, 30))
	}
	defer database.Close()

	trackLine := resolveTrackContext(database, htmlgraphDir, featureID)
	if trackLine != "" {
		return fmt.Sprintf("%s -> %s %s",
			trackLine, iconFor(featureType), truncate(featureTitle, 25))
	}
	return fmt.Sprintf("%s %s", iconFor(featureType), truncate(featureTitle, 30))
}

// ReadStatuslineCache reads the project-scoped cached status line from disk.
// Returns empty string if the cache file doesn't exist or is empty.
// htmlgraphDir is required to scope the lookup to the correct project.
func ReadStatuslineCache(htmlgraphDir string) string {
	cachePath := statuslineCachePath(htmlgraphDir)
	if cachePath == "" {
		return ""
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// statuslineCachePath returns the project-scoped cache file path.
// Format: <cacheDir>/.htmlgraph-statusline-<hash8>
func statuslineCachePath(htmlgraphDir string) string {
	cacheDir := os.Getenv("HTMLGRAPH_CACHE_DIR")
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cacheDir = home
	}
	if htmlgraphDir == "" {
		return ""
	}
	h := sha256.Sum256([]byte(filepath.Clean(htmlgraphDir)))
	suffix := hex.EncodeToString(h[:4]) // 8 hex chars
	return filepath.Join(cacheDir, ".htmlgraph-statusline-"+suffix)
}
