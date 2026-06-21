package recap

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// EmptyTreeSHA is the SHA of git's well-known empty tree object. It is used as
// the exclusive lower bound of a range that must cover all commits from the
// very beginning of the repository (root-commit case). It is valid for git diff
// but NOT for git log (tree object, not a commit), so callers that need to list
// commits from the beginning must use commitsToHead instead.
const EmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// IsRootRange reports whether gitRange uses the empty-tree lower bound,
// indicating "all commits from the very beginning through HEAD".
func IsRootRange(gitRange string) bool {
	return strings.HasPrefix(gitRange, EmptyTreeSHA+"..") || gitRange == EmptyTreeSHA
}

// commitExists reports whether the given revision resolves to a commit object in
// the repository at projectDir.
func commitExists(projectDir, rev string) bool {
	return exec.Command("git", "-C", projectDir, "cat-file", "-e", rev+"^{commit}").Run() == nil
}

// rootRangeUpper returns the revision to log for an empty-tree-based range.
// Git log cannot accept the empty tree as a lower endpoint, so
// EmptyTreeSHA..rev is equivalent to listing every commit reachable from rev.
func rootRangeUpper(gitRange string) string {
	if gitRange == EmptyTreeSHA {
		return "HEAD"
	}
	if strings.HasPrefix(gitRange, EmptyTreeSHA+"..") {
		upper := strings.TrimSpace(strings.TrimPrefix(gitRange, EmptyTreeSHA+".."))
		if upper != "" {
			return upper
		}
	}
	return "HEAD"
}

// commitsToRevision lists every commit reachable from rev (newest first).
// Used for the root-commit case where the lower bound of the range is the
// empty-tree object (not a commit), which git log cannot accept.
func commitsToRevision(projectDir, rev string) ([]Commit, error) {
	if strings.TrimSpace(rev) == "" {
		rev = "HEAD"
	}
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "--no-color", "--format=%H%x1f%s%x1f%cI", rev,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("recap: git log %s: %w", rev, err)
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 3 {
			continue
		}
		commits = append(commits, Commit{
			Hash:      parts[0],
			Message:   parts[1],
			Timestamp: parts[2],
		})
	}
	return commits, nil
}

// commitsInRange lists the commits contained in a git range (newest first),
// returning lightweight Commit entries populated from git metadata. The DB read
// index is not consulted here — ranges are not necessarily work-item grounded.
func commitsInRange(projectDir, gitRange string) ([]Commit, error) {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "--no-color", "--format=%H%x1f%s%x1f%cI", gitRange,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("recap: git log %s: %w", gitRange, err)
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 3 {
			continue
		}
		commits = append(commits, Commit{
			Hash:      parts[0],
			Message:   parts[1],
			Timestamp: parts[2],
		})
	}
	return commits, nil
}

// diffRange runs `git diff --unified=3` over a range, parses the unified diff,
// and returns a deterministic, path-sorted set of FileChange values with hunks.
func diffRange(projectDir, gitRange string) ([]FileChange, error) {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"diff", "--unified=3", "--no-color", gitRange,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("recap: git diff %s: %w", gitRange, err)
	}
	files := parseUnifiedDiff(string(out))
	sortFiles(files)
	return files, nil
}

// diffCommits diffs each selected commit on its own via git show. This avoids
// treating non-contiguous selected commits as one continuous oldest^..newest
// range and handles root commits without requiring a parent revision.
func diffCommits(projectDir string, hashes []string) ([]FileChange, error) {
	byPath := make(map[string]*FileChange)
	var order []string
	for _, hash := range hashes {
		out, err := exec.Command(
			"git", "-C", projectDir,
			"show", "--format=", "--unified=3", "--no-color", hash,
		).Output()
		if err != nil {
			return nil, fmt.Errorf("recap: git show %s: %w", hash, err)
		}
		for _, fc := range parseUnifiedDiff(string(out)) {
			existing := byPath[fc.Path]
			if existing == nil {
				copy := fc
				byPath[fc.Path] = &copy
				order = append(order, fc.Path)
				continue
			}
			existing.Change = fc.Change
			existing.Hunks = append(existing.Hunks, fc.Hunks...)
		}
	}
	files := make([]FileChange, 0, len(order))
	for _, path := range order {
		files = append(files, *byPath[path])
	}
	sortFiles(files)
	return files, nil
}

