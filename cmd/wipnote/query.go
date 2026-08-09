package main

import (
	"fmt"
	"strings"

	"github.com/shakestzd/wipnote/core/projection"
	"github.com/spf13/cobra"
)

func queryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   `query "<dsl-expression>"`,
		Short: "Query the link graph with a path expression",
		Long: `Execute a PathQuery DSL expression against the link graph.

Syntax:
  type[field=value] -> rel_type -> type[field=value]

Examples:
  wipnote query "features[status=todo]"
  wipnote query "tracks -> contains -> features"
  wipnote query "features[status=todo] -> blocked_by -> features[status=done]"

Supported types: features, bugs, spikes, tracks, plans, specs
Supported fields: status, type, priority, track_id`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runQuery(args[0])
		},
	}
}

func runQuery(dsl string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	snap, err := projection.Load(dir)
	if err != nil {
		return fmt.Errorf("load canonical projection: %w", err)
	}
	results, err := snap.ExecuteDSL(dsl)
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}

	if len(results) == 0 {
		fmt.Println("No results.")
		return nil
	}

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Query: %s\n", dsl)
	fmt.Printf("  Results: %d\n", len(results))
	fmt.Println(sep)
	for _, r := range results {
		status := r.Status
		if status == "" {
			status = "—"
		}
		nodeType := r.Type
		if nodeType == "" {
			nodeType = "—"
		}
		title := r.Title
		if title == "" {
			title = r.ID
		}
		fmt.Printf("  %-25s  [%-10s]  [%s]  %s\n",
			r.ID, status, nodeType, truncate(title, 40))
	}
	return nil
}
