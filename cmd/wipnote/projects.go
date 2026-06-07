// Package main — projects subcommand for cross-project registry management.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shakestzd/wipnote/internal/registry"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

// defaultRegistryPath is an indirection so tests can point the projects
// commands at a tmpdir registry without touching the real user's home.
var defaultRegistryPath = registry.DefaultPath

// projectsCmd returns the `wipnote projects` command tree.
func projectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Manage the cross-project registry",
		Long: `Manage the cross-project registry at ~/.local/share/wipnote/projects.json.

The registry is populated passively: every wipnote invocation inside a
project upserts that project into the registry. Use ` + "`projects list`" + ` to
see all known projects and ` + "`projects prune`" + ` to remove stale entries.`,
	}
	cmd.AddCommand(projectsListCmd(), projectsPruneCmd())
	return cmd
}

func projectsListCmd() *cobra.Command {
	var goneOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all known projects in the registry",
		Long: `List all known projects in the registry.

With --gone, show only orphan entries whose .wipnote directory no longer exists.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := registry.Load(defaultRegistryPath())
			if err != nil {
				return fmt.Errorf("load registry: %w", err)
			}
			entries := reg.List()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tDIR\tLAST_SEEN\tSTATUS\tITEMS")
			printed := 0
			for _, e := range entries {
				alive := false
				items := "-"
				hgDir := filepath.Join(e.ProjectDir, ".wipnote")
				if _, statErr := os.Stat(hgDir); statErr == nil {
					alive = true
					if dbPath, pathErr := storage.CanonicalDBPath(e.ProjectDir); pathErr == nil {
						if db, openErr := registry.OpenReadOnly(dbPath); openErr == nil {
							items = countItems(db)
							db.Close()
						}
					}
				}
				if goneOnly && alive {
					continue
				}
				status := "missing"
				if alive {
					status = "exists"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name, e.ProjectDir, e.LastSeen, status, items)
				printed++
			}
			if printed == 0 {
				if goneOnly {
					fmt.Fprintln(w, "(no orphan projects)")
				} else {
					fmt.Fprintln(w, "(no projects registered)")
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&goneOnly, "gone", false, "show only orphan entries whose .wipnote directory no longer exists")
	return cmd
}

func projectsPruneCmd() *cobra.Command {
	var (
		pruneSince       string
		pruneTempdirOnly bool
		pruneDryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove stale registry entries",
		Long: `Remove stale registry entries from ~/.local/share/wipnote/projects.json.

Default behavior (no flags): remove entries whose .wipnote directory no longer exists.

With --since: also remove entries last_seen older than the given duration.
  Accepts Go duration strings (e.g. 30m, 48h) or a "Nd" shorthand for N days.

With --tempdir-only: remove only entries whose project_dir is inside the OS temp
  directory and matches the Go test naming pattern (Test*), regardless of last_seen.

With --dry-run: print what would be removed without writing to disk.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := registry.Load(defaultRegistryPath())
			if err != nil {
				return fmt.Errorf("load registry: %w", err)
			}

			// --tempdir-only: remove only test tempdir entries.
			if pruneTempdirOnly {
				return pruneTempdirOnlyAction(cmd, reg, pruneDryRun)
			}

			// --since: TTL-based prune (with optional dry-run).
			if pruneSince != "" {
				return pruneWithSinceAction(cmd, reg, pruneSince, pruneDryRun)
			}

			// Default: remove entries whose .wipnote directory no longer exists.
			if pruneDryRun {
				// Dry-run structural prune: report what would be removed.
				var wouldRemove []string
				for _, e := range reg.List() {
					hgDir := filepath.Join(e.ProjectDir, ".wipnote")
					if _, statErr := os.Stat(hgDir); statErr != nil {
						wouldRemove = append(wouldRemove, e.ProjectDir)
					}
				}
				for _, p := range wouldRemove {
					fmt.Fprintln(cmd.OutOrStdout(), "would prune:", p)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would prune %d entries\n", len(wouldRemove))
				return nil
			}

			before := len(reg.List())
			pruned := reg.Prune()
			kept := before - len(pruned)
			for _, p := range pruned {
				fmt.Fprintln(cmd.OutOrStdout(), "pruned:", p)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pruned %d stale projects, kept %d\n", len(pruned), kept)
			if len(pruned) == 0 {
				return nil
			}
			return reg.Save()
		},
	}
	cmd.Flags().StringVar(&pruneSince, "since", "", "remove entries last_seen older than this duration (e.g. 3d, 48h, 30m)")
	cmd.Flags().BoolVar(&pruneTempdirOnly, "tempdir-only", false, "remove only entries inside OS temp dir matching Go test naming pattern (Test*)")
	cmd.Flags().BoolVar(&pruneDryRun, "dry-run", false, "print what would be removed without writing to disk")
	return cmd
}

