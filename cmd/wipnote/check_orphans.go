package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

func checkOrphansCmd() *cobra.Command {
	var strict bool

	cmd := &cobra.Command{
		Use:   "orphans",
		Short: "Report work items with no track relationship",
		Long: `Scan all features, bugs, plans, and specs for items not linked to any track.
Spikes are always exempt from orphan warnings.

Use --strict to return exit code 1 if orphaned items exist (for CI/pre-commit).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCheckOrphans(strict)
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false,
		"exit code 1 if orphaned items exist")
	return cmd
}

func runCheckOrphans(strict bool) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	type orphanItem struct {
		id       string
		title    string
		itemType string
	}

	var orphans []orphanItem
	dirs := []struct {
		subdir   string
		typeName string
	}{
		{"features", "feature"},
		{"bugs", "bug"},
		{"plans", "plan"},
		{"specs", "spec"},
		// spikes intentionally excluded — always exempt
	}

	for _, d := range dirs {
		nodes, err := graph.LoadDir(filepath.Join(dir, d.subdir))
		if err != nil {
			continue
		}
		for _, n := range nodes {
			if isOrphan(n) {
				orphans = append(orphans, orphanItem{
					id:       n.ID,
					title:    n.Title,
					itemType: d.typeName,
				})
			}
		}
	}

	if len(orphans) == 0 {
		fmt.Println("No orphaned work items found.")
		return nil
	}

	fmt.Printf("Found %d orphaned work item(s):\n", len(orphans))
	fmt.Println(strings.Repeat("-", 70))
	for _, o := range orphans {
		fmt.Printf("  %-8s  %-20s  %s\n", o.itemType, o.id, truncate(o.title, 36))
	}
	fmt.Printf("\nUse --track when creating items to link them to an initiative.\n")

	if strict {
		os.Exit(1)
	}
	return nil
}

// isOrphan returns true if a node has no track relationship.
func isOrphan(n *models.Node) bool {
	if n.TrackID != "" {
		return false
	}
	for _, edges := range n.Edges {
		for _, e := range edges {
			if e.Relationship == models.RelPartOf {
				return false
			}
		}
	}
	return true
}
