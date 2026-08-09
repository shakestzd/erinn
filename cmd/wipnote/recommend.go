// Register in main.go: rootCmd.AddCommand(recommendCmd())
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// recommendOutput is the JSON-serialisable form of the full recommend output.
type recommendOutput struct {
	GeneratedAt string                    `json:"generated_at"`
	Health      map[string]typeCounts     `json:"health"`
	WIP         wipSummary                `json:"wip"`
	Bottlenecks []workitem.Bottleneck     `json:"bottlenecks"`
	Recommended []workitem.Recommendation `json:"recommended"`
	Parallel    []parallelSetSummary      `json:"parallel_opportunities"`
}

type typeCounts struct {
	Todo    int `json:"todo"`
	Active  int `json:"active"`
	Blocked int `json:"blocked"`
	Done    int `json:"done"`
}

type wipSummary struct {
	Count               int      `json:"count"`
	AdvisoryLimit       int      `json:"advisory_limit"`
	PerSessionSoftLimit int      `json:"per_session_soft_limit"`
	Items               []wipRow `json:"items"`
}

type wipRow struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	// Rollup surfaces the item's outcome signal (feat-7ee73444) — set only
	// when a rollup was actually measured for this item, most commonly a
	// reopen of previously-completed work (Start does not touch rollup
	// properties). nil means unmeasured, not clean, and a caller must not
	// read the omission either way (feat-f9118b9c).
	Rollup *workitem.ItemRollupSignal `json:"rollup,omitempty"`
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
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	// One corpus parse for the whole command. Every section below is derived
	// from this node set; before bug-1a51ab15 the three analytics calls each
	// re-read every HTML file from disk, so a single invocation paid four
	// full parses. Threading the loaded set through also makes the sections
	// mutually consistent — WIP already counted plan nodes that the
	// bottleneck and parallel sections could not see.
	nodes, err := graph.LoadAll(dir)
	if err != nil {
		return fmt.Errorf("load work items: %w", err)
	}

	health := buildHealthCounts(nodes)
	wipItems := collectWIPItems(nodes)
	bottlenecks := workitem.FindBottlenecksIn(nodes)
	recs := workitem.RecommendNextWorkIn(nodes)
	if len(recs) > topN {
		recs = recs[:topN]
	}
	parallelSets := workitem.GetParallelWorkIn(nodes)

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
	if len(wipItems) >= wipGlobalAdvisoryLimit {
		wipStatus = "ADVISORY"
	}
	fmt.Printf("WIP: %d/%d [%s]\n", len(wipItems), wipGlobalAdvisoryLimit, wipStatus)
	for _, n := range wipItems {
		note := ""
		// A WIP item can carry a rollup left by an earlier completion (Start
		// does not touch rollup properties — core/workitem/collection.go),
		// most commonly a reopen of previously-done work. Flag it inline;
		// `wipnote recommend` Bottlenecks already carries the itemized
		// failure-rate/retries/churn detail (feat-f9118b9c).
		if sig := workitem.RollupSignalFor(n); sig.Measured && sig.Thrashed() {
			note = "  [prior-run thrash]"
		}
		fmt.Printf("  %-20s  %-8s  %s%s\n", n.ID, n.Type, truncate(n.Title, 44), note)
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
	wip := wipSummary{Count: len(wipItems), AdvisoryLimit: wipGlobalAdvisoryLimit, PerSessionSoftLimit: wipPerSessionSoftLimit}
	for _, n := range wipItems {
		row := wipRow{ID: n.ID, Type: n.Type, Title: n.Title}
		// Only attach a rollup when one was actually measured (feat-f9118b9c)
		// — omitting the field for an unmeasured item, rather than emitting
		// a zero-valued ItemRollupSignal, is what keeps "no rollup" from
		// being indistinguishable from "measured clean" in the JSON output.
		if sig := workitem.RollupSignalFor(n); sig.Measured {
			row.Rollup = &sig
		}
		wip.Items = append(wip.Items, row)
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
