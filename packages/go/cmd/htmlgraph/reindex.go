package main

import (
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/htmlparse"
	"github.com/spf13/cobra"
)

const metaKeyLastIndexedCommit = "last_indexed_commit"

func reindexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Sync HTML work items to SQLite index",
		Long: `Reads HTML work item files from .htmlgraph/ and upserts them into the SQLite index.

By default runs incrementally: only files changed since the last successful reindex
are reparsed. Use --full to force a complete reparse of all files.`,
		RunE: runReindex,
	}
	cmd.Flags().Bool("full", false, "Force full reindex of all HTML files (ignores git diff)")
	return cmd
}

func runReindex(cmd *cobra.Command, _ []string) error {
	fullFlag, _ := cmd.Flags().GetBool("full")

	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	database, err := dbpkg.Open(filepath.Join(htmlgraphDir, "htmlgraph.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	// Determine project dir (parent of .htmlgraph/).
	projectDir := filepath.Dir(htmlgraphDir)

	// Resolve current HEAD commit (empty string if git unavailable).
	currentCommit := gitHeadCommit(projectDir)

	// Decide incremental vs full.
	lastCommit, _ := dbpkg.GetMetadata(database, metaKeyLastIndexedCommit)
	useIncremental := !fullFlag && lastCommit != "" && currentCommit != ""

	var total, upserted, errCount int
	validIDs := make(map[string]bool)

	if useIncremental {
		// Check that lastCommit still exists in git history.
		if !gitCommitExists(projectDir, lastCommit) {
			useIncremental = false
		}
	}

	if useIncremental {
		total, upserted, errCount = runIncrementalReindex(database, htmlgraphDir, projectDir, lastCommit, validIDs)
		fmt.Printf("Reindexed (incremental): %d upserted, %d errors (of %d changed HTML files)\n",
			upserted, errCount, total)
	} else {
		// Full reindex — original behaviour.
		trackTotal, trackUpserted, trackErrs := reindexTracks(database, htmlgraphDir, validIDs)
		total += trackTotal
		upserted += trackUpserted
		errCount += trackErrs

		for _, dir := range []string{"features", "bugs", "spikes"} {
			t, u, e := reindexFeatureDir(database, htmlgraphDir, dir, validIDs)
			total += t
			upserted += u
			errCount += e
		}

		purged, edgesPurged := purgeStaleEntries(database, validIDs)
		fmt.Printf("Reindexed: %d upserted, %d errors (of %d HTML files)\n",
			upserted, errCount, total)
		if purged > 0 || edgesPurged > 0 {
			fmt.Printf("Purged: %d stale features, %d stale edges\n", purged, edgesPurged)
		}
	}

	// Persist current HEAD so the next run can diff from here.
	if currentCommit != "" && errCount == 0 {
		_ = dbpkg.SetMetadata(database, metaKeyLastIndexedCommit, currentCommit)
	}

	return nil
}

// runIncrementalReindex parses only files changed between lastCommit and HEAD.
// Deleted files are removed from the DB. Returns (total, upserted, errors).
func runIncrementalReindex(
	database *sql.DB,
	htmlgraphDir, projectDir, lastCommit string,
	validIDs map[string]bool,
) (int, int, int) {
	added, deleted := gitChangedFiles(projectDir, lastCommit, htmlgraphDir)

	// Remove deleted files from the DB.
	for _, path := range deleted {
		id := idFromHTMLPath(path)
		if id != "" {
			database.Exec(`DELETE FROM features WHERE id = ?`, id)
			database.Exec(`DELETE FROM tracks WHERE id = ?`, id)
		}
	}

	if len(added) == 0 {
		return 0, 0, 0
	}

	var total, upserted, errCount int
	for _, path := range added {
		total++

		node, parseErr := htmlparse.ParseFile(path)
		if parseErr != nil {
			errCount++
			continue
		}

		createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)

		if node.Type == "track" {
			track := &dbpkg.Track{
				ID:        node.ID,
				Type:      "track",
				Title:     node.Title,
				Priority:  string(node.Priority),
				Status:    normalizeStatus(string(node.Status)),
				CreatedAt: createdAt,
				UpdatedAt: updatedAt,
			}
			if err := dbpkg.UpsertTrack(database, track); err != nil {
				errCount++
				continue
			}
		} else {
			desc := node.Content
			if len([]rune(desc)) > 500 {
				desc = string([]rune(desc)[:499]) + "…"
			}
			stepsTotal := len(node.Steps)
			stepsCompleted := 0
			for _, s := range node.Steps {
				if s.Completed {
					stepsCompleted++
				}
			}
			feat := &dbpkg.Feature{
				ID:             node.ID,
				Type:           mapNodeType(node.Type),
				Title:          node.Title,
				Description:    desc,
				Status:         normalizeStatus(string(node.Status)),
				Priority:       string(node.Priority),
				AssignedTo:     node.AgentAssigned,
				TrackID:        node.TrackID,
				CreatedAt:      createdAt,
				UpdatedAt:      updatedAt,
				StepsTotal:     stepsTotal,
				StepsCompleted: stepsCompleted,
			}
			if err := dbpkg.UpsertFeature(database, feat); err != nil {
				errCount++
				continue
			}
		}
		validIDs[node.ID] = true
		upserted++
	}
	return total, upserted, errCount
}

// gitHeadCommit returns the current HEAD commit hash, or "" on any error.
func gitHeadCommit(projectDir string) string {
	out, err := exec.Command("git", "-C", projectDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitCommitExists returns true if the given commit hash is reachable in the repo.
func gitCommitExists(projectDir, commit string) bool {
	err := exec.Command("git", "-C", projectDir, "cat-file", "-e", commit+"^{commit}").Run()
	return err == nil
}

// gitChangedFiles returns (added/modified, deleted) HTML file paths in htmlgraphDir
// that changed between fromCommit and HEAD.
// Falls back to (nil, nil) on any git error.
func gitChangedFiles(projectDir, fromCommit, htmlgraphDir string) (added []string, deleted []string) {
	// Use a path relative to projectDir so git filters correctly.
	relHg, err := filepath.Rel(projectDir, htmlgraphDir)
	if err != nil {
		return nil, nil
	}

	out, err := exec.Command(
		"git", "-C", projectDir,
		"diff", "--name-status", fromCommit, "HEAD", "--", relHg,
	).Output()
	if err != nil {
		return nil, nil
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Format: "M\tpath" or "A\tpath" or "D\tpath" or "R100\told\tnew"
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		// Renames: status starts with R; treat destination as added, source as deleted.
		if strings.HasPrefix(status, "R") && len(parts) == 3 {
			oldPath := filepath.Join(projectDir, parts[1])
			newPath := filepath.Join(projectDir, parts[2])
			if strings.HasSuffix(newPath, ".html") {
				added = append(added, newPath)
			}
			if strings.HasSuffix(oldPath, ".html") {
				deleted = append(deleted, oldPath)
			}
			continue
		}
		filePath := filepath.Join(projectDir, parts[1])
		if !strings.HasSuffix(filePath, ".html") {
			continue
		}
		switch status {
		case "A", "M":
			added = append(added, filePath)
		case "D":
			deleted = append(deleted, filePath)
		}
	}

	// Also include untracked HTML files in .htmlgraph/ (new files not yet committed).
	untrackedOut, err := exec.Command(
		"git", "-C", projectDir,
		"ls-files", "--others", "--exclude-standard", "--", relHg,
	).Output()
	if err == nil {
		for _, rel := range strings.Split(strings.TrimSpace(string(untrackedOut)), "\n") {
			if rel == "" {
				continue
			}
			path := filepath.Join(projectDir, rel)
			if strings.HasSuffix(path, ".html") {
				added = append(added, path)
			}
		}
	}

	return added, deleted
}

// idFromHTMLPath extracts a work-item ID from an HTML file path.
// Expects the filename (without extension) to be the ID (e.g. "feat-abc123.html" -> "feat-abc123").
func idFromHTMLPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".html")
}

// reindexTracks globs both flat (tracks/*.html) and nested (tracks/*/index.html)
// track files and upserts each into the tracks table.
// Returns (total, upserted, errors).
func reindexTracks(database *sql.DB, htmlgraphDir string, validIDs map[string]bool) (int, int, int) {
	patterns := []string{
		filepath.Join(htmlgraphDir, "tracks", "*.html"),
		filepath.Join(htmlgraphDir, "tracks", "*", "index.html"),
	}

	seen := make(map[string]bool)
	var total, upserted, errCount int

	for _, pattern := range patterns {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			if seen[f] {
				continue
			}
			seen[f] = true
			total++

			node, parseErr := htmlparse.ParseFile(f)
			if parseErr != nil {
				errCount++
				continue
			}

			createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)
			track := &dbpkg.Track{
				ID:        node.ID,
				Type:      "track",
				Title:     node.Title,
				Priority:  string(node.Priority),
				Status:    normalizeStatus(string(node.Status)),
				CreatedAt: createdAt,
				UpdatedAt: updatedAt,
			}

			if upsertErr := dbpkg.UpsertTrack(database, track); upsertErr != nil {
				errCount++
				continue
			}
			validIDs[node.ID] = true
			upserted++
		}
	}
	return total, upserted, errCount
}

