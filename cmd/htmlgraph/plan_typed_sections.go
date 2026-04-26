package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/htmlparse"
	"github.com/shakestzd/htmlgraph/internal/plantmpl"
	"github.com/shakestzd/htmlgraph/internal/storage"
)

// buildTypedPlanSections builds typed plantmpl.SliceCard and DependencyGraph
// from a work item's "contains" edges. Each slice gets structured What/Files
// fields populated from the child feature's content and DB file counts.
func buildTypedPlanSections(nodePath, htmlgraphDir string) ([]plantmpl.SliceCard, *plantmpl.DependencyGraph) {
	node, err := htmlparse.ParseFile(nodePath)
	if err != nil {
		return nil, nil
	}

	containsEdges := node.Edges["contains"]
	if len(containsEdges) == 0 {
		return nil, nil
	}

	// Build feature index for dependency resolution.
	idToNum := make(map[string]int, len(containsEdges))
	type featureInfo struct {
		num   int
		id    string
		title string
	}
	features := make([]featureInfo, 0, len(containsEdges))
	for i, edge := range containsEdges {
		title := strings.TrimSpace(edge.Title)
		if title == "" {
			title = edge.TargetID
		}
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		num := i + 1
		idToNum[edge.TargetID] = num
		features = append(features, featureInfo{num: num, id: edge.TargetID, title: title})
	}

	var database *sql.DB
	if dbPath, pathErr := storage.CanonicalDBPath(filepath.Dir(htmlgraphDir)); pathErr == nil {
		if db, dbErr := dbpkg.Open(dbPath); dbErr == nil {
			database = db
			defer database.Close()
		}
	}

	var slices []plantmpl.SliceCard
	var nodes []plantmpl.GraphNode

	for _, f := range features {
		var deps []string
		var what string
		var files int

		childPath := resolveNodePath(htmlgraphDir, f.id)
		if childPath != "" {
			if childNode, err := htmlparse.ParseFile(childPath); err == nil {
				if childNode.Content != "" {
					what = stripHTMLTags(childNode.Content)
					if len(what) > 200 {
						what = what[:197] + "..."
					}
				}
				for _, be := range childNode.Edges["blocked_by"] {
					if num, ok := idToNum[be.TargetID]; ok {
						deps = append(deps, fmt.Sprintf("%d", num))
					}
				}
			}
		}
		if database != nil {
			if count, err := dbpkg.CountFilesByFeature(database, f.id); err == nil {
				files = count
			}
		}

		depStr := strings.Join(deps, ",")

		slices = append(slices, plantmpl.SliceCard{
			Num:    f.num,
			ID:     f.id,
			Title:  f.title,
			What:   what,
			Deps:   depStr,
			Status: "pending",
		})
		nodes = append(nodes, plantmpl.GraphNode{
			Num:    f.num,
			Name:   f.title,
			Status: "pending",
			Deps:   depStr,
			Files:  files,
		})
	}

	graph := &plantmpl.DependencyGraph{Nodes: nodes}
	return slices, graph
}

// stripHTMLTags removes common HTML tags from content for plain-text display.
func stripHTMLTags(s string) string {
	s = strings.ReplaceAll(s, "<p>", "")
	s = strings.ReplaceAll(s, "</p>", "")
	s = strings.ReplaceAll(s, "<br>", " ")
	s = strings.ReplaceAll(s, "<br/>", " ")
	return strings.TrimSpace(s)
}
