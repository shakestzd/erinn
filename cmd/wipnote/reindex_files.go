package main

import (
	"bufio"
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

	// Resolve every distinct commit exactly once, no matter how many
	// features reference it, and in a single git subprocess rather than one
	// per commit (bug-1f338b5b: the old per-commit-per-feature spawn loop
	// dominated a full `wipnote reindex` at ~70 minutes on this repo).
	seenHash := make(map[string]bool, len(refs))
	uniqueHashes := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.commitHash == "" || seenHash[ref.commitHash] {
			continue
		}
		seenHash[ref.commitHash] = true
		uniqueHashes = append(uniqueHashes, ref.commitHash)
	}
	filesByCommit := commitFilesByHash(projectDir, uniqueHashes)

	total := 0
	for _, ref := range refs {
		for _, filePath := range filesByCommit[ref.commitHash] {
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

// expandCommitFiles runs git diff-tree on the provided commit hashes and
// returns the deduplicated, repo-relative file paths touched by those
// commits, in the order the hashes and their files were encountered. Hashes
// that do not exist locally (rebased away, shallow clone) are silently
// skipped. An empty slice is returned when no files can be resolved.
func expandCommitFiles(projectDir string, hashes []string) []string {
	filesByCommit := commitFilesByHash(projectDir, hashes)
	seen := make(map[string]bool)
	var files []string
	for _, hash := range hashes {
		for _, fp := range filesByCommit[hash] {
			if seen[fp] {
				continue
			}
			seen[fp] = true
			files = append(files, fp)
		}
	}
	return files
}

// commitFilesByHash resolves the repo-relative files touched by each of the
// given commit hashes using a single `git diff-tree --stdin` invocation
// instead of one subprocess per commit. Duplicate hashes are only diffed
// once. This is the batched replacement for looping `git diff-tree --root
// --no-commit-id -r --name-only <hash>` once per hash (bug-1f338b5b).
//
// --stdin mode preserves the exact per-commit semantics of the old
// one-hash-per-invocation form: merge commits (multiple parents) produce no
// output unless -m/-c/--cc is passed (unchanged: neither is passed here),
// --root still diffs a parentless commit against the empty tree, and a hash
// that doesn't resolve to a local commit object is silently omitted from the
// output rather than aborting the batch -- verified empirically against this
// repo's git (a bad hash mixed into a --stdin batch produces zero lines for
// that hash and exit code 0, matching the old code's per-hash `continue` on
// error). Each commit's own file list is deduplicated, matching what a
// single-hash call would have produced.
func commitFilesByHash(projectDir string, hashes []string) map[string][]string {
	hashSet := make(map[string]bool, len(hashes))
	uniqueHashes := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if h == "" || hashSet[h] {
			continue
		}
		hashSet[h] = true
		uniqueHashes = append(uniqueHashes, h)
	}
	if len(uniqueHashes) == 0 {
		return nil
	}

	cmd := exec.Command(
		"git", "-C", projectDir,
		"diff-tree", "--stdin", "--root", "-r", "--name-only",
	)
	cmd.Stdin = strings.NewReader(strings.Join(uniqueHashes, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		// The whole batch failed (e.g. projectDir is not a git repo) -- the
		// old code would have failed identically on every hash and skipped
		// all of them, so an empty result here matches.
		return nil
	}

	result := make(map[string][]string, len(uniqueHashes))
	var current string
	var currentSeen map[string]bool
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// diff-tree --stdin echoes the requested commit hash verbatim as a
		// header line before that commit's changed files (empirically
		// verified: it reproduces the exact string fed in, not a
		// canonicalized/expanded form). Since we control that input set
		// exactly, an output line matching one of our requested hashes is
		// unambiguously a header, not a changed file.
		if hashSet[line] {
			current = line
			currentSeen = make(map[string]bool)
			continue
		}
		if current == "" || currentSeen[line] {
			continue
		}
		currentSeen[line] = true
		result[current] = append(result[current], line)
	}
	return result
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
