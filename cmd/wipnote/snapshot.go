package main

import (
	"fmt"
	"sort"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

func snapshotCmd() *cobra.Command {
	var summary bool

	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Quick project overview",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSnapshot(summary)
		},
	}
	cmd.Flags().BoolVar(&summary, "summary", false, "Print compact summary table")
	return cmd
}

type snapshotRow struct {
	nodeType   string
	todo       int
	inProgress int
	blocked    int
	done       int
	total      int
}

func runSnapshot(summary bool) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	nodes, err := graph.LoadAll(dir)
	if err != nil {
		return fmt.Errorf("load work items: %w", err)
	}

	rowMap := make(map[string]*snapshotRow)
	var inProgressNodes []*models.Node

	for _, n := range nodes {
		if rowMap[n.Type] == nil {
			rowMap[n.Type] = &snapshotRow{nodeType: n.Type}
		}
		r := rowMap[n.Type]
		r.total++
		switch n.Status {
		case models.StatusTodo:
			r.todo++
		case models.StatusInProgress:
			r.inProgress++
			inProgressNodes = append(inProgressNodes, n)
		case models.StatusBlocked:
			r.blocked++
		case models.StatusDone:
			r.done++
		}
	}

	typeOrder := []string{"feature", "bug", "spike", "track"}

	if summary {
		printSummaryTable(rowMap, typeOrder, len(nodes))
		return nil
	}

	// Full snapshot view.
	fmt.Printf("wipnote snapshot  (%s)\n", dir)
	fmt.Printf("%-12s  %5s  %8s  %7s  %4s\n",
		"TYPE", "TOTAL", "IN-PROG", "BLOCKED", "DONE")
	fmt.Println("─────────────────────────────────────────")

	for _, t := range typeOrder {
		r := rowMap[t]
		if r == nil {
			continue
		}
		fmt.Printf("%-12s  %5d  %8d  %7d  %4d\n",
			t+"s", r.total, r.inProgress, r.blocked, r.done)
	}
	fmt.Println("─────────────────────────────────────────")
	fmt.Printf("%-12s  %5d\n", "total", len(nodes))

	if len(inProgressNodes) > 0 {
		sort.Slice(inProgressNodes, func(i, j int) bool {
			return inProgressNodes[i].ID < inProgressNodes[j].ID
		})
		fmt.Println("\nActive work:")
		for _, n := range inProgressNodes {
			fmt.Printf("  %-20s  %s  %s\n",
				n.ID, n.Type, truncate(n.Title, 50))
		}
	}

	return nil
}

func printSummaryTable(rowMap map[string]*snapshotRow, order []string, total int) {
	fmt.Printf("%-10s  %5s  %4s  %7s  %4s\n",
		"", "TODO", "WIP", "BLOCKED", "DONE")
	for _, t := range order {
		r := rowMap[t]
		if r == nil {
			continue
		}
		fmt.Printf("%-10s  %5d  %4d  %7d  %4d\n",
			t+"s", r.todo, r.inProgress, r.blocked, r.done)
	}
	fmt.Printf("\nTotal: %d items\n", total)
}
