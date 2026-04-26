package main

import (
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/htmlparse"
	"github.com/shakestzd/htmlgraph/internal/storage"
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
	cmd.Flags().BoolP("verbose", "v", false, "Print one line per error encountered during reindex")
	return cmd
}

func runReindex(cmd *cobra.Command, _ []string) error {
	fullFlag, _ := cmd.Flags().GetBool("full")
	verboseFlag, _ := cmd.Flags().GetBool("verbose")

	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	projectDir := filepath.Dir(htmlgraphDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return fmt.Errorf("ensure db dir: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	currentCommit := gitHeadCommit(projectDir)

	lastCommit, _ := dbpkg.GetMetadata(database, metaKeyLastIndexedCommit)
	useIncremental := !fullFlag && lastCommit != "" && currentCommit != ""

	var total, upserted, errCount int
	validIDs := make(map[string]bool)

	if useIncremental {
		if !gitCommitExists(projectDir, lastCommit) {
			useIncremental = false
		}
	}

	if useIncremental {
		total, upserted, errCount = runIncrementalReindex(database, htmlgraphDir, projectDir, lastCommit, validIDs, verboseFlag)
		fmt.Printf("Reindexed (incremental): %d upserted, %d errors (of %d changed HTML files)\n",
			upserted, errCount, total)
	} else {
		trackTotal, trackUpserted, trackErrs := reindexTracks(database, htmlgraphDir, projectDir, validIDs, verboseFlag)
		total += trackTotal
		upserted += trackUpserted
		errCount += trackErrs

		for _, dir := range []string{"features", "bugs", "spikes"} {
			t, u, e := reindexFeatureDir(database, htmlgraphDir, projectDir, dir, validIDs, verboseFlag)
			total += t
			upserted += u
			errCount += e
		}

		collectSessionIDs(database, validIDs)
		purged, edgesPurged := purgeStaleEntries(database, validIDs)
		reindexEdges(database, htmlgraphDir, validIDs)
		fixImplementedInEdges(database)
		fmt.Printf("Reindexed: %d upserted, %d errors (of %d HTML files)\n",
			upserted, errCount, total)
		if purged > 0 || edgesPurged > 0 {
			fmt.Printf("Purged: %d stale features, %d stale edges\n", purged, edgesPurged)
		}
	}

	// Rebuild agent_events from session HTML activity logs. projectDir is
	// passed through so parseSessionHTML can attribute sessions whose HTML
	// files predate the data-project-dir attribute (bug-a52d5bf9).
	sessDir := filepath.Join(htmlgraphDir, "sessions")
	sessTotal, sessUpserted, sessErrs := reindexSessions(database, sessDir, projectDir)
	if sessUpserted > 0 || sessErrs > 0 {
		fmt.Printf("  sessions: %d events upserted, %d errors (of %d session files)\n",
			sessUpserted, sessErrs, sessTotal)
	}

	// Parse git commit trailers (Refs:/Fixes:) to backfill feature attribution.
	trailerCount, trailerErr := reindexCommitTrailers(database, projectDir)
	if trailerErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: commit trailer ingestion: %v\n", trailerErr)
	} else if trailerCount > 0 {
		fmt.Printf("  commit trailers: %d feature links from Refs/Fixes trailers\n", trailerCount)
	}

	// Rebuild feature_files from git_commits.
	fileCount, ffErr := reindexFeatureFiles(database, projectDir)
	if ffErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: feature_files rebuild: %v\n", ffErr)
	} else if fileCount > 0 {
		fmt.Printf("  feature_files: %d file associations rebuilt\n", fileCount)
	}

	if currentCommit != "" && errCount == 0 {
		_ = dbpkg.SetMetadata(database, metaKeyLastIndexedCommit, currentCommit)
	}

	return nil
}

// runIncrementalReindex parses only files changed between lastCommit and HEAD.
func runIncrementalReindex(
	database *sql.DB,
	htmlgraphDir, projectDir, lastCommit string,
	validIDs map[string]bool,
	verbose bool,
) (int, int, int) {
	added, deleted := gitChangedFiles(projectDir, lastCommit, htmlgraphDir)

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
			if verbose {
				fmt.Printf("reindex: error: %s: %v\n", path, parseErr)
			}
			continue
		}

		createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)
		createdAt, updatedAt = applyGitTimestamps(projectDir, path, createdAt, updatedAt)

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
				if verbose {
					fmt.Printf("reindex: error: %s: %v\n", path, err)
				}
				continue
			}
		} else {
			desc := node.Content
			if len([]rune(desc)) > 500 {
				desc = string([]rune(desc)[:499]) + "\u2026"
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
				if verbose {
					fmt.Printf("reindex: error: %s: %v\n", path, err)
				}
				continue
			}
		}
		validIDs[node.ID] = true
		upserted++
	}
	return total, upserted, errCount
}