// parseUnifiedDiff parses `git diff --unified` output into FileChange entries.
// It tracks the add/modify/delete classification from the file header lines and
// accumulates before/after content per hunk.
func parseUnifiedDiff(diff string) []FileChange {
	var (
		files   []FileChange
		cur     *FileChange
		curHunk *Hunk
	)
	flush := func() {
		if cur != nil && curHunk != nil {
			cur.Hunks = append(cur.Hunks, *curHunk)
			curHunk = nil
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			if cur != nil {
				files = append(files, *cur)
			}
			cur = &FileChange{Path: pathFromDiffHeader(line), Change: ChangeModify}
		case cur == nil:
			// Preamble before the first file header; ignore.
		case strings.HasPrefix(line, "new file mode"):
			cur.Change = ChangeAdd
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Change = ChangeDelete
		case strings.HasPrefix(line, "rename to "):
			cur.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "+++ "):
			if p := pathFromMarker(line); p != "" {
				cur.Path = p
			}
		case strings.HasPrefix(line, "@@"):
			flush()
			curHunk = parseHunkHeader(line)
		case curHunk == nil:
			// Inside a file header but before the first hunk; ignore.
		case strings.HasPrefix(line, "+"):
			content := strings.TrimPrefix(line, "+")
			curHunk.After = append(curHunk.After, content)
			curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: DiffAdd, Text: content})
		case strings.HasPrefix(line, "-"):
			content := strings.TrimPrefix(line, "-")
			curHunk.Before = append(curHunk.Before, content)
			curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: DiffDel, Text: content})
		case strings.HasPrefix(line, " "):
			content := strings.TrimPrefix(line, " ")
			curHunk.Before = append(curHunk.Before, content)
			curHunk.After = append(curHunk.After, content)
			curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: DiffContext, Text: content})
		}
	}
	flush()
	if cur != nil {
		files = append(files, *cur)
	}
	return files
}

// pathFromDiffHeader extracts the new-side path from a "diff --git a/x b/x"
// line, falling back to the old-side path.
func pathFromDiffHeader(line string) string {
	fields := strings.Fields(strings.TrimPrefix(line, "diff --git "))
	if len(fields) != 2 {
		return ""
	}
	if p := strings.TrimPrefix(fields[1], "b/"); p != fields[1] {
		return p
	}
	return strings.TrimPrefix(fields[0], "a/")
}

// pathFromMarker extracts the path from a "+++ b/x" marker, ignoring /dev/null.
func pathFromMarker(line string) string {
	p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, "b/")
}

// parseHunkHeader parses an "@@ -a,b +c,d @@ context" line into a Hunk. Counts
// default to 1 when omitted, matching unified-diff semantics.
func parseHunkHeader(line string) *Hunk {
	h := &Hunk{OldLines: 1, NewLines: 1}
	body := line
	if idx := strings.Index(line[2:], "@@"); idx >= 0 {
		body = line[2 : idx+2]
		h.Header = strings.TrimSpace(line[idx+4:])
	}
	for _, tok := range strings.Fields(body) {
		switch {
		case strings.HasPrefix(tok, "-"):
			h.OldStart, h.OldLines = parseRange(strings.TrimPrefix(tok, "-"))
		case strings.HasPrefix(tok, "+"):
			h.NewStart, h.NewLines = parseRange(strings.TrimPrefix(tok, "+"))
		}
	}
	return h
}

// parseRange parses a "start,count" or "start" unified-diff range token.
func parseRange(tok string) (start, count int) {
	count = 1
	if comma := strings.IndexByte(tok, ','); comma >= 0 {
		start, _ = strconv.Atoi(tok[:comma])
		count, _ = strconv.Atoi(tok[comma+1:])
		return start, count
	}
	start, _ = strconv.Atoi(tok)
	return start, count
}
