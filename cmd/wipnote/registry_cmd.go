// Package main — registry subcommand for cross-project registry management.
//
// wipnote registry prune [--dry-run] [--since <duration>] [--tempdir-only] [--force]
//
// This is the spec-mandated entry point (bug-f0eff9b7). It wraps the same
// prune logic as `wipnote projects prune` but with safer defaults:
//
//   - Dry-run is the default — pass --force to write changes.
//   - The command name "registry" is what the bug spec calls for.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/internal/registry"
	"github.com/spf13/cobra"
)

// registryCmd returns the `wipnote registry` command tree.
func registryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Manage the cross-project project registry",
		Long: `Manage the cross-project project registry at ~/.local/share/wipnote/projects.json.

The registry is populated passively: every wipnote invocation inside a project
upserts that project's path. Use ` + "`registry prune`" + ` to remove stale entries.`,
	}
	cmd.AddCommand(registryPruneCmd())
	return cmd
}

func registryPruneCmd() *cobra.Command {
	var (
		pruneSince string
		force      bool
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove stale registry entries (dry-run by default)",
		Long: `Remove stale registry entries from ~/.local/share/wipnote/projects.json.

Default behavior: print what would be removed WITHOUT writing to disk (dry-run).
Pass --force to actually remove entries.

What counts as stale:
  - Entries whose .wipnote directory no longer exists on disk.
  - Entries whose project_dir is inside a temp directory and matches the
    Go test naming pattern (Test*) — accumulated by test subprocesses.

With --since: also remove entries last_seen older than the given duration.
  Accepts Go duration strings (e.g. 30m, 48h) or a "Nd" shorthand for N days.

Safe by design: valid projects (path exists and contains .wipnote/) are
never removed even with --force.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := registry.Load(defaultRegistryPath())
			if err != nil {
				return fmt.Errorf("load registry: %w", err)
			}

			entries := reg.List()
			var wouldRemove []registryPruneCandidate

			// Collect entries to remove: structural (missing .wipnote), tempdir,
			// and optional TTL.
			var (
				cutoff time.Time
				ttl    time.Duration
			)
			if pruneSince != "" {
				var terr error
				ttl, terr = parseDuration(pruneSince)
				if terr != nil {
					return fmt.Errorf("invalid --since value %q: %w", pruneSince, terr)
				}
				cutoff = time.Now().Add(-ttl)
			}

			for _, e := range entries {
				reason := registryPruneReason(e, cutoff)
				if reason != "" {
					wouldRemove = append(wouldRemove, registryPruneCandidate{entry: e, reason: reason})
				}
			}

			if len(wouldRemove) == 0 {
				if !force {
					fmt.Fprintln(cmd.OutOrStdout(), "dry-run: 0 entries would be pruned")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "0 entries pruned")
				}
				return nil
			}

			for _, c := range wouldRemove {
				if force {
					fmt.Fprintf(cmd.OutOrStdout(), "pruned (%s): %s\n", c.reason, c.entry.ProjectDir)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "would prune (%s): %s\n", c.reason, c.entry.ProjectDir)
				}
			}

			if !force {
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would prune %d entries — rerun with --force to apply\n", len(wouldRemove))
				return nil
			}

			// Apply: structural first, then tempdir, then TTL.
			reg.Prune()                       // removes missing .wipnote
			registry.PruneTempdirEntries(reg) // removes test temp dirs
			if ttl > 0 {
				registry.PruneStale(reg, ttl)
			}

			kept := len(reg.List())
			fmt.Fprintf(cmd.OutOrStdout(), "pruned %d entries, kept %d\n", len(wouldRemove), kept)
			return reg.SaveExact()
		},
	}

	cmd.Flags().StringVar(&pruneSince, "since", "", "also remove entries last_seen older than duration (e.g. 3d, 48h)")
	cmd.Flags().BoolVar(&force, "force", false, "apply changes; without this flag the command is a dry-run")
	return cmd
}

// registryPruneCandidate bundles an entry with the reason it would be pruned.
type registryPruneCandidate struct {
	entry  registry.Entry
	reason string
}

// registryPruneReason returns a non-empty string describing why e should be
// pruned, or "" when e should be kept. The cutoff is only checked when
// non-zero (i.e. --since was supplied).
func registryPruneReason(e registry.Entry, cutoff time.Time) string {
	// Structural: .wipnote dir missing.
	if _, err := os.Stat(filepath.Join(e.ProjectDir, ".wipnote")); err != nil {
		return "missing .wipnote"
	}
	// Tempdir: Go test temp path.
	if registry.IsGoTestTempDirPath(e.ProjectDir) {
		return "test tempdir"
	}
	// TTL: last_seen older than cutoff.
	if !cutoff.IsZero() {
		t, terr := time.Parse(time.RFC3339, e.LastSeen)
		if terr != nil || t.Before(cutoff) {
			return "stale (last_seen)"
		}
	}
	return ""
}
