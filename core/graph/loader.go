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

// LoadAll reads features, bugs, spikes, tracks, plans, and specs from a .wipnote root.
func LoadAll(wipnoteDir string) ([]*models.Node, error) {
	subdirs := []string{"features", "bugs", "spikes", "tracks", "plans", "specs"}
	var all []*models.Node

	for _, sub := range subdirs {
		dir := filepath.Join(wipnoteDir, sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		nodes, err := LoadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", sub, err)
		}
		all = append(all, nodes...)
	}
	return all, nil
}
