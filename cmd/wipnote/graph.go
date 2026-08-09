package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/projection"
	"github.com/spf13/cobra"
)

func graphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Query the work item link graph",
		Long: `Traverse and analyze the work item link graph.

Subcommands:
  cycles      — detect circular dependencies
  path        — shortest path between two nodes
  reach       — all nodes reachable within N hops
  orphans     — nodes with zero edges
  hubs        — highly connected nodes
  bottlenecks — nodes that block the most others
  sessions    — sessions that worked on a feature`,
	}
	cmd.AddCommand(graphCyclesCmd())
	cmd.AddCommand(graphPathCmd())
	cmd.AddCommand(graphReachCmd())
	cmd.AddCommand(graphOrphansCmd())
	cmd.AddCommand(graphHubsCmd())
	cmd.AddCommand(graphBottlenecksCmd())
	cmd.AddCommand(graphSessionsCmd())
	return cmd
}

func graphCyclesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cycles",
		Short: "Detect circular dependencies in the link graph",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runGraphCycles()
		},
	}
}

func runGraphCycles() error {
	snap, err := loadGraphProjection()
	if err != nil {
		return err
	}
	cycles := snap.Cycles()

	if len(cycles) == 0 {
		fmt.Println("No circular dependencies found.")
		return nil
	}

	// Resolve node titles for display.
	allIDs := graph.AllUniqueIDs(cycles)
	resolved := snap.ResolveToMap(allIDs)

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Circular Dependencies (%d)\n", len(cycles))
	fmt.Println(sep)
	for i, cycle := range cycles {
		fmt.Printf("\n  Cycle %d:\n", i+1)
		for j, id := range cycle {
			label := graph.FormatNodeLabel(id, resolved)
			if j < len(cycle)-1 {
				fmt.Printf("    %s ->\n", label)
			} else {
				fmt.Printf("    %s -> (back to %s)\n", label, cycle[0])
			}
		}
	}
	return nil
}

func graphPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path <from-id> <to-id>",
		Short: "Find shortest path between two nodes",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runGraphPath(args[0], args[1])
		},
	}
}

func runGraphPath(fromID, toID string) error {
	snap, err := loadGraphProjection()
	if err != nil {
		return err
	}
	path := snap.ShortestPath(fromID, toID)
	if path == nil {
		fmt.Printf("No path from %s to %s.\n", fromID, toID)
		return nil
	}

	resolved := snap.ResolveToMap(path)

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Shortest Path (%d hops)\n", len(path)-1)
	fmt.Println(sep)
	for i, id := range path {
		label := graph.FormatNodeLabel(id, resolved)
		if i < len(path)-1 {
			fmt.Printf("  %s ->\n", label)
		} else {
			fmt.Printf("  %s\n", label)
		}
	}
	return nil
}

func graphReachCmd() *cobra.Command {
	var depth int
	cmd := &cobra.Command{
		Use:   "reach <id> [--depth N]",
		Short: "All nodes reachable within N hops",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runGraphReach(args[0], depth)
		},
	}
	cmd.Flags().IntVar(&depth, "depth", 3, "maximum traversal depth")
	return cmd
}

func runGraphReach(startID string, depth int) error {
	snap, err := loadGraphProjection()
	if err != nil {
		return err
	}
	ids := snap.Reachable(startID, depth)

	if len(ids) == 0 {
		fmt.Printf("No nodes reachable from %s within %s hops.\n",
			startID, strconv.Itoa(depth))
		return nil
	}

	resolved := snap.ResolveToMap(ids)

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Reachable from %s (depth %d): %d nodes\n",
		startID, depth, len(ids))
	fmt.Println(sep)
	for _, id := range ids {
		label := graph.FormatNodeLabel(id, resolved)
		fmt.Printf("  %s\n", label)
	}
	return nil
}

func graphOrphansCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "orphans",
		Short: "Find nodes with zero edges",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runGraphOrphans()
		},
	}
}

func runGraphOrphans() error {
	snap, err := loadGraphProjection()
	if err != nil {
		return err
	}
	ids := snap.Orphans()
	if len(ids) == 0 {
		fmt.Println("No orphan nodes found.")
		return nil
	}

	resolved := snap.ResolveToMap(ids)

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Orphan Nodes (%d)\n", len(ids))
	fmt.Println(sep)
	for _, id := range ids {
		label := graph.FormatNodeLabel(id, resolved)
		fmt.Printf("  %s\n", label)
	}
	return nil
}

func graphHubsCmd() *cobra.Command {
	var minEdges int
	cmd := &cobra.Command{
		Use:   "hubs [--min-edges N]",
		Short: "Find highly connected nodes",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runGraphHubs(minEdges)
		},
	}
	cmd.Flags().IntVar(&minEdges, "min-edges", 3, "minimum edge count")
	return cmd
}

func runGraphHubs(minEdges int) error {
	snap, err := loadGraphProjection()
	if err != nil {
		return err
	}
	hubs := snap.Hubs(minEdges)
	if len(hubs) == 0 {
		fmt.Printf("No nodes with %d+ edges found.\n", minEdges)
		return nil
	}

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Hub Nodes (%d, min %d edges)\n", len(hubs), minEdges)
	fmt.Println(sep)
	for _, h := range hubs {
		title := h.Title
		if title == "" {
			title = h.ID
		}
		fmt.Printf("  %-25s  %s\n", h.ID, title)
	}
	return nil
}

func graphBottlenecksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bottlenecks",
		Short: "Find nodes that block the most others",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runGraphBottlenecks()
		},
	}
}

func runGraphBottlenecks() error {
	snap, err := loadGraphProjection()
	if err != nil {
		return err
	}
	bns := snap.Bottlenecks()
	if len(bns) == 0 {
		fmt.Println("No bottleneck nodes found.")
		return nil
	}

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Bottleneck Nodes (%d)\n", len(bns))
	fmt.Println(sep)
	for _, b := range bns {
		title := b.Title
		if title == "" {
			title = b.ID
		}
		fmt.Printf("  %-25s  blocks %d items  [%s]  %s\n",
			b.ID, b.BlockCount, b.Status, title)
	}
	return nil
}

func graphSessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions <feature-id>",
		Short: "Show sessions that worked on a feature",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runGraphSessions(args[0])
		},
	}
}

func runGraphSessions(featureID string) error {
	snap, err := loadGraphProjection()
	if err != nil {
		return err
	}
	sessions := snap.SessionsForFeature(featureID)
	if len(sessions) == 0 {
		fmt.Printf("No sessions found for %s.\n", featureID)
		return nil
	}

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Sessions for %s (%d)\n", featureID, len(sessions))
	fmt.Println(sep)
	for _, s := range sessions {
		created := s.CreatedAt
		if len(created) > 19 {
			created = created[:19]
		}
		fmt.Printf("  %-20s  [%s]  %s  %s\n",
			truncate(s.SessionID, 20), s.Status, s.Agent, created)
	}
	return nil
}

func loadGraphProjection() (*projection.Snapshot, error) {
	dir, err := findWipnoteDir()
	if err != nil {
		return nil, err
	}
	snap, err := projection.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("load canonical projection: %w", err)
	}
	return snap, nil
}