// reindexFeatureDir upserts all HTML files in a single directory into the features table.
// Returns (total, upserted, errors).
func reindexFeatureDir(database *sql.DB, htmlgraphDir, dir string, validIDs map[string]bool) (int, int, int) {
	pattern := filepath.Join(htmlgraphDir, dir, "*.html")
	files, _ := filepath.Glob(pattern)

	var total, upserted, errCount int
	for _, f := range files {
		total++
		node, parseErr := htmlparse.ParseFile(f)
		if parseErr != nil {
			errCount++
			continue
		}

		createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)
		desc := node.Content
		if len([]rune(desc)) > 500 {
			desc = string([]rune(desc)[:499]) + "…"
		}

		stepsTotal := len(node.Steps)
		stepsCompleted := 0
		for _, s := range node.Steps {
			if s.Completed {
				stepsCompleted++
			}
		}

		feat := &dbpkg.Feature{
			ID:             node.ID,
			Type:           mapNodeType(node.Type),
			Title:          node.Title,
			Description:    desc,
			Status:         normalizeStatus(string(node.Status)),
			Priority:       string(node.Priority),
			AssignedTo:     node.AgentAssigned,
			TrackID:        node.TrackID,
			CreatedAt:      createdAt,
			UpdatedAt:      updatedAt,
			StepsTotal:     stepsTotal,
			StepsCompleted: stepsCompleted,
		}

		if upsertErr := dbpkg.UpsertFeature(database, feat); upsertErr != nil {
			errCount++
			continue
		}
		validIDs[node.ID] = true
		upserted++
	}
	return total, upserted, errCount
}

