package main

import (
	"fmt"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/storage"
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
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(dir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	// bug-7dbaf552: `wipnote query` is strictly read-only — graph.ExecuteDSL
	// only issues SELECTs and closes every *sql.Rows it opens internally
	// before returning a materialised slice. OpenReadOnlyMigrated bootstraps
	// (writable Open → schema/migrations, RetryOnBusy-safe) then hands back a
	// mode=ro handle so the long query path can never hold the writer lock.
	// roborev followup: the bare OpenReadOnly here previously regressed fresh
	// / schema-behind workspaces (mode=ro never creates or migrates) — the
	// migrated helper restores that guarantee while keeping the contention
	// benefit. ExecuteDSL stays wrapped in RetryOnBusy (no-leaked-handle).
	database, err := dbpkg.OpenReadOnlyMigrated(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	var results []graph.NodeResult
	if err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
		r, derr := graph.ExecuteDSL(database, archSourceFor(dir), dsl)
		if derr != nil {
			return derr
		}
		results = r
		return nil
	}); err != nil {
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
