package main

import (
	"bytes"
	"os/exec"
	"strings"
	"time"
)

// gitFileTimestamps returns (created, updated) for a file from git history.
//
//   - created = timestamp of the first commit that added the file
//     (via git log --diff-filter=A --follow, oldest entry wins).
//   - updated = timestamp of the most recent commit touching the file
//     (via git log -1).
//
// Falls back to (zero, zero) when git is unavailable or the file is untracked.
// Callers should use the HTML-attribute timestamps as a fallback when both
// returned values are zero.
func gitFileTimestamps(projectDir, filePath string) (created, updated time.Time, err error) {
	updated, err = gitLastModified(projectDir, filePath)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	created, err = gitFirstAdded(projectDir, filePath)
	if err != nil {
		// If we can't get the creation time, still return updated.
		return time.Time{}, updated, err
	}

	// If file has only one commit, created == updated, which is correct.
	if created.IsZero() {
		created = updated
	}

	return created, updated, nil
}

// gitLastModified returns the author timestamp of the most recent commit
// that touched filePath. Returns zero time when the file is untracked.
func gitLastModified(projectDir, filePath string) (time.Time, error) {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "-1", "--format=%aI", "--", filePath,
	).Output()
	if err != nil {
		return time.Time{}, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return time.Time{}, nil // untracked
	}
	return parseGitTimestamp(line)
}

// gitFirstAdded returns the author timestamp of the oldest commit that
// introduced filePath (following renames via --follow --diff-filter=A).
// Returns zero time when the file is untracked.
func gitFirstAdded(projectDir, filePath string) (time.Time, error) {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "--diff-filter=A", "--follow", "--format=%aI", "--", filePath,
	).Output()
	if err != nil {
		return time.Time{}, err
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return time.Time{}, nil // untracked
	}

	// git log outputs newest-first; we want the oldest (last line).
	lines := bytes.Split([]byte(raw), []byte("\n"))
	last := strings.TrimSpace(string(lines[len(lines)-1]))
	if last == "" {
		last = strings.TrimSpace(string(lines[0]))
	}
	return parseGitTimestamp(last)
}

// parseGitTimestamp parses an ISO 8601 timestamp produced by git --format=%aI.
func parseGitTimestamp(s string) (time.Time, error) {
	// git %aI produces RFC3339 with timezone offset e.g. "2024-01-15T10:30:00+05:30"
	return time.Parse(time.RFC3339, s)
}

// applyGitTimestamps overrides node timestamps with git history when available.
// If git has no record of the file (untracked/not committed), the provided
// htmlCreated and htmlUpdated values are returned unchanged.
//
// This is the primary integration point between git history and reindex.
func applyGitTimestamps(
	projectDir, filePath string,
	htmlCreated, htmlUpdated time.Time,
) (created, updated time.Time) {
	gitCreated, gitUpdated, err := gitFileTimestamps(projectDir, filePath)
	if err != nil || (gitCreated.IsZero() && gitUpdated.IsZero()) {
		// git unavailable or file is untracked — use HTML attributes as-is.
		return htmlCreated, htmlUpdated
	}

	created = gitCreated
	updated = gitUpdated

	// Sanity: if git only returned updated (no --diff-filter=A hit), fall back
	// to HTML created or updated as the creation timestamp.
	if created.IsZero() {
		if !htmlCreated.IsZero() {
			created = htmlCreated
		} else {
			created = updated
		}
	}

	return created, updated
}
