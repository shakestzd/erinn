package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// knownDeadArtifacts lists wipnote-generated files that are safe to delete
// automatically. Each entry must be well-understood as wipnote-generated with
// no user value — when in doubt, add to unknownOrphans instead.
var knownDeadArtifacts = []string{
	"refs.json", // scaffolded until feat-6cacaaa4; now obsolete
}

// unknownOrphans are paths (relative to .wipnote/) that may exist but should
// only be reported, never deleted. They may contain user data in foreign repos.
var unknownOrphans = []struct {
	path  string
	label string
}{
	{"research", "research/ directory"},
	{"agents.json", "agents.json"},
}

// unknownOrphanPrefixes lists dir-name prefixes that warrant a report-only
// notice when found directly under .wipnote/.
var unknownOrphanPrefixes = []string{
	".pre-merge-backup-",
}

// cleanResult holds the disposition of a single scanned item.
type cleanResult struct {
	path        string // relative to .wipnote
	disposition string // "would-remove" | "removed" | "report-only" | "not-found"
	label       string // human description
}

func cleanCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Report and optionally remove stale wipnote artifacts",
		Long: `Scan the local .wipnote/ directory for stale artifacts and report
what would be changed. Dry-run by default — pass --apply to execute.

Steps performed:
  1. Migrate legacy arch-card .md files into the HTML ledger.
  2. Remove known-dead wipnote-generated files (e.g. refs.json).
  3. Report unknown orphans for manual review (never deleted automatically).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runClean(apply)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "execute all mutations (default is dry-run)")
	return cmd
}

func runClean(apply bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	// Verify the resolved .wipnote directory actually exists. findWipnoteDir
	// can fall back to cwd/.wipnote even when no such directory is present,
	// causing clean to report success outside any wipnote checkout.
	if info, statErr := os.Stat(wipnoteDir); statErr != nil || !info.IsDir() {
		return fmt.Errorf(".wipnote directory not found at %s: not inside a wipnote project", wipnoteDir)
	}
	printProjectHeaderIfDifferent(wipnoteDir)

	dryRun := !apply
	if dryRun {
		fmt.Println("Dry run — pass --apply to execute changes.")
		fmt.Println()
	}

	// Step 1: arch-card migration.
	fmt.Println("Step 1: arch-card migration")
	migrated, skipped, errCount, err := runArchCardMigration(wipnoteDir, dryRun)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Printf("  summary: %d would-migrate / %d skipped / %d errors\n", migrated, skipped, errCount)
	} else {
		fmt.Printf("  summary: %d migrated / %d skipped / %d errors\n", migrated, skipped, errCount)
	}
	fmt.Println()

	// Step 2: known-dead artifacts.
	fmt.Println("Step 2: known-dead artifacts")
	var deadResults []cleanResult
	for _, name := range knownDeadArtifacts {
		full := filepath.Join(wipnoteDir, name)
		r := cleanResult{path: name, label: name}
		if _, statErr := os.Stat(full); os.IsNotExist(statErr) {
			r.disposition = "not-found"
		} else if dryRun {
			r.disposition = "would-remove"
		} else {
			if rmErr := os.Remove(full); rmErr != nil && !os.IsNotExist(rmErr) {
				fmt.Printf("  ERROR removing %s: %v\n", name, rmErr)
				r.disposition = "error"
			} else {
				r.disposition = "removed"
			}
		}
		deadResults = append(deadResults, r)
		if r.disposition != "not-found" {
			fmt.Printf("  %-14s  %s\n", r.disposition, r.path)
		}
	}
	if allNotFound(deadResults) {
		fmt.Println("  (no known-dead artifacts present)")
	}
	fmt.Println()

	// Step 3: unknown orphans — report only, never delete.
	fmt.Println("Step 3: review manually (wipnote will not delete these)")
	var reportItems []string

	for _, o := range unknownOrphans {
		full := filepath.Join(wipnoteDir, o.path)
		if _, statErr := os.Stat(full); !os.IsNotExist(statErr) {
			reportItems = append(reportItems, fmt.Sprintf("  report-only     %s  (%s)", o.path, o.label))
		}
	}

	// Scan for .pre-merge-backup-* dirs.
	entries, _ := os.ReadDir(wipnoteDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, prefix := range unknownOrphanPrefixes {
			if len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix {
				reportItems = append(reportItems, fmt.Sprintf("  report-only     %s  (pre-merge backup dir)", e.Name()))
			}
		}
	}

	if len(reportItems) == 0 {
		fmt.Println("  (no unknown orphans detected)")
	} else {
		for _, line := range reportItems {
			fmt.Println(line)
		}
	}
	fmt.Println()

	// Summary footer.
	printCleanSummary(dryRun, migrated, skipped, errCount, deadResults, reportItems)

	// Count removal errors in --apply mode and propagate as a non-nil error so
	// scripts and callers see the failure (per-item lines are already printed above).
	if apply {
		var deadErrCount int
		for _, r := range deadResults {
			if r.disposition == "error" {
				deadErrCount++
			}
		}
		if deadErrCount > 0 {
			return fmt.Errorf("clean: %d known-dead artifact(s) could not be removed", deadErrCount)
		}
	}
	return nil
}

// allNotFound returns true when every result has disposition "not-found".
func allNotFound(results []cleanResult) bool {
	for _, r := range results {
		if r.disposition != "not-found" {
			return false
		}
	}
	return true
}

// printCleanSummary prints the footer counts and a dry-run hint when applicable.
func printCleanSummary(dryRun bool, archMigrated, archSkipped, archErrors int, dead []cleanResult, reportOnly []string) {
	deadCount := 0
	for _, r := range dead {
		if r.disposition == "would-remove" || r.disposition == "removed" {
			deadCount++
		}
	}

	verb := "removed"
	if dryRun {
		verb = "would-remove"
	}
	fmt.Printf("Summary: arch-cards %s=%d skipped=%d errors=%d | dead-artifacts %s=%d | report-only=%d\n",
		verb, archMigrated, archSkipped, archErrors, verb, deadCount, len(reportOnly))

	if dryRun {
		fmt.Println()
		fmt.Println("Hint: run `wipnote clean --apply` to execute the changes above.")
	}
}
