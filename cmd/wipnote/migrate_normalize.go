package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/migrate"
	"github.com/spf13/cobra"
)

// migrateNormalizePathsCmd wires `wipnote migrate normalize-paths` onto the
// parent migrate command. The command rewrites absolute host paths in stored
// artefacts to repo-relative form so existing data matches the shape produced
// by the runtime paths.NormalizeToRepoRelative path.
//
// Five of the rewriter's seven targets were columns in the per-project SQLite
// read index. That index is no longer persisted (feat-fc3cc9e0), so those five
// have nothing left to rewrite; the two canonical HTML targets do, and they are
// what this command still exists for. The library is still driven with a
// projection handle because its entry point takes one — an empty, process-local
// one, so the SQL passes report a truthful zero rather than a number produced
// against a throwaway copy of real data.
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
		Short: "Rewrite absolute host paths in .wipnote/ HTML to repo-relative form",
		Long: `Walk .wipnote/ HTML, rewriting absolute host paths (/Users/...,
/home/..., /workspaces/...) to repo-relative form across two targets:

  1. data-project-dir attribute in .wipnote/sessions/*.html
  2. affected_files property strings in .wipnote/{features,bugs,spikes}/*.html

Historically this also rewrote five columns of the per-project SQLite read
index (agent_events.tool_input, agent_events.input_summary,
feature_files.file_path, pending_subagent_starts.cwd, sessions.project_dir).
wipnote no longer keeps that index on disk, so those passes run against an
empty process-local projection and always report zero. They are kept in the
summary rather than hidden so the report matches the rewriter's contract.

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

	// Empty and process-local on purpose: see the command's doc comment. The
	// rewriter's SQL passes need a handle with the schema on it, and this one
	// carries no rows, so their reported counts are a truthful zero.
	database, err := dbpkg.OpenEphemeralProjection()
	if err != nil {
		return fmt.Errorf("open projection: %w", err)
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
