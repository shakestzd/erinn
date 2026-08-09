package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/internal/migrate"
	"github.com/spf13/cobra"
)

// migrateRestorePathsCmd wires `wipnote migrate restore-paths --from <dir>`
// for rolling back a previous `migrate normalize-paths` run by copying every
// backed-up HTML file back into .wipnote/.
//
// The command refuses to restore over a clean working tree unless --force is
// passed — overwriting committed HTML without an intent to roll back is far
// more likely to be a mistake than a real recovery scenario.
func migrateRestorePathsCmd() *cobra.Command {
	var fromDir string
	var force bool

	cmd := &cobra.Command{
		Use:   "restore-paths",
		Short: "Restore HTML files from a previous normalize-paths backup",
		Long: `Copy every HTML file from a normalize-paths backup directory
(produced by --backup) back into .wipnote/ at its original location.

Refuses to run over a clean working tree without --force, since the most
likely caller is recovering from a regression that has not yet been
committed.

Example:
  wipnote migrate restore-paths --from .wipnote/.backup-20251231T230000Z/`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if fromDir == "" {
				return fmt.Errorf("--from <backup-dir> is required")
			}
			return runMigrateRestore(fromDir, force)
		},
	}
	cmd.Flags().StringVar(&fromDir, "from", "", "Backup directory produced by normalize-paths --backup")
	cmd.Flags().BoolVar(&force, "force", false, "Restore even when the working tree is clean")
	return cmd
}

func runMigrateRestore(fromDir string, force bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	printProjectHeaderIfDifferent(wipnoteDir)
	repoRoot := filepath.Dir(wipnoteDir)

	if !force {
		dirty, err := migrate.IsWorkingTreeDirty(repoRoot, runGitForMigrate)
		if err != nil {
			return fmt.Errorf("check working tree: %w", err)
		}
		if !dirty {
			return fmt.Errorf("refusing to restore over a clean working tree; pass --force to override")
		}
	}

	// Walk the backup directory and copy every regular file back into
	// .wipnote/ at the matching relative position.
	count := 0
	err = filepath.Walk(fromDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(fromDir, path)
		if relErr != nil {
			return relErr
		}
		// Skip hidden control files at the backup root (none currently
		// emitted, but reserved so future audit metadata doesn't get
		// copied over real HTML).
		if strings.HasPrefix(rel, ".") {
			return nil
		}
		dst := filepath.Join(wipnoteDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
		}
		if err := copyFile(path, dst); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk backup dir: %w", err)
	}
	fmt.Printf("Restored %d files from %s\n", count, fromDir)
	return nil
}