func gitHeadCommit(projectDir string) string {
	out, err := exec.Command("git", "-C", projectDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitCommitExists(projectDir, commit string) bool {
	err := exec.Command("git", "-C", projectDir, "cat-file", "-e", commit+"^{commit}").Run()
	return err == nil
}

func gitChangedFiles(projectDir, fromCommit, htmlgraphDir string) (added []string, deleted []string) {
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
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
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

func idFromHTMLPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".html")
}

func reindexTracks(database *sql.DB, htmlgraphDir, projectDir string, validIDs map[string]bool, verbose bool) (int, int, int) {
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
				if verbose {
					fmt.Printf("reindex: error: %s: %v\n", f, parseErr)
				}
				continue
			}

			createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)
			createdAt, updatedAt = applyGitTimestamps(projectDir, f, createdAt, updatedAt)
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
				if verbose {
					fmt.Printf("reindex: error: %s: %v\n", f, upsertErr)
				}
				continue
			}
			validIDs[node.ID] = true
			upserted++
		}
	}
	return total, upserted, errCount
}

func reindexFeatureDir(database *sql.DB, htmlgraphDir, projectDir, dir string, validIDs map[string]bool, verbose bool) (int, int, int) {
	pattern := filepath.Join(htmlgraphDir, dir, "*.html")
	files, _ := filepath.Glob(pattern)

	var total, upserted, errCount int
	for _, f := range files {
		total++
		node, parseErr := htmlparse.ParseFile(f)
		if parseErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex: error: %s: %v\n", f, parseErr)
			}
			continue
		}

		createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)
		createdAt, updatedAt = applyGitTimestamps(projectDir, f, createdAt, updatedAt)
		desc := node.Content
		if len([]rune(desc)) > 500 {
			desc = string([]rune(desc)[:499]) + "\u2026"
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
			if verbose {
				fmt.Printf("reindex: error: %s: %v\n", f, upsertErr)
			}
			continue
		}
		validIDs[node.ID] = true
		upserted++
	}
	return total, upserted, errCount
}

func collectSessionIDs(database *sql.DB, validIDs map[string]bool) {
	rows, err := database.Query("SELECT session_id FROM sessions")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && id != "" {
			validIDs[id] = true
		}
	}
}

func reindexEdges(database *sql.DB, htmlgraphDir string, validIDs map[string]bool) {
	dirs := []struct {
		subdir   string
		nodeType string
	}{
		{"tracks", "track"},
		{"features", "feature"},
		{"bugs", "bug"},
		{"spikes", "spike"},
	}
	for _, d := range dirs {
		pattern := filepath.Join(htmlgraphDir, d.subdir, "*.html")
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			node, err := htmlparse.ParseFile(f)
			if err != nil || !validIDs[node.ID] {
				continue
			}
			for _, edges := range node.Edges {
				for _, e := range edges {
					if !validIDs[e.TargetID] {
						continue
					}
					edgeID := fmt.Sprintf("%s-%s-%s", node.ID, string(e.Relationship), e.TargetID)
					_ = dbpkg.InsertEdge(
						database,
						edgeID, node.ID, d.nodeType,
						e.TargetID, inferNodeTypeFromID(e.TargetID),
						string(e.Relationship),
						e.Properties,
					)
				}
			}
		}
	}
}

func inferNodeTypeFromID(id string) string {
	switch {
	case len(id) > 5 && id[:5] == "feat-":
		return "feature"
	case len(id) > 4 && id[:4] == "bug-":
		return "bug"
	case len(id) > 4 && id[:4] == "spk-":
		return "spike"
	case len(id) > 4 && id[:4] == "trk-":
		return "track"
	case len(id) > 5 && id[:5] == "plan-":
		return "plan"
	case len(id) > 5 && id[:5] == "spec-":
		return "spec"
	case len(id) > 5 && id[:5] == "sess-":
		return "session"
	default:
		return "unknown"
	}
}

// fixImplementedInEdges corrects implemented_in edges that have to_node_type='unknown'.
// Session IDs are UUIDs (not prefixed), so inferNodeTypeFromID returns 'unknown' by
// default. This function updates all implemented_in edges with unknown target types
// by re-inferring the correct type from the target ID.
func fixImplementedInEdges(database *sql.DB) {
	// Fetch all implemented_in edges with unknown target type.
	rows, err := database.Query(`
		SELECT id, to_node_id FROM graph_edges
		WHERE relationship_type = 'implemented_in' AND to_node_type = 'unknown'
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	var toFix []struct {
		id       string
		toNodeID string
	}
	for rows.Next() {
		var edge struct {
			id       string
			toNodeID string
		}
		if err := rows.Scan(&edge.id, &edge.toNodeID); err != nil {
			continue
		}
		toFix = append(toFix, edge)
	}

	// Update each edge with the correct inferred type.
	for _, edge := range toFix {
		correctType := inferNodeTypeFromID(edge.toNodeID)
		_, _ = database.Exec(
			`UPDATE graph_edges SET to_node_type = ? WHERE id = ?`,
			correctType, edge.id,
		)
	}
}

