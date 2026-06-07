package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/migrate"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

// migrateNormalizePathsCmd wires `wipnote migrate normalize-paths` onto the
// parent migrate command. The command rewrites absolute host paths in seven
// stored artefacts to repo-relative form so existing data matches the shape
// produced by the runtime paths.NormalizeToRepoRelative path.
//
// See internal/migrate/normalize.go for the rewriter contract and the
// per-target rules. The command is intentionally a thin shell — all logic
// lives in the library so tests can exercise it without spinning up cobra.
func migrateNormalizePathsCmd() *cobra.Command {
	var dryRun bool
	var allowDirty bool
	var noMerge bool
	var backup bool

	cmd := &cobra.Command{
		Use:   "normalize-paths",
		Short: "Rewrite absolute host paths in .wipnote/ to repo-relative form",
		Long: `Walk .wipnote/ HTML and the SQLite read index, rewriting absolute
host paths (/Users/..., /home/..., /workspaces/...) to repo-relative form
across seven targets:

  1. agent_events.tool_input — single-encoded JSON; path keys re-marshalled
  2. agent_events.input_summary — free-text embeds rewritten in place
  3. feature_files.file_path — collision-aware (default merge; --no-merge-collisions to abort)
  4. pending_subagent_starts.cwd
  5. sessions.project_dir
  6. data-project-dir attribute in .wipnote/sessions/*.html
  7. affected_files property strings in .wipnote/{features,bugs,spikes}/*.html

Out of scope:
  • sessions.transcript_path — Claude's private machine-local store
  • .wipnote/.active-session — transient per-session JSON

Safety preconditions:
  • Clean working tree under .wipnote/ — pass --allow-dirty to override
  • Re-running on already-relative records is a no-op (idempotent)

Companion:
  wipnote migrate restore-paths --from .wipnote/.backup-<timestamp>/`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrateNormalize(dryRun, allowDirty, noMerge, backup)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print proposed changes without writing")
	cmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "Bypass the clean-working-tree precondition")
	cmd.Flags().BoolVar(&noMerge, "no-merge-collisions", false, "Abort on feature_files collisions instead of merging")
	cmd.Flags().BoolVar(&backup, "backup", true, "Copy touched HTML files to .wipnote/.backup-<timestamp>/ before rewriting")
	return cmd
}

func runMigrateNormalize(dryRun, allowDirty, noMerge, backup bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	printProjectHeaderIfDifferent(wipnoteDir)
	repoRoot := filepath.Dir(wipnoteDir)

	// Safety precondition: clean working tree under .wipnote/.
	if !allowDirty {
		dirty, err := migrate.IsWorkingTreeDirty(repoRoot, runGitForMigrate)
		if err != nil {
			return fmt.Errorf("check working tree: %w", err)
		}
		if dirty {
			return fmt.Errorf("working tree dirty; pass --allow-dirty to bypass")
		}
	}

	dbPath, err := storage.CanonicalDBPath(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	opts := migrate.NormalizeOptions{
		RepoRoot:          repoRoot,
		DryRun:            dryRun,
		AllowDirty:        allowDirty,
		NoMergeCollisions: noMerge,
		Backup:            backup && !dryRun,
		BackupTimestamp:   time.Now().UTC().Format("20060102T150405Z"),
	}

	summary, err := migrate.NormalizePaths(database, opts)
	if err != nil {
		// Print the partial summary so the operator sees what was found
		// before the abort. The summary always includes the collision
		// list, which is the actionable signal for --no-merge mode.
		fmt.Print(summary.Format())
		if len(summary.Collisions) > 0 {
			fmt.Println("\nfeature_files collisions:")
			for _, c := range summary.Collisions {
				fmt.Printf("  feature=%s  %s + %s -> %s\n",
					c.FeatureID, c.BeforeA, c.BeforeB, c.After)
			}
		}
		return err
	}

	if dryRun {
		fmt.Println("Dry-run mode — no files were written, no DB rows were modified.")
		if len(summary.Proposals) > 0 {
			fmt.Println("\nProposed changes:")
			for _, p := range summary.Proposals {
				fmt.Printf("  %-32s %s -> %s\n", p.Target, p.Before, p.After)
			}
		}
	}
	if allowDirty {
		summary.AllowDirtyOverride = true
	}
	fmt.Print(summary.Format())
	return nil
}

// runGitForMigrate is the production gitRunner used by IsWorkingTreeDirty.
// Split out so tests can substitute a stub via the var below.
var runGitForMigrate = func(repoRoot string, args ...string) (string, error) {
	full := append([]string{"-C", repoRoot}, args...)
	out, err := exec.Command("git", full...).Output()
	return string(out), err
}