// normalizeTimes returns sensible defaults for zero-value timestamps.
func normalizeTimes(createdAt, updatedAt time.Time) (time.Time, time.Time) {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return createdAt, updatedAt
}

// purgeStaleEntries removes features, tracks, and graph_edges whose IDs are no
// longer backed by an HTML file. Returns counts of purged features+tracks and edges.
func purgeStaleEntries(database *sql.DB, validIDs map[string]bool) (int, int) {
	staleFeatureIDs := collectStaleIDs(database, "SELECT id FROM features", validIDs)
	purged := deleteByIDs(database, "DELETE FROM features WHERE id = ?", staleFeatureIDs)

	// Purge stale tracks (HTML files deleted from .htmlgraph/tracks/).
	staleTrackIDs := collectStaleIDs(database, "SELECT id FROM tracks", validIDs)
	purged += deleteByIDs(database, "DELETE FROM tracks WHERE id = ?", staleTrackIDs)

	// Purge edges that reference deleted node IDs (either endpoint).
	staleEdgeIDs := collectStaleEdgeIDs(database, validIDs)
	edgesPurged := deleteByIDs(database, "DELETE FROM graph_edges WHERE edge_id = ?", staleEdgeIDs)

	return purged, edgesPurged
}

// collectStaleIDs queries all IDs from a single-column SELECT and returns those
// not present in validIDs.
func collectStaleIDs(database *sql.DB, query string, validIDs map[string]bool) []string {
	rows, err := database.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && !validIDs[id] {
			stale = append(stale, id)
		}
	}
	return stale
}

// collectStaleEdgeIDs returns edge_ids where either endpoint (from_node_id or
// to_node_id) refers to a node no longer backed by an HTML file.
func collectStaleEdgeIDs(database *sql.DB, validIDs map[string]bool) []string {
	rows, err := database.Query("SELECT edge_id, from_node_id, to_node_id FROM graph_edges")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var edgeID, fromID, toID string
		if rows.Scan(&edgeID, &fromID, &toID) == nil {
			if !validIDs[fromID] || !validIDs[toID] {
				stale = append(stale, edgeID)
			}
		}
	}
	return stale
}

// deleteByIDs executes a parameterised DELETE for each ID and returns the count
// of successful deletions.
func deleteByIDs(database *sql.DB, query string, ids []string) int {
	count := 0
	for _, id := range ids {
		if _, err := database.Exec(query, id); err == nil {
			count++
		}
	}
	return count
}

// normalizeStatus maps HTML statuses to the features table CHECK constraint values.
// features table allows: todo, in-progress, blocked, done, active, ended, stale
func normalizeStatus(status string) string {
	switch status {
	case "todo", "in-progress", "blocked", "done", "active", "ended", "stale":
		return status
	case "completed":
		return "done"
	case "in_progress":
		return "in-progress"
	case "archived", "cancelled":
		return "ended"
	case "pending", "identified":
		return "todo"
	default:
		return "todo"
	}
}

// mapNodeType converts HTML node types to the features table CHECK constraint values.
// features table allows: feature, bug, spike, chore, epic, task
func mapNodeType(nodeType string) string {
	switch nodeType {
	case "feature":
		return "feature"
	case "bug":
		return "bug"
	case "spike":
		return "spike"
	case "track":
		return "epic"
	case "chore":
		return "chore"
	case "plan", "spec":
		return "task"
	default:
		return "feature"
	}
}
