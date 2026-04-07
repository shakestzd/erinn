package hooks

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// looksLikeGitMerge returns true when the bash command is a git merge.
// Excludes quoted occurrences (e.g. echo "git merge") and non-merge subcommands.
func looksLikeGitMerge(cmd string) bool {
	return gitMergeRe.MatchString(cmd)
}

// gitMergeRe matches "git merge" at the start of a command or after && / ; / |.
// Avoids matching inside quoted strings.
var gitMergeRe = regexp.MustCompile(`(?:^|&&|;|\|)\s*git\s+merge\b`)

// extractMergeBranch extracts the branch name from a git merge command.
// Returns "" if no branch name can be determined (e.g. --abort, --continue).
func extractMergeBranch(cmd string) string {
	m := mergeBranchRe.FindStringSubmatch(cmd)
	if len(m) < 2 {
		return ""
	}
	branch := m[1]
	// Filter out merge sub-operations that aren't branch names.
	switch branch {
	case "--abort", "--continue", "--quit":
		return ""
	}
	return branch
}

// mergeBranchRe captures the first non-flag argument after "git merge".
// Flags start with "-"; the branch name is the first arg that doesn't.
var mergeBranchRe = regexp.MustCompile(
	`git\s+merge` + // git merge
		`(?:\s+--?[\w-]+)*` + // skip flags like --no-ff, --no-commit
		`\s+([\w][\w./-]*)`, // capture branch name
)

// branchItemRe matches work item IDs (feat-, bug-, spk-, trk-) with 8-char hex suffix
// anywhere in a string.
var branchItemRe = regexp.MustCompile(`((?:feat|bug|spk|trk)-[0-9a-f]{8})`)

// extractBranchItemIDs extracts work item IDs from a branch name.
// Recognises patterns like yolo-feat-xxx, trk-xxx, agent-trk-xxx-task1.
func extractBranchItemIDs(branch string) []string {
	if branch == "" {
		return nil
	}
	matches := branchItemRe.FindAllStringSubmatch(branch, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, m := range matches {
		id := m[1]
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// checkMergeCompleteness checks whether work items associated with a merged
// branch are all completed. Returns a warning string if any are still
// in-progress, or "" if everything is clean.
func checkMergeCompleteness(cmd string, database *sql.DB) string {
	branch := extractMergeBranch(cmd)
	if branch == "" {
		return ""
	}
	ids := extractBranchItemIDs(branch)
	if len(ids) == 0 {
		return ""
	}

	var incomplete []string
	for _, id := range ids {
		if strings.HasPrefix(id, "trk-") {
			// For tracks, check all features on the track.
			incomplete = append(incomplete, inProgressOnTrack(database, id)...)
		} else {
			// For individual items, check the item directly.
			if isItemInProgress(database, id) {
				incomplete = append(incomplete, id)
			}
		}
	}

	if len(incomplete) == 0 {
		return ""
	}

	return fmt.Sprintf(
		"MERGE WARNING: %d work item(s) still in-progress after merging branch %q: %s. "+
			"Run `htmlgraph feature complete <id>` (or bug/spike) for each, or verify they were intentionally left open.",
		len(incomplete), branch, strings.Join(incomplete, ", "),
	)
}

// isItemInProgress returns true if the work item exists and has status "in-progress".
func isItemInProgress(database *sql.DB, id string) bool {
	var status string
	err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, id).Scan(&status)
	return err == nil && status == "in-progress"
}

// inProgressOnTrack returns IDs of in-progress features on the given track.
func inProgressOnTrack(database *sql.DB, trackID string) []string {
	rows, err := database.Query(
		`SELECT id FROM features WHERE track_id = ? AND status = 'in-progress'`,
		trackID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
