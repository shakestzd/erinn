package main

import (
	"database/sql"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

const trailerSessionID = "trailer-ingest"

// metaKeyLastTrailerScanCommit is the high-water mark for commit-trailer
// ingestion, distinct from metaKeyLastIndexedCommit (which tracks HTML
// work-item files). Trailer scanning walks the *entire* commit graph rather
// than a wipnote-dir-scoped diff, so it needs its own bookmark.
const metaKeyLastTrailerScanCommit = "last_trailer_scan_commit"

// reindexCommitTrailers walks git log and parses Refs:/Fixes: trailers to
// populate git_commits.feature_id for commits made outside of Claude Code
// sessions. Returns the count of new rows inserted.
//
// Commit history is immutable, so once a commit has been scanned its trailers
// never change. The first run (no bookmark yet, or the bookmarked commit was
// rewritten/GC'd away) does a one-time full-history scan; every run after
// that only scans commits added since the last scan (see bug-9577013c — a
// flat "-500" cap silently held linkage at 27% forever because it never
// looked further back).
func reindexCommitTrailers(database *sql.DB, projectDir string) (int, error) {
	currentCommit := gitHeadCommit(projectDir)

	logRange := ""
	if lastScanned, _ := dbpkg.GetMetadata(database, metaKeyLastTrailerScanCommit); lastScanned != "" && gitCommitExists(projectDir, lastScanned) {
		logRange = lastScanned + "..HEAD"
	}

	args := []string{"-C", projectDir, "log", "--format=%H %s%n%b%n---TRAILER-SEP---"}
	if logRange != "" {
		args = append(args, logRange)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return 0, fmt.Errorf("git log: %w", err)
	}

	total := 0
	for _, block := range splitTrailerBlocks(string(out)) {
		if block.hash == "" {
			continue
		}
		ids := parseTrailers(block.body)
		if len(ids) == 0 {
			continue
		}
		for _, featureID := range ids {
			result, insertErr := database.Exec(`
				INSERT OR IGNORE INTO git_commits
					(commit_hash, session_id, feature_id, message, timestamp)
				VALUES (?, ?, ?, ?, ?)`,
				block.hash, trailerSessionID, featureID,
				block.subject, time.Now().UTC().Format(time.RFC3339),
			)
			if insertErr == nil {
				if n, _ := result.RowsAffected(); n > 0 {
					total++
				}
			}
		}
	}

	// Advance the bookmark so the next run only scans new commits. Skipped
	// when HEAD is unresolvable so a transient git failure doesn't silently
	// mark unscanned history as scanned.
	if currentCommit != "" {
		_ = dbpkg.SetMetadata(database, metaKeyLastTrailerScanCommit, currentCommit)
	}
	return total, nil
}

type commitBlock struct {
	hash    string
	subject string
	body    string
}

// splitTrailerBlocks parses the git log output into individual commit blocks.
func splitTrailerBlocks(output string) []commitBlock {
	raw := strings.Split(output, "---TRAILER-SEP---")
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
		// First line: "<hash> <subject>"
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
			body:    firstLine + "\n" + body,
		})
	}
	return blocks
}

// parenWorkItemRe matches parenthesized work item references in commit messages,
// e.g. "(feat-abc12345)". This is the primary wipnote commit convention.
var parenWorkItemRe = regexp.MustCompile(`\(\s*((?:feat|bug|spk|trk|pln|spc|plan|spec)-[0-9a-f]{8})\s*\)`)

// parseTrailers extracts work item IDs from a git commit message.
// Supported formats:
//
//	Refs: feat-abc123
//	Fixes: bug-def456
//	Refs: feat-abc123, feat-def456
//	fix: resolve crash (feat-abc12345)     — parenthesized convention
func parseTrailers(message string) []string {
	var ids []string
	seen := make(map[string]bool)

	// Parenthesized work item refs — the primary wipnote convention.
	for _, m := range parenWorkItemRe.FindAllStringSubmatch(message, -1) {
		id := m[1]
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}

	// Explicit Refs:/Fixes: trailers.
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"Refs:", "Fixes:"} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			rest := strings.TrimPrefix(line, prefix)
			for _, part := range strings.Split(rest, ",") {
				id := strings.TrimSpace(part)
				if id == "" || seen[id] {
					continue
				}
				if isWorkItemID(id) {
					ids = append(ids, id)
					seen[id] = true
				}
			}
		}
	}
	return ids
}

// isWorkItemID returns true if s looks like a valid work item ID prefix.
func isWorkItemID(s string) bool {
	for _, prefix := range []string{"feat-", "bug-", "spk-", "trk-", "pln-", "spc-", "plan-", "spec-"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
