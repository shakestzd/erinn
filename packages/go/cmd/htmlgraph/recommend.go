// Register in main.go: rootCmd.AddCommand(recommendCmd())
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/graph"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/workitem"
	"github.com/spf13/cobra"
)

// recommendOutput is the JSON-serialisable form of the full recommend output.
type recommendOutput struct {
	GeneratedAt   string                      `json:"generated_at"`
	Health        map[string]typeCounts       `json:"health"`
	WIP           wipSummary                  `json:"wip"`
	Bottlenecks   []workitem.Bottleneck       `json:"bottlenecks"`
	Recommended   []workitem.Recommendation   `json:"recommended"`
	Parallel      []parallelSetSummary        `json:"parallel_opportunities"`
}

type typeCounts struct {
	Todo    int `json:"todo"`
	Active  int `json:"active"`
	Blocked int `json:"blocked"`
	Done    int `json:"done"`
}

type wipSummary struct {
	Count int      `json:"count"`
	Limit int      `json:"limit"`
	Items []wipRow `json:"items"`
}

type wipRow struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type parallelSetSummary struct {
	TrackID string   `json:"track_id"`
	Items   []string `json:"item_ids"`
}

func recommendCmd() *cobra.Command {
	var topN int
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "recommend",
		Short: "Consolidated project health, WIP, bottlenecks, and recommendations",
		Long: `Single command that shows all analytics in one call:
  - Project health snapshot (counts by type and status)
  - WIP status against limit
  - Bottlenecks (stale items, overloaded tracks)
  - Recommended next work items
  - Parallel work opportunities`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runRecommend(topN, jsonOut)
		},
	}
	cmd.Flags().IntVar(&topN, "top", 5, "number of recommendations to show")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output as JSON for programmatic consumption")
	return cmd
}

func runRecommend(topN int, jsonOut bool) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	nodes, err := graph.LoadAll(dir)
	if err != nil {
		return fmt.Errorf("load work items: %w", err)
	}

	health := buildHealthCounts(nodes)
	wipItems := collectWIPItems(nodes)
	bottlenecks, err := workitem.FindBottlenecks(dir)
	if err != nil {
		return fmt.Errorf("find bottlenecks: %w", err)
	}
	recs, err := workitem.RecommendNextWork(dir)
	if err != nil {
		return fmt.Errorf("recommend next work: %w", err)
	}
	if len(recs) > topN {
		recs = recs[:topN]
	}
	parallelSets, err := workitem.GetParallelWork(dir)
	if err != nil {
		return fmt.Errorf("get parallel work: %w", err)
	}

	if jsonOut {
		return printRecommendJSON(health, wipItems, bottlenecks, recs, parallelSets)
	}
	printRecommendText(health, wipItems, bottlenecks, recs, parallelSets)
	return nil
}

func buildHealthCounts(nodes []*models.Node) map[string]typeCounts {
	counts := make(map[string]typeCounts)
	for _, n := range nodes {
		c := counts[n.Type]
		switch n.Status {
		case models.StatusTodo:
			c.Todo++
		case models.StatusInProgress:
			c.Active++
		case models.StatusBlocked:
			c.Blocked++
		case models.StatusDone:
			c.Done++
		}
		counts[n.Type] = c
	}
	return counts
}

func collectWIPItems(nodes []*models.Node) []*models.Node {
	var out []*models.Node
	for _, n := range nodes {
		if n.Status == models.StatusInProgress && n.Type != "track" {
			out = append(out, n)
		}
	}
	return out
}

func printRecommendText(
	health map[string]typeCounts,
	wipItems []*models.Node,
	bottlenecks []workitem.Bottleneck,
	recs []workitem.Recommendation,
	parallelSets []workitem.ParallelSet,
) {
	fmt.Printf("Project Health (%s)\n", time.Now().Format("2006-01-02"))
	fmt.Printf("%-10s  %5s  %6s  %7s  %4s\n", "TYPE", "TODO", "ACTIVE", "BLOCKED", "DONE")
	fmt.Println(strings.Repeat("─", 42))
	for _, t := range []string{"feature", "bug", "spike", "track"} {
		c, ok := health[t]
		if !ok {
			continue
		}
		fmt.Printf("%-10s  %5d  %6d  %7d  %4d\n", t+"s", c.Todo, c.Active, c.Blocked, c.Done)
	}
	fmt.Println()

	wipStatus := "OK"
	if len(wipItems) >= wipLimit {
		wipStatus = "AT LIMIT"
	}
	fmt.Printf("WIP: %d/%d [%s]\n", len(wipItems), wipLimit, wipStatus)
	for _, n := range wipItems {
		fmt.Printf("  %-20s  %-8s  %s\n", n.ID, n.Type, truncate(n.Title, 44))
	}
	fmt.Println()

	fmt.Println("Bottlenecks:")
	if len(bottlenecks) == 0 {
		fmt.Println("  none")
	}
	for _, b := range bottlenecks {
		fmt.Printf("  %-20s  %s  %s\n", b.ItemID, b.Type, b.Reason)
	}
	fmt.Println()

	fmt.Printf("Recommended (top %d):\n", len(recs))
	if len(recs) == 0 {
		fmt.Println("  none")
	}
	for i, r := range recs {
		fmt.Printf("  %d. [%-8s]  %-20s  %s  — %s\n",
			i+1, r.Priority, r.ItemID, truncate(r.Title, 40), r.Reason)
	}
	fmt.Println()

	fmt.Println("Parallel Opportunities:")
	if len(parallelSets) == 0 {
		fmt.Println("  none")
	}
	for _, ps := range parallelSets {
		ids := make([]string, 0, len(ps.Items))
		for _, item := range ps.Items {
			ids = append(ids, item.ID)
		}
		fmt.Printf("  %s: %s\n", ps.TrackID, strings.Join(ids, ", "))
	}
}

func printRecommendJSON(
	health map[string]typeCounts,
	wipItems []*models.Node,
	bottlenecks []workitem.Bottleneck,
	recs []workitem.Recommendation,
	parallelSets []workitem.ParallelSet,
) error {
	wip := wipSummary{Count: len(wipItems), Limit: wipLimit}
	for _, n := range wipItems {
		wip.Items = append(wip.Items, wipRow{ID: n.ID, Type: n.Type, Title: n.Title})
	}

	var parallel []parallelSetSummary
	for _, ps := range parallelSets {
		ids := make([]string, 0, len(ps.Items))
		for _, item := range ps.Items {
			ids = append(ids, item.ID)
		}
		parallel = append(parallel, parallelSetSummary{TrackID: ps.TrackID, Items: ids})
	}

	out := recommendOutput{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Health:      health,
		WIP:         wip,
		Bottlenecks: bottlenecks,
		Recommended: recs,
		Parallel:    parallel,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
