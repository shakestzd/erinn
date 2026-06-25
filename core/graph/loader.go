// Package graph loads and queries wipnote work item files.
package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// LoadDir reads all HTML work item files from a directory and returns Nodes.
// Supports both flat format (id.html) and subdirectory format (id/index.html).
// Non-HTML files and directories without index.html are silently skipped.
func LoadDir(dir string) ([]*models.Node, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	var nodes []*models.Node
	for _, entry := range entries {
		var path string
		if entry.IsDir() {
			// Try subdirectory format: id/index.html
			path = filepath.Join(dir, entry.Name(), "index.html")
			if _, err := os.Stat(path); err != nil {
				continue
			}
		} else if !strings.HasSuffix(entry.Name(), ".html") {
			// Skip non-HTML files
			continue
		} else {
			// Flat format: id.html
			path = filepath.Join(dir, entry.Name())
		}

		node, err := htmlparse.ParseFile(path)
		if err != nil {
			// Skip unparseable files (matches Python's lenient behaviour).
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// LoadAll reads features, bugs, spikes, tracks, plans, and specs from a .wipnote
// root, PLUS any work items that have been compacted into archive ledgers under
// .wipnote/archive/. Archived items are canonical and must remain visible to
// every canonical-first reader (find/analytics/status/snapshot/recommend/track/
// list) — merging them here is the single chokepoint that guarantees that
// without touching each caller. Items still present as individual files win over
// a stale ledger row of the same ID (de-dup by ID, file first).
func LoadAll(wipnoteDir string) ([]*models.Node, error) {
	subdirs := []string{"features", "bugs", "spikes", "tracks", "plans", "specs"}
	var all []*models.Node
	seen := make(map[string]bool)

	for _, sub := range subdirs {
		dir := filepath.Join(wipnoteDir, sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		nodes, err := LoadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", sub, err)
		}
		for _, n := range nodes {
			seen[n.ID] = true
		}
		all = append(all, nodes...)
	}

	archived, err := LoadArchivedNodes(wipnoteDir)
	if err != nil {
		return nil, fmt.Errorf("loading archive ledgers: %w", err)
	}
	for _, n := range archived {
		if seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		all = append(all, n)
	}
	return all, nil
}
