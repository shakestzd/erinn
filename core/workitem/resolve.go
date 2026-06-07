package workitem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// subdirs is the ordered list of collection directories to scan.
var subdirs = []string{"features", "bugs", "spikes", "tracks", "plans", "specs"}

// kindFromPrefix determines the work item kind from an ID prefix.
// Examples: "feat-" -> "feature", "bug-" -> "bug", "trk-" -> "track", "spk-" -> "spike"
func kindFromPrefix(id string) string {
	if strings.HasPrefix(id, "feat-") {
		return "feature"
	}
	if strings.HasPrefix(id, "bug-") {
		return "bug"
	}
	if strings.HasPrefix(id, "spk-") {
		return "spike"
	}
	if strings.HasPrefix(id, "trk-") {
		return "track"
	}
	if strings.HasPrefix(id, "pln-") {
		return "plan"
	}
	if strings.HasPrefix(id, "spc-") {
		return "spec"
	}
	return "work item" // fallback
}

// ResolvePartialID resolves a partial or full work item ID to a canonical ID.
//
// Resolution order:
//  1. Exact match — returns immediately if <wipnoteDir>/<subdir>/<id>.html exists.
//  2. Prefix match — scans all collection directories for any file whose
//     stem starts with id. If exactly one match is found, returns it.
//     If multiple matches are found, returns an error listing all candidates.
//  3. No match — returns an error.
//
// The returned string is always the full canonical ID (e.g. "feat-43aea33f"),
// never a file path.
func ResolvePartialID(wipnoteDir, id string) (string, error) {
	// 1. Exact match: check each subdir for <id>.html.
	for _, sub := range subdirs {
		p := filepath.Join(wipnoteDir, sub, id+".html")
		if _, err := os.Stat(p); err == nil {
			return id, nil
		}
	}

	// 2. Prefix match across all collection directories.
	matches, err := prefixMatchIDs(wipnoteDir, id)
	if err != nil {
		return "", err
	}

	switch len(matches) {
	case 0:
		// Determine kind from the ID prefix for better error message
		kind := kindFromPrefix(id)
		return "", ErrNotFound(kind, id)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("ambiguous ID %q — did you mean one of: %s",
			id, strings.Join(matches, ", "))
	}
}

// prefixMatchIDs scans all collection subdirectories for HTML files whose
// stem (filename without .html) starts with prefix. Returns all matching IDs.
func prefixMatchIDs(wipnoteDir, prefix string) ([]string, error) {
	var matches []string
	for _, sub := range subdirs {
		dir := filepath.Join(wipnoteDir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("scan %s: %w", sub, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".html") {
				continue
			}
			stem := strings.TrimSuffix(name, ".html")
			if strings.HasPrefix(stem, prefix) {
				matches = append(matches, stem)
			}
		}
	}
	return matches, nil
}
