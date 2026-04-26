package main

import (
	"fmt"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/graph"
	"github.com/shakestzd/htmlgraph/internal/storage"
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
  htmlgraph query "features[status=todo]"
  htmlgraph query "tracks -> contains -> features"
  htmlgraph query "features[status=todo] -> blocked_by -> features[status=done]"

Supported types: features, bugs, spikes, tracks, plans, specs
Supported fields: status, type, priority, track_id`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runQuery(args[0])
		},
	}
}

func runQuery(dsl string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(dir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	results, err := graph.ExecuteDSL(database, dsl)
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
