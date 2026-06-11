package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// reindexFeatureFiles rebuilds the feature_files table from git_commits.
// For each feature with linked commits, runs git diff-tree to get the files
// touched by each commit and upserts them into feature_files.
// This captures ALL files touched by a feature -- including manual commits,
// other agents, and historical work -- without relying on the hook hot path.
// Returns the total number of file associations upserted.
func reindexFeatureFiles(database *sql.DB, projectDir string) (int, error) {
	rows, err := database.Query(`
		SELECT DISTINCT feature_id, commit_hash
		FROM git_commits
		WHERE feature_id IS NOT NULL AND feature_id != ''
	`)
	if err != nil {
		return 0, fmt.Errorf("query git_commits: %w", err)
	}
	defer rows.Close()

	type commitRef struct {
		featureID  string
		commitHash string
	}
	var refs []commitRef
	for rows.Next() {
		var r commitRef
		if scanErr := rows.Scan(&r.featureID, &r.commitHash); scanErr != nil {
			continue
		}
		refs = append(refs, r)
	}
	if rowErr := rows.Err(); rowErr != nil {
		return 0, fmt.Errorf("scan git_commits: %w", rowErr)
	}

	total := 0
	for _, ref := range refs {
		for _, filePath := range expandCommitFiles(projectDir, []string{ref.commitHash}) {
			hashPrefix := ref.commitHash
			if len(hashPrefix) > 8 {
				hashPrefix = hashPrefix[:8]
			}
			ff := &models.FeatureFile{
				ID:        ref.featureID + "-" + hashPrefix + "-" + sanitizePathID(filePath),
				FeatureID: ref.featureID,
				FilePath:  filePath,
				Operation: "commit",
			}
			if upsertErr := dbpkg.UpsertFeatureFile(database, ff); upsertErr == nil {
				total++
			}
		}
	}
	return total, nil
}

// expandCommitFiles runs git diff-tree on each provided commit hash and returns
// the deduplicated, repo-relative file paths touched by those commits.
// Hashes that do not exist locally (rebased away, shallow clone) are silently
// skipped. An empty slice is returned when no files can be resolved.
func expandCommitFiles(projectDir string, hashes []string) []string {
	seen := make(map[string]bool)
	var files []string
	for _, hash := range hashes {
		if hash == "" {
			continue
		}
		out, err := exec.Command(
			"git", "-C", projectDir,
			"diff-tree", "--root", "--no-commit-id", "-r", "--name-only", hash,
		).Output()
		if err != nil {
			// Commit may not exist locally (rebased away) -- skip silently.
			continue
		}
		for _, fp := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if fp == "" || seen[fp] {
				continue
			}
			seen[fp] = true
			files = append(files, fp)
		}
	}
	return files
}

// sanitizePathID converts a file path to a short token safe for use in a
// composite primary key (replaces separators and dots, truncates to 32 chars).
// When truncation is required, an 8-char hex suffix derived from the original
// path is appended to prevent collisions between paths with identical prefixes.
func sanitizePathID(filePath string) string {
	r := strings.NewReplacer("/", "-", ".", "-", " ", "-")
	s := r.Replace(filePath)
	if len(s) >= 32 {
		h := sha256.Sum256([]byte(filePath))
		s = s[:24] + fmt.Sprintf("%x", h[:4]) // 24 chars + 8 hex = 32 total
	}
	return s
}
