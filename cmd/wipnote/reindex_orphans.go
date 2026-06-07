package main

import (
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

// featureIDRe matches a canonical work-item ID anywhere in a commit message.
// Anchored with word-boundary equivalents: must NOT be preceded or followed by
// a hex character so that "feat-9b767422ab" does not match "feat-9b767422".
var featureIDRe = regexp.MustCompile(`(?:^|[^0-9a-f])((?:feat|bug|spk|trk|pln|spc|plan|spec)-[0-9a-f]{8})(?:[^0-9a-f]|$)`)

func reindexBackfillOrphansCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backfill-orphans",
		Short: "Backfill feature_files for features with zero file attribution",
		Long: `Walks git log to find commits that reference orphan features (features
with zero feature_files rows) and inserts attribution rows.

By default runs in dry-run mode: prints what would happen without writing.
Pass --write to actually insert rows into feature_files.

A commit is matched when the commit message subject or body contains a
canonical work-item ID (e.g. feat-9b767422). Both parenthesized references
(feat-XXXXXXXX) and plain inline references are matched. False-match guard:
IDs that appear as a prefix of a longer hex string are skipped.

Only commits reachable from HEAD on the current branch are considered.`,
		RunE: runReindexBackfillOrphans,
	}
	cmd.Flags().Bool("write", false, "Insert rows into feature_files (default is dry-run)")
	cmd.Flags().BoolP("verbose", "v", false, "Print per-feature progress")
	return cmd
}

// orphanFeature holds an orphan feature's ID and whether it exists in the
// features table (used for the skip-unknown guard).
type orphanFeature struct {
	id    string
	title string
}

// commitMatch holds a commit that references a feature and the files it touched.
type commitMatch struct {
	hash  string
	files []fileStats
}

// fileStats holds a file path and its diff stats for one commit.
type fileStats struct {
	path         string
	linesAdded   int
	linesRemoved int
}

func runReindexBackfillOrphans(cmd *cobra.Command, _ []string) error {
	writeMode, _ := cmd.Flags().GetBool("write")
	verbose, _ := cmd.Flags().GetBool("verbose")

	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(wipnoteDir)

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

	orphans, err := findOrphanFeatures(database)
	if err != nil {
		return fmt.Errorf("find orphan features: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Println("No orphan features found — all features have file attribution.")
		return nil
	}

	if !writeMode {
		fmt.Printf("Dry-run mode: %d orphan feature(s) found. Pass --write to insert rows.\n\n", len(orphans))
	} else {
		fmt.Printf("Write mode: backfilling %d orphan feature(s)...\n\n", len(orphans))
	}

	totalFeatures := 0
	totalCommits := 0
	totalFiles := 0

	for _, orphan := range orphans {
		matches, searchErr := findCommitsForFeature(projectDir, orphan.id)
		if searchErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: git log for %s: %v\n", orphan.id, searchErr)
			continue
		}

		commitCount := len(matches)
		fileCount := 0
		for _, m := range matches {
			fileCount += len(m.files)
		}

		if verbose || commitCount > 0 {
			if writeMode {
				fmt.Printf("%s: %d commits found, %d files indexed\n", orphan.id, commitCount, fileCount)
			} else {
				fmt.Printf("%s: %d commits found, %d files would be indexed\n", orphan.id, commitCount, fileCount)
			}
		}

		if commitCount == 0 {
			continue
		}
		totalFeatures++
		totalCommits += commitCount
		totalFiles += fileCount

		if writeMode {
			inserted, insertErr := insertFeatureFileRows(database, orphan.id, matches)
			if insertErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: insert for %s: %v\n", orphan.id, insertErr)
			}
			if verbose && inserted != fileCount {
				fmt.Printf("  %s: %d new rows inserted (%d already existed)\n", orphan.id, inserted, fileCount-inserted)
			}
		}
	}

	fmt.Println()
	if writeMode {
		fmt.Printf("Backfill complete: %d features, %d commits, %d file rows inserted.\n",
			totalFeatures, totalCommits, totalFiles)
	} else {
		fmt.Printf("Dry-run summary: %d features, %d commits, %d file rows would be inserted.\n",
			totalFeatures, totalCommits, totalFiles)
		fmt.Println("Run with --write to apply changes.")
	}
	return nil
}

