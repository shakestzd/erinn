// Package main — projects subcommand for cross-project registry management.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/shakestzd/htmlgraph/internal/registry"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

// defaultRegistryPath is an indirection so tests can point the projects
// commands at a tmpdir registry without touching the real user's home.
var defaultRegistryPath = registry.DefaultPath

// projectsCmd returns the `htmlgraph projects` command tree.
func projectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Manage the cross-project registry",
		Long: `Manage the cross-project registry at ~/.local/share/htmlgraph/projects.json.

The registry is populated passively: every htmlgraph invocation inside a
project upserts that project into the registry. Use ` + "`projects list`" + ` to
see all known projects and ` + "`projects prune`" + ` to remove stale entries.`,
	}
	cmd.AddCommand(projectsListCmd(), projectsPruneCmd())
	return cmd
}

func projectsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all known projects in the registry",
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := registry.Load(defaultRegistryPath())
			if err != nil {
				return fmt.Errorf("load registry: %w", err)
			}
			entries := reg.List()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tDIR\tLAST_SEEN\tSTATUS\tITEMS")
			for _, e := range entries {
				status := "missing"
				items := "-"
				hgDir := filepath.Join(e.ProjectDir, ".htmlgraph")
				if _, statErr := os.Stat(hgDir); statErr == nil {
					status = "exists"
					if dbPath, pathErr := storage.CanonicalDBPath(e.ProjectDir); pathErr == nil {
						if db, openErr := registry.OpenReadOnly(dbPath); openErr == nil {
							items = countItems(db)
							db.Close()
						}
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name, e.ProjectDir, e.LastSeen, status, items)
			}
			if len(entries) == 0 {
				fmt.Fprintln(w, "(no projects registered)")
			}
			return w.Flush()
		},
	}
}

func projectsPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Remove registry entries whose .htmlgraph directory no longer exists",
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := registry.Load(defaultRegistryPath())
			if err != nil {
				return fmt.Errorf("load registry: %w", err)
			}
			pruned := reg.Prune()
			for _, p := range pruned {
				fmt.Fprintln(cmd.OutOrStdout(), "pruned:", p)
			}
			if len(pruned) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(nothing to prune)")
				return nil
			}
			return reg.Save()
		},
	}
}

// countItems returns a compact summary of feature/bug/spike counts in the
// given project DB. Failures (missing tables, query errors) return "-" so
// the list view stays usable even for partially-initialised project DBs.
func countItems(db *sql.DB) string {
	var features, bugs, spikes int
	row := db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN type = 'feature' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN type = 'bug'     THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN type = 'spike'   THEN 1 ELSE 0 END), 0)
		FROM features`)
	if err := row.Scan(&features, &bugs, &spikes); err != nil {
		return "-"
	}
	return fmt.Sprintf("%df %db %ds", features, bugs, spikes)
}
