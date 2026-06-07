package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

func linkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Manage edges between work items",
		Long: `Add, remove, and list edges (relationships) between work items.

Relationship types: blocks, blocked_by, relates_to, implements,
                    caused_by, spawned_from, implemented_in, part_of, contains

Examples:
  wipnote link add feat-abc feat-def --rel blocks
  wipnote link remove feat-abc feat-def --rel blocks
  wipnote link list feat-abc`,
	}
	cmd.AddCommand(linkAddCmd())
	cmd.AddCommand(linkRemoveCmd())
	cmd.AddCommand(linkListCmd())
	return cmd
}

func linkAddCmd() *cobra.Command {
	var rel string

	cmd := &cobra.Command{
		Use:   "add <from-id> <to-id> [rel-type]",
		Short: "Add an edge between two work items",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(_ *cobra.Command, args []string) error {
			// Accept relationship type as optional 3rd positional arg
			// (e.g. `link add trk-xxx feat-yyy child`) in addition to --rel.
			// "child" is a common alias for "contains".
			if len(args) == 3 {
				rel = args[2]
			}
			return runLinkAdd(args[0], args[1], rel)
		},
	}
	cmd.Flags().StringVar(&rel, "rel", "relates_to",
		"relationship type (blocks, blocked_by, relates_to, implements, caused_by, spawned_from, implemented_in, part_of, contains)")
	return cmd
}

func runLinkAdd(fromID, toID, rel string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	fromID, err = resolveID(dir, fromID)
	if err != nil {
		return err
	}
	toID, err = resolveID(dir, toID)
	if err != nil {
		return err
	}

	// Resolve the target node to get its title for the edge label.
	targetPath := resolveNodePath(dir, toID)
	var title string
	if targetPath != "" {
		if target, err := htmlparse.ParseFile(targetPath); err == nil {
			title = target.Title
		}
	}
	if title == "" {
		title = toID
	}

	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := resolveCollection(p, fromID)
	if col == nil {
		return fmt.Errorf("cannot determine collection for %q\nID prefix must be feat-, bug-, spk-, trk-, plan-, or spec-. Partial hex IDs need the full prefix.", fromID)
	}

	normalizedRel := models.NormalizeRelationship(rel)
	if !models.IsValidRelationship(normalizedRel) {
		return fmt.Errorf("unknown relationship type %q; valid types: blocks, blocked_by, relates_to, implements, caused_by, spawned_from, implemented_in, part_of, contains", rel)
	}

	edge := models.Edge{
		TargetID:     toID,
		Relationship: normalizedRel,
		Title:        title,
		Since:        time.Now().UTC(),
	}

	_, err = col.AddEdge(fromID, edge)
	if err != nil {
		return fmt.Errorf("add edge: %w", err)
	}

	fmt.Printf("Linked: %s -[%s]-> %s\n", fromID, rel, toID)
	return nil
}

func linkRemoveCmd() *cobra.Command {
	var rel string

	cmd := &cobra.Command{
		Use:   "remove <from-id> <to-id>",
		Short: "Remove an edge between two work items",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runLinkRemove(args[0], args[1], rel)
		},
	}
	cmd.Flags().StringVar(&rel, "rel", "",
		"relationship type to remove (required)")
	_ = cmd.MarkFlagRequired("rel")
	return cmd
}

func runLinkRemove(fromID, toID, rel string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	fromID, err = resolveID(dir, fromID)
	if err != nil {
		return err
	}
	toID, err = resolveID(dir, toID)
	if err != nil {
		return err
	}

	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := resolveCollection(p, fromID)
	if col == nil {
		return fmt.Errorf("cannot determine collection for %q\nID prefix must be feat-, bug-, spk-, trk-, plan-, or spec-. Partial hex IDs need the full prefix.", fromID)
	}

	_, removed, err := col.RemoveEdge(fromID, toID, models.RelationshipType(rel))
	if err != nil {
		return fmt.Errorf("remove edge: %w", err)
	}
	if !removed {
		return fmt.Errorf("no %s edge from %s to %s\nRun 'wipnote link list %s' to see existing edges for this work item.", rel, fromID, toID, fromID)
	}

	fmt.Printf("Unlinked: %s -[%s]-> %s\n", fromID, rel, toID)
	return nil
}

func linkListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <id>",
		Short: "List all edges for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runLinkList(args[0])
		},
	}
}

func runLinkList(id string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	id, err = resolveID(dir, id)
	if err != nil {
		return err
	}

	path := resolveNodePath(dir, id)
	if path == "" {
		return fmt.Errorf("work item %q not found", id)
	}

	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse %s: %w", id, err)
	}

	if len(node.Edges) == 0 {
		fmt.Printf("%s has no edges.\n", id)
		return nil
	}

	fmt.Printf("Edges for %s (%s):\n", id, node.Title)
	fmt.Println(strings.Repeat("-", 60))
	total := 0
	for rel, edges := range node.Edges {
		for _, e := range edges {
			label := e.Title
			if label == "" {
				label = e.TargetID
			}
			fmt.Printf("  %-15s -> %-20s  %s\n", rel, e.TargetID, label)
			total++
		}
	}
	fmt.Printf("\n%d edge(s)\n", total)
	return nil
}

// resolveCollection returns the Collection for a node ID based on its prefix.
func resolveCollection(p *workitem.Project, id string) *workitem.Collection {
	switch {
	case strings.HasPrefix(id, "feat-"):
		return p.Features.Collection
	case strings.HasPrefix(id, "bug-"):
		return p.Bugs.Collection
	case strings.HasPrefix(id, "spk-"):
		return p.Spikes.Collection
	case strings.HasPrefix(id, "trk-"):
		return p.Tracks.Collection
	case strings.HasPrefix(id, "plan-"):
		return p.Plans.Collection
	case strings.HasPrefix(id, "spec-"):
		return p.Specs.Collection
	default:
		return nil
	}
}
