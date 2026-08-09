package main

import (
	"fmt"
	"os"
	"path/filepath"

	corearch "github.com/shakestzd/wipnote/core/arch"
	"github.com/spf13/cobra"
)

// migrateCmd groups the one-shot data migrations that still have canonical
// artifacts to migrate.
//
// Two subcommands were removed in feat-fc3cc9e0 because their only input was
// the per-project SQLite file that no longer exists:
//
//   - `migrate sessions` rendered HTML for sessions that existed as SQLite rows
//     and nothing else. There is no such session: a session's canonical record
//     is its ledger entry and its HTML, both of which are written at session
//     start.
//   - `migrate attribution-fix` rewrote agent_events.agent_id rows misattributed
//     by a hook-environment bug fixed long before this change. agent_events is
//     rebuilt per process from canonical activity logs, so there is no durable
//     row left to rewrite.
//
// Neither was replaced. Reintroducing them as no-ops would leave two commands
// that print a success line and do nothing, which is the failure mode this
// whole change exists to remove.
func migrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "One-shot data migrations",
	}
	cmd.AddCommand(migrateNormalizePathsCmd())
	cmd.AddCommand(migrateRestorePathsCmd())
	cmd.AddCommand(migrateArchCardsCmd())
	return cmd
}

// migrateArchCardsCmd wires `wipnote migrate arch-cards` onto the parent
// migrate command. It ingests every .md arch card found under
// .wipnote/arch/ into the canonical architecture.html ledger and removes
// the migrated .md file. The operation is idempotent — cards already present
// in the ledger are skipped.
func migrateArchCardsCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "arch-cards",
		Short: "Ingest legacy arch markdown cards into architecture.html",
		Long: `Walk .wipnote/arch/*.md and migrate each card into the canonical
HTML ledger (architecture.html). Cards whose slug already exists in the
ledger are skipped (idempotent). On success the .md file is removed.

Run twice — the second run is a no-op.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrateArchCards(dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would happen but make no changes")
	return cmd
}

func runMigrateArchCards(dryRun bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	printProjectHeaderIfDifferent(wipnoteDir)
	migrated, skipped, errCount, err := runArchCardMigration(wipnoteDir, dryRun)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Printf("\nDry run: %d card(s) would be migrated\n", migrated)
		return nil
	}
	fmt.Printf("\nMigrated %d / skipped %d / errors %d\n", migrated, skipped, errCount)
	return nil
}

// runArchCardMigration is the reusable core for arch-card migration.
// It walks wipnoteDir/arch/*.md, ingests each card into the HTML ledger, and
// removes the .md file on success. It is called by both `wipnote migrate
// arch-cards` and `wipnote clean`. Returns per-item counts; progress lines are
// printed to stdout as each card is processed.
func runArchCardMigration(wipnoteDir string, dryRun bool) (migrated, skipped, errCount int, err error) {
	archDir := filepath.Join(wipnoteDir, "arch")
	entries, rdErr := os.ReadDir(archDir)
	if rdErr != nil {
		if os.IsNotExist(rdErr) {
			fmt.Println("  arch-cards: no .wipnote/arch/ directory — nothing to migrate.")
			return 0, 0, 0, nil
		}
		return 0, 0, 0, fmt.Errorf("read arch dir: %w", rdErr)
	}

	var mdFiles []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			mdFiles = append(mdFiles, filepath.Join(archDir, e.Name()))
		}
	}

	if len(mdFiles) == 0 {
		fmt.Println("  arch-cards: no .md arch cards found — nothing to migrate.")
		return 0, 0, 0, nil
	}

	// Build the set of slugs already in the ledger for idempotency.
	ledgerSlugs := make(map[string]bool)
	if existing, readErr := corearch.ReadLedger(corearch.LedgerPath(wipnoteDir)); readErr == nil {
		for _, c := range existing {
			ledgerSlugs[c.Name] = true
		}
	}

	store, openErr := corearch.NewStore(wipnoteDir)
	if openErr != nil {
		return 0, 0, 0, fmt.Errorf("open arch store: %w", openErr)
	}

	for _, path := range mdFiles {
		card, parseErr := corearch.ParseFile(path)
		if parseErr != nil {
			errCount++
			fmt.Printf("  arch-cards  %s: ERROR parsing: %v\n", filepath.Base(path), parseErr)
			continue
		}

		// Emit advisory path warnings (non-blocking).
		if ws := corearch.ValidatePaths(card.Paths); len(ws) > 0 {
			for _, w := range ws {
				fmt.Printf("  arch-cards  %s: WARNING %s\n", card.Name, w)
			}
		}

		if ledgerSlugs[card.Name] {
			skipped++
			fmt.Printf("  arch-cards  %s: skipped (already in ledger)\n", card.Name)
			continue
		}

		if dryRun {
			fmt.Printf("  arch-cards  %s: would-migrate -> architecture.html\n", card.Name)
			migrated++
			continue
		}

		// Remove the .md BEFORE store.Create so the store's duplicate-file
		// check does not mistake the source .md for a pre-existing card.
		mdContent, readErr := os.ReadFile(path)
		if readErr != nil {
			errCount++
			fmt.Printf("  arch-cards  %s: ERROR reading .md for backup: %v\n", card.Name, readErr)
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			errCount++
			fmt.Printf("  arch-cards  %s: ERROR removing .md before create: %v\n", card.Name, removeErr)
			continue
		}

		createErr := store.Create(card)
		if createErr != nil {
			// Restore the .md so the operator can retry.
			if restoreErr := os.WriteFile(path, mdContent, 0o644); restoreErr != nil {
				fmt.Printf("  arch-cards  %s: ERROR %v (also failed to restore .md: %v)\n", card.Name, createErr, restoreErr)
			} else {
				fmt.Printf("  arch-cards  %s: ERROR %v (.md restored)\n", card.Name, createErr)
			}
			errCount++
			continue
		}

		migrated++
		ledgerSlugs[card.Name] = true
		fmt.Printf("  arch-cards  %s: migrated\n", card.Name)
	}

	return migrated, skipped, errCount, nil
}