// parseDuration parses a duration string supporting the "Nd" shorthand for days
// in addition to standard Go duration strings (e.g. 48h, 30m).
func parseDuration(s string) (time.Duration, error) {
	// Handle "Nd" shorthand where N is an integer.
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("expected integer before 'd', got %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// structuralPrune removes entries whose .wipnote directory is missing and
// returns the list of removed ProjectDir values. The registry is mutated.
func structuralPrune(reg *registry.Registry) []string {
	return reg.Prune()
}

// pruneWithSinceAction handles the --since flag path: applies both structural
// prune and TTL prune, printing what was removed (or would be in dry-run).
func pruneWithSinceAction(cmd *cobra.Command, reg *registry.Registry, since string, dryRun bool) error {
	ttl, err := parseDuration(since)
	if err != nil {
		return fmt.Errorf("invalid --since value %q: %w", since, err)
	}

	allBefore := reg.List()
	cutoff := time.Now().Add(-ttl)

	var structuralRemove []string
	var ttlRemove []string

	// First pass: collect both structural and TTL removals
	for _, e := range allBefore {
		// Structural check: .wipnote directory missing
		if _, err := os.Stat(filepath.Join(e.ProjectDir, ".wipnote")); err != nil {
			structuralRemove = append(structuralRemove, e.ProjectDir)
			continue
		}

		// TTL check: last_seen older than cutoff
		t, terr := time.Parse(time.RFC3339, e.LastSeen)
		if terr != nil || t.Before(cutoff) {
			ttlRemove = append(ttlRemove, e.ProjectDir)
		}
	}

	if dryRun {
		if len(structuralRemove) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "would prune %d missing .wipnote dirs:\n", len(structuralRemove))
			for _, p := range structuralRemove {
				fmt.Fprintln(cmd.OutOrStdout(), "  would prune (missing .wipnote):", p)
			}
		}
		if len(ttlRemove) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "would prune %d stale entries (older than %s):\n", len(ttlRemove), since)
			for _, p := range ttlRemove {
				fmt.Fprintln(cmd.OutOrStdout(), "  would prune (stale):", p)
			}
		}
		total := len(structuralRemove) + len(ttlRemove)
		fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would prune %d entries total\n", total)
		return nil
	}

	// Apply both prunes: structural first, then TTL
	structuralCount := len(structuralPrune(reg))
	ttlCount := registry.PruneStale(reg, ttl)
	totalRemoved := structuralCount + ttlCount
	kept := len(reg.List())

	if structuralCount > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "pruned %d entries with missing .wipnote\n", structuralCount)
	}
	if ttlCount > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "pruned %d stale entries (older than %s)\n", ttlCount, since)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "pruned %d entries total, kept %d\n", totalRemoved, kept)

	if totalRemoved == 0 {
		return nil
	}
	return reg.SaveExact()
}

// pruneTempdirOnlyAction handles the --tempdir-only flag path.
func pruneTempdirOnlyAction(cmd *cobra.Command, reg *registry.Registry, dryRun bool) error {
	if dryRun {
		var wouldRemove []string
		for _, e := range reg.List() {
			if registry.IsGoTestTempDirPath(e.ProjectDir) {
				wouldRemove = append(wouldRemove, e.ProjectDir)
			}
		}
		for _, p := range wouldRemove {
			fmt.Fprintln(cmd.OutOrStdout(), "would prune:", p)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would prune %d tempdir entries\n", len(wouldRemove))
		return nil
	}

	removed := registry.PruneTempdirEntries(reg)
	kept := len(reg.List())
	fmt.Fprintf(cmd.OutOrStdout(), "pruned %d tempdir entries, kept %d\n", removed, kept)
	if removed == 0 {
		return nil
	}
	return reg.SaveExact()
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
