package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

func checkIncompleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "incomplete",
		Short: "Report work items missing recommended fields",
		Long: `Scan all work items and report those missing recommended fields:
  bug:     description
  feature: description
  spec:    description
  spike:   (no requirements)
  track:   steps (requirements)
  plan:    steps`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCheckIncomplete()
		},
	}
}

type incompleteItem struct {
	id       string
	title    string
	itemType string
	missing  []string
}

func runCheckIncomplete() error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	checks := []struct {
		subdir   string
		typeName string
		check    func(*models.Node) []string
	}{
		{"features", "feature", checkFeatureFields},
		{"bugs", "bug", checkBugFields},
		{"specs", "spec", checkSpecFields},
		{"tracks", "track", checkTrackFields},
		{"plans", "plan", checkPlanFields},
	}

	var items []incompleteItem
	for _, c := range checks {
		nodes, err := graph.LoadDir(filepath.Join(dir, c.subdir))
		if err != nil {
			continue
		}
		for _, n := range nodes {
			if missing := c.check(n); len(missing) > 0 {
				items = append(items, incompleteItem{
					id:       n.ID,
					title:    n.Title,
					itemType: c.typeName,
					missing:  missing,
				})
			}
		}
	}

	if len(items) == 0 {
		fmt.Println("All work items have recommended fields populated.")
		return nil
	}

	fmt.Printf("Found %d work item(s) with missing fields:\n", len(items))
	fmt.Println(strings.Repeat("-", 70))
	for _, item := range items {
		fmt.Printf("  %-8s  %-20s  missing: %s\n",
			item.itemType, item.id, strings.Join(item.missing, ", "))
	}
	return nil
}

func checkFeatureFields(n *models.Node) []string {
	var missing []string
	if n.Content == "" {
		missing = append(missing, "description")
	}
	return missing
}

func checkBugFields(n *models.Node) []string {
	var missing []string
	if n.Content == "" {
		missing = append(missing, "description")
	}
	return missing
}

func checkSpecFields(n *models.Node) []string {
	var missing []string
	if n.Content == "" {
		missing = append(missing, "description")
	}
	return missing
}

func checkTrackFields(n *models.Node) []string {
	var missing []string
	if len(n.Steps) == 0 {
		missing = append(missing, "steps")
	}
	return missing
}

func checkPlanFields(n *models.Node) []string {
	var missing []string
	if len(n.Steps) == 0 {
		missing = append(missing, "steps")
	}
	return missing
}
