package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// featureIDRe matches a canonical work-item ID anywhere in a commit message.
// Anchored with word-boundary equivalents: must NOT be preceded or followed by
// a hex character so that "feat-9b767422ab" does not match "feat-9b767422".
var featureIDRe = regexp.MustCompile(`(?:^|[^0-9a-f])((?:feat|bug|spk|trk|pln|spc|plan|spec)-[0-9a-f]{8})(?:[^0-9a-f]|$)`)

func reindexBackfillOrphansCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backfill-orphans",
		Short: "Backfill affected_files for work items with no file attribution",
		Long: `Walks git log to find commits that reference orphan work items (features,
bugs, and spikes whose canonical HTML carries no affected_files property) and
records the files those commits touched.

By default runs in dry-run mode: prints what would happen without writing.
Pass --write to set the affected_files property on each item's HTML.

A commit is matched when the commit message subject or body contains a
canonical work-item ID (e.g. feat-9b767422). Both parenthesized references
(feat-XXXXXXXX) and plain inline references are matched. False-match guard:
IDs that appear as a prefix of a longer hex string are skipped.

Attribution is derived entirely from git and written to the canonical store:
both the input (which items are orphans) and the output (affected_files) live
in .wipnote/ HTML, so a backfill survives the process that produced it.`,
		RunE: runReindexBackfillOrphans,
	}
	cmd.Flags().Bool("write", false, "Set affected_files on the work item HTML (default is dry-run)")
	cmd.Flags().BoolP("verbose", "v", false, "Print per-item progress")
	return cmd
}

// orphanFeature holds an orphan work item's ID and title.
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

	orphans, err := findOrphanWorkItems(wipnoteDir)
	if err != nil {
		return fmt.Errorf("find orphan work items: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Println("No orphan work items found — every item already has file attribution.")
		return nil
	}

	if !writeMode {
		fmt.Printf("Dry-run mode: %d orphan work item(s) found. Pass --write to record attribution.\n\n", len(orphans))
	} else {
		fmt.Printf("Write mode: backfilling %d orphan work item(s)...\n\n", len(orphans))
	}

	var project *workitem.Project
	if writeMode {
		project, err = workitem.Open(wipnoteDir, "claude-code")
		if err != nil {
			return fmt.Errorf("open project: %w", err)
		}
		defer project.Close()
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
		files := distinctFilesFromMatches(matches)
		fileCount := len(files)

		if verbose || commitCount > 0 {
			if writeMode {
				fmt.Printf("%s: %d commits found, %d files attributed\n", orphan.id, commitCount, fileCount)
			} else {
				fmt.Printf("%s: %d commits found, %d files would be attributed\n", orphan.id, commitCount, fileCount)
			}
		}

		if commitCount == 0 || fileCount == 0 {
			continue
		}
		totalFeatures++
		totalCommits += commitCount
		totalFiles += fileCount

		if writeMode {
			if saveErr := setAffectedFiles(project, orphan.id, files); saveErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: write attribution for %s: %v\n", orphan.id, saveErr)
			}
		}
	}

	fmt.Println()
	if writeMode {
		fmt.Printf("Backfill complete: %d work items, %d commits, %d files attributed.\n",
			totalFeatures, totalCommits, totalFiles)
	} else {
		fmt.Printf("Dry-run summary: %d work items, %d commits, %d files would be attributed.\n",
			totalFeatures, totalCommits, totalFiles)
		fmt.Println("Run with --write to apply changes.")
	}
	return nil
}

// findOrphanWorkItems returns every feature, bug, and spike in the canonical
// store whose HTML carries no non-empty affected_files property.
//
// The orphan set used to come from a SQL anti-join against feature_files. That
// table is derived and no longer persisted, so the question is now asked of the
// canonical artifacts directly — which is also the only place the answer stays
// true between runs.
func findOrphanWorkItems(wipnoteDir string) ([]orphanFeature, error) {
	nodes, err := graph.LoadAll(wipnoteDir)
	if err != nil {
		return nil, fmt.Errorf("load work items: %w", err)
	}
	var out []orphanFeature
	for _, n := range nodes {
		switch n.Type {
		case "feature", "bug", "spike":
		default:
			continue
		}
		if strings.TrimSpace(nodeAffectedFiles(n)) != "" {
			continue
		}
		out = append(out, orphanFeature{id: n.ID, title: n.Title})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

// nodeAffectedFiles returns a node's affected_files property as a string, or ""
// when it is absent or not a string. Properties is map[string]any, so a
// hand-edited or JSON-escaped value of another type must not panic here.
func nodeAffectedFiles(n *models.Node) string {
	if n == nil {
		return ""
	}
	s, _ := n.Properties[affectedFilesProp].(string)
	return s
}

// affectedFilesProp is the canonical node property holding the comma-separated
// list of files a work item touched. `wipnote <type> create --files` writes it;
// this backfill fills it in for items created before that flag was used.
const affectedFilesProp = "affected_files"

// distinctFilesFromMatches flattens the per-commit file lists into one
// deduplicated, sorted slice. A file touched by three of an item's commits is
// one entry in its attribution, not three.
func distinctFilesFromMatches(matches []commitMatch) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		for _, fs := range m.files {
			if fs.path == "" || seen[fs.path] {
				continue
			}
			seen[fs.path] = true
			out = append(out, fs.path)
		}
	}
	sort.Strings(out)
	return out
}

// setAffectedFiles writes the derived file list onto the work item's canonical
// HTML as its affected_files property.
func setAffectedFiles(project *workitem.Project, itemID string, files []string) error {
	col := editCollectionForID(project, itemID)
	if col == nil {
		return fmt.Errorf("cannot determine collection for %s", itemID)
	}
	return col.Edit(itemID).SetProperty(affectedFilesProp, strings.Join(files, ",")).Save()
}

// editCollectionForID maps a work-item ID to the collection that owns its file.
// Only the three types this backfill covers are mapped; anything else returns
// nil so the caller reports rather than writes to the wrong place.
func editCollectionForID(project *workitem.Project, id string) *workitem.Collection {
	if project == nil {
		return nil
	}
	switch {
	case strings.HasPrefix(id, "feat-"):
		return project.Features.Collection
	case strings.HasPrefix(id, "bug-"):
		return project.Bugs.Collection
	case strings.HasPrefix(id, "spk-"):
		return project.Spikes.Collection
	default:
		return nil
	}
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