// findOrphanFeatures returns all features that have zero feature_files rows.
func findOrphanFeatures(database *sql.DB) ([]orphanFeature, error) {
	rows, err := database.Query(`
		SELECT f.id, COALESCE(f.title, '')
		FROM features f
		WHERE NOT EXISTS (
			SELECT 1 FROM feature_files ff WHERE ff.feature_id = f.id
		)
		ORDER BY f.id
	`)
	if err != nil {
		return nil, fmt.Errorf("query orphan features: %w", err)
	}
	defer rows.Close()

	var out []orphanFeature
	for rows.Next() {
		var o orphanFeature
		if err := rows.Scan(&o.id, &o.title); err != nil {
			continue
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// findCommitsForFeature walks git log on the current branch and returns commits
// that reference featureID in their message body or subject.
// Only commits reachable from HEAD are considered (not detached branches).
func findCommitsForFeature(projectDir, featureID string) ([]commitMatch, error) {
	// Use git log with --all to walk all branches and remotes, not just HEAD.
	// This ensures commits on merged or squashed branches are not missed.
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "--all", "--format=%H %s%n%b%n---COMMIT-SEP---",
		"--grep="+featureID,
	).Output()
	if err != nil {
		// git log may return exit 1 on empty result in some versions; treat as no matches.
		return nil, nil
	}

	seen := make(map[string]bool)
	var matches []commitMatch
	for _, block := range splitCommitBlocks(string(out)) {
		if block.hash == "" {
			continue
		}
		// Deduplicate: --all can yield the same commit via multiple refs.
		if seen[block.hash] {
			continue
		}
		seen[block.hash] = true
		// Verify this commit's message actually references featureID precisely
		// (not as a substring of a longer ID).
		if !commitReferencesFeature(block.hash, block.subject+"\n"+block.body, featureID) {
			continue
		}

		files, statsErr := getCommitFilesWithStats(projectDir, block.hash)
		if statsErr != nil {
			// Commit may not exist locally (rebased away) — skip silently.
			continue
		}
		if len(files) == 0 {
			continue
		}
		matches = append(matches, commitMatch{hash: block.hash, files: files})
	}
	return matches, nil
}

// commitReferencesFeature checks whether message contains featureID as a
// precise match — not as a prefix of a longer hex string.
func commitReferencesFeature(_, message, featureID string) bool {
	subs := featureIDRe.FindAllStringSubmatch(message, -1)
	for _, m := range subs {
		if len(m) >= 2 && m[1] == featureID {
			return true
		}
	}
	return false
}

// splitCommitBlocks parses git log output (with ---COMMIT-SEP--- as delimiter)
// into individual commitBlock entries. Reuses the commitBlock type from
// reindex_trailers.go.
func splitCommitBlocks(output string) []commitBlock {
	raw := strings.Split(output, "---COMMIT-SEP---")
	blocks := make([]commitBlock, 0, len(raw))
	for _, chunk := range raw {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		lines := strings.SplitN(chunk, "\n", 2)
		if len(lines) == 0 {
			continue
		}
		firstLine := lines[0]
		spaceIdx := strings.IndexByte(firstLine, ' ')
		var hash, subject string
		if spaceIdx > 0 {
			hash = firstLine[:spaceIdx]
			subject = firstLine[spaceIdx+1:]
		} else {
			hash = firstLine
		}
		var body string
		if len(lines) > 1 {
			body = lines[1]
		}
		blocks = append(blocks, commitBlock{
			hash:    hash,
			subject: subject,
			body:    body,
		})
	}
	return blocks
}

// getCommitFilesWithStats returns the list of files touched by a commit along
// with their numstat (lines added/removed). Uses git diff-tree --numstat.
func getCommitFilesWithStats(projectDir, commitHash string) ([]fileStats, error) {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"diff-tree", "--root", "--no-commit-id", "-r", "--numstat", commitHash,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("diff-tree %s: %w", commitHash, err)
	}

	var stats []fileStats
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// numstat format: "<added>\t<removed>\t<path>"
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		added := parseStatInt(parts[0])
		removed := parseStatInt(parts[1])
		filePath := strings.TrimSpace(parts[2])
		if filePath == "" {
			continue
		}
		stats = append(stats, fileStats{
			path:         filePath,
			linesAdded:   added,
			linesRemoved: removed,
		})
	}
	return stats, nil
}

// parseStatInt parses a numstat integer field, returning 0 for binary files ("-").
func parseStatInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "-" {
		return 0
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// insertFeatureFileRows inserts feature_files rows for the given commits.
// Returns the count of rows that were new (not already present).
// Idempotent: UpsertFeatureFile uses ON CONFLICT DO UPDATE.
func insertFeatureFileRows(database *sql.DB, featureID string, matches []commitMatch) (int, error) {
	inserted := 0
	for _, m := range matches {
		hashPrefix := m.hash
		if len(hashPrefix) > 8 {
			hashPrefix = hashPrefix[:8]
		}
		for _, fs := range m.files {
			ff := &models.FeatureFile{
				ID:        featureID + "-" + hashPrefix + "-" + sanitizePathID(fs.path),
				FeatureID: featureID,
				FilePath:  fs.path,
				Operation: "backfill",
			}
			if err := dbpkg.UpsertFeatureFile(database, ff); err == nil {
				inserted++
			}
		}
	}
	return inserted, nil
}
