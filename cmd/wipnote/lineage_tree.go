package main

import (
	"database/sql"
	"fmt"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// RenderAgentTree reads agent_lineage_trace rows for the given rootSessionID,
// reconstructs the parent→child tree, and returns an indented text tree.
//
// Format per node line:
//
//	<indent><agent_name>  <short_session>  d<depth>  <feature_id>
//
// Indent is 2 spaces per depth level. Nodes are emitted in depth-first order.
// Rows with len(path) < 2 are treated as roots (no parent derivable).
func RenderAgentTree(db *sql.DB, rootSessionID string) (string, error) {
	traces, err := dbpkg.GetLineageByRoot(db, rootSessionID)
	if err != nil {
		return "", fmt.Errorf("get lineage by root %s: %w", rootSessionID, err)
	}

	// Index rows by session ID for O(1) lookup.
	bySession := make(map[string]*models.LineageTrace, len(traces))
	for i := range traces {
		bySession[traces[i].SessionID] = &traces[i]
	}

	// Build parent→children adjacency list.
	// A row is a root when len(path) < 2 (cannot derive a parent) OR when its
	// derived parent session is missing from bySession — that happens for
	// partial or inconsistent lineage data and promoting the orphan to a root
	// keeps it visible instead of dropping it silently.
	children := make(map[string][]string) // parentSessionID -> []childSessionID
	var roots []string

	for i := range traces {
		t := &traces[i]
		if len(t.Path) < 2 {
			roots = append(roots, t.SessionID)
			continue
		}
		parent := t.Path[len(t.Path)-2]
		if _, ok := bySession[parent]; !ok {
			roots = append(roots, t.SessionID)
			continue
		}
		children[parent] = append(children[parent], t.SessionID)
	}

	sep := strings.Repeat("─", 60)
	var sb strings.Builder
	fmt.Fprintln(&sb, sep)
	fmt.Fprintf(&sb, "  Agent tree: %s\n", truncate(rootSessionID, 16))
	fmt.Fprintln(&sb, sep)

	// DFS emission.
	var dfs func(sessionID string, depth int)
	dfs = func(sessionID string, depth int) {
		t, ok := bySession[sessionID]
		if !ok {
			return
		}
		indent := strings.Repeat("  ", depth)
		shortSess := truncate(sessionID, 8)
		featureID := t.FeatureID
		if featureID == "" {
			featureID = "-"
		}
		agentName := t.AgentName
		if agentName == "" {
			agentName = "(unknown)"
		}
		fmt.Fprintf(&sb, "%s%s  %s  d%d  %s\n", indent, agentName, shortSess, depth, featureID)
		for _, childID := range children[sessionID] {
			dfs(childID, depth+1)
		}
	}

	for _, rootID := range roots {
		dfs(rootID, 0)
	}

	return sb.String(), nil
}
