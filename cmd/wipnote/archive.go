package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/internal/migrate"
)

// defaultArchiveAgeDays is the minimum age (since last update / completion) a
// DONE work item must reach before it is eligible for archiving. Recent items
// stay as individual files where they are cheapest to inspect and edit.
const defaultArchiveAgeDays = 30

// archiveCandidate is a DONE work item eligible for compaction into a ledger.
type archiveCandidate struct {
	id        string
	filePath  string // absolute path to the individual .wipnote HTML file
	node      *models.Node
	html      string // verbatim original file content (lossless payload)
	updatedAt time.Time
}

func archiveCmd() *cobra.Command {
	var apply bool
	var dryRun bool
	var olderThanDays int

	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Compact old DONE work items into HTML table-ledger artifacts",
		Long: `Compact completed (status=done) work items that have been idle for a
threshold period out of individual .wipnote/<type>s/*.html files into a single
type-specific HTML table ledger at .wipnote/archive/<type>s.html.

Archived rows stay CANONICAL: reindex reads them back and they remain fully
queryable (list/find/show), and their lineage edges are preserved. Active,
todo, in-progress, and recently-completed items keep their individual files.

Dry-run by default — pass --apply to execute. Refuses to run on a dirty git
tree so the move (write ledger + remove files + commit) is auditable as one
reviewable change. Idempotent: re-running archives only newly-eligible items.

Slice 1 archives FEATURES only.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			// --dry-run is an explicit affirmation of the default; --apply opts in
			// to execution. --apply wins if both are somehow set.
			doApply := apply && !dryRun
			return runArchive(doApply, olderThanDays)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "execute the archive (default is dry-run)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview only; never modify files (the default)")
	cmd.Flags().IntVar(&olderThanDays, "older-than", defaultArchiveAgeDays,
		"only archive DONE items idle for at least this many days")
	return cmd
}

func runArchive(apply bool, olderThanDays int) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(wipnoteDir); statErr != nil || !info.IsDir() {
		return fmt.Errorf(".wipnote directory not found at %s: not inside a wipnote project", wipnoteDir)
	}
	printProjectHeaderIfDifferent(wipnoteDir)

	repoRoot := filepath.Dir(wipnoteDir)

	// Refuse on a dirty tree (apply mode only): the archive performs a multi-file
	// canonical mutation (rewrite ledger + delete individual files) that must land
	// as one reviewable commit. Running it over existing uncommitted edits would
	// entangle unrelated changes. Dry-run never mutates, so it is always allowed.
	if apply {
		if !isGitRepo(repoRoot) {
			return fmt.Errorf("archive --apply requires a git repository at %s", repoRoot)
		}
		// Reuse the shared .wipnote-scoped dirty check used by migrate
		// normalize/restore — refusing only when .wipnote/ itself has
		// uncommitted changes that the archive commit would entangle.
		dirty, derr := migrate.IsWorkingTreeDirty(repoRoot, runGitForMigrate)
		if derr != nil {
			return fmt.Errorf("check git status: %w", derr)
		}
		if dirty {
			return fmt.Errorf("refusing to archive: .wipnote/ has uncommitted changes — commit or stash them first")
		}
	}

	if !apply {
		fmt.Println("Dry run — pass --apply to execute the archive.")
		fmt.Println()
	}

	cutoff := time.Now().Add(-time.Duration(olderThanDays) * 24 * time.Hour)
	totalArchived := 0
	for _, col := range graph.ArchiveLedgerCollections {
		n, archErr := archiveCollection(wipnoteDir, col, cutoff, apply)
		if archErr != nil {
			return archErr
		}
		totalArchived += n
	}

	fmt.Println()
	verb := "would archive"
	if apply {
		verb = "archived"
	}
	fmt.Printf("Summary: %s %d work item(s) (threshold: idle ≥ %d days)\n", verb, totalArchived, olderThanDays)
	if !apply && totalArchived > 0 {
		fmt.Println()
		fmt.Println("Hint: run `wipnote archive --apply` to execute the changes above.")
	}
	if apply && totalArchived > 0 {
		fmt.Println()
		fmt.Println("Run `wipnote reindex --full` to refresh the read index, or it will lazy-rebuild on next query.")
	}
	return nil
}

// archiveCollection finds eligible DONE items in one collection, prints what it
// will do, and (when apply) writes them into the ledger and removes the
// individual files. Returns the number of items archived (or that would be).
func archiveCollection(wipnoteDir, collectionDir string, cutoff time.Time, apply bool) (int, error) {
	candidates, err := collectArchiveCandidates(wipnoteDir, collectionDir, cutoff)
	if err != nil {
		return 0, err
	}

	fmt.Printf("Collection: %s\n", collectionDir)
	if len(candidates) == 0 {
		fmt.Println("  (no eligible DONE items)")
		return 0, nil
	}
	for _, c := range candidates {
		disp := "would-archive"
		if apply {
			disp = "archive"
		}
		fmt.Printf("  %-14s  %s  %s  (idle since %s)\n",
			disp, c.id, truncate(c.node.Title, 40), c.updatedAt.Format("2006-01-02"))
	}

	if !apply {
		return len(candidates), nil
	}

	ledgerPath := graph.ArchiveLedgerPath(wipnoteDir, collectionDir)
	if err := appendToLedger(ledgerPath, candidates); err != nil {
		return 0, err
	}
	// Only after the ledger write succeeds do we remove the individual files —
	// canonical data is never momentarily absent (ledger first, files second).
	for _, c := range candidates {
		if rmErr := os.Remove(c.filePath); rmErr != nil && !os.IsNotExist(rmErr) {
			return 0, fmt.Errorf("remove archived file %s: %w", c.filePath, rmErr)
		}
	}

	// Commit ledger + removals as one reviewable change. The .wipnote dir may be
	// gitignored in conductor worktrees, so stage with an explicit absolute path
	// (mirrors commitWipnoteArtifact's bypass of the exclusion).
	// Belt-and-suspenders: force-add the ledger file to ensure it can never be
	// silently dropped by a gitignore rule, even if the pattern reappears.
	repoRoot := filepath.Dir(wipnoteDir)
	if isGitRepo(repoRoot) && !isTestTmpPath(wipnoteDir) {
		collectionPath := filepath.Join(wipnoteDir, collectionDir)
		msg := fmt.Sprintf("wipnote: archive %d %s into ledger", len(candidates), collectionDir)
		if _, cErr := runGitMutationBatch(repoRoot,
			[]string{"add", "-f", ledgerPath},
			[]string{"add", "--", collectionPath},
			[]string{"commit", "-m", msg, "--", ledgerPath, collectionPath},
		); cErr != nil {
			fmt.Fprintf(stderr, "archive warning: git commit failed (changes persisted to disk — commit manually): %v\n", cErr)
		}
	}
	return len(candidates), nil
}

// collectArchiveCandidates returns DONE items in collectionDir whose last update
// is at or before cutoff. Items already represented in the ledger are skipped so
// the operation is idempotent.
func collectArchiveCandidates(wipnoteDir, collectionDir string, cutoff time.Time) ([]archiveCandidate, error) {
	already, err := ledgerIDSet(graph.ArchiveLedgerPath(wipnoteDir, collectionDir))
	if err != nil {
		return nil, err
	}

	pattern := filepath.Join(wipnoteDir, collectionDir, "*.html")
	files, _ := filepath.Glob(pattern)

	var out []archiveCandidate
	for _, f := range files {
		raw, readErr := os.ReadFile(f)
		if readErr != nil {
			continue
		}
		node, parseErr := htmlparse.ParseString(string(raw))
		if parseErr != nil || node.ID == "" {
			continue
		}
		if already[node.ID] {
			continue
		}
		if node.Status != models.StatusDone {
			continue
		}
		updated := node.UpdatedAt
		if updated.IsZero() {
			updated = node.CreatedAt
		}
		if updated.IsZero() || updated.After(cutoff) {
			continue
		}
		out = append(out, archiveCandidate{
			id:        node.ID,
			filePath:  f,
			node:      node,
			html:      string(raw),
			updatedAt: updated,
		})
	}
	return out, nil
}

// appendToLedger merges new candidates into the existing ledger (read-modify-
// write) and rewrites it atomically. Existing rows are preserved; a candidate
// whose ID already exists is overwritten with the fresh payload.
func appendToLedger(ledgerPath string, candidates []archiveCandidate) error {
	existing, err := graph.ReadLedger(ledgerPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ledger %s: %w", ledgerPath, err)
	}

	byID := make(map[string]*graph.LedgerEntry, len(existing)+len(candidates))
	for _, e := range existing {
		byID[e.ID] = e
	}
	now := time.Now().UTC()
	for _, c := range candidates {
		byID[c.id] = &graph.LedgerEntry{
			ID:         c.node.ID,
			Type:       c.node.Type,
			Title:      c.node.Title,
			Status:     string(c.node.Status),
			Priority:   string(c.node.Priority),
			TrackID:    c.node.TrackID,
			CreatedBy:  c.node.CreatedByAgent,
			CreatedAt:  c.node.CreatedAt,
			UpdatedAt:  c.node.UpdatedAt,
			ArchivedAt: now,
			HTML:       c.html,
		}
	}

	merged := make([]*graph.LedgerEntry, 0, len(byID))
	for _, e := range byID {
		merged = append(merged, e)
	}
	return graph.WriteLedger(ledgerPath, merged)
}

// ledgerIDSet returns the set of work-item IDs already present in the ledger at
// path. A missing ledger yields an empty set with no error.
func ledgerIDSet(path string) (map[string]bool, error) {
	entries, err := graph.ReadLedger(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("read ledger %s: %w", path, err)
	}
	set := make(map[string]bool, len(entries))
	for _, e := range entries {
		set[e.ID] = true
	}
	return set, nil
}

// resolveArchivedNode returns the reconstructed node for id if it lives in an
// archive ledger (and not as an individual file). Returns (nil, nil) when id is
// not archived. Read commands (show/find) call this as a fallback so archived
// items stay queryable by ID even though their standalone file is gone.
func resolveArchivedNode(wipnoteDir, id string) (*models.Node, error) {
	for _, col := range graph.ArchiveLedgerCollections {
		entries, err := graph.ReadLedger(graph.ArchiveLedgerPath(wipnoteDir, col))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.ID == id {
				return e.Node()
			}
		}
	}
	return nil, nil
}

// resolveArchivedNodeByPartialID resolves an exact OR unambiguous-prefix id
// against archived ledger rows and returns the reconstructed node. This mirrors
// workitem.ResolvePartialID (which only scans individual files) for the archive
// case, so `show <partial-or-full-id>` works for archived items. Returns
// (nil, nil) when nothing matches; an error when the prefix is ambiguous.
func resolveArchivedNodeByPartialID(wipnoteDir, id string) (*models.Node, error) {
	var matches []*graph.LedgerEntry
	for _, col := range graph.ArchiveLedgerCollections {
		entries, err := graph.ReadLedger(graph.ArchiveLedgerPath(wipnoteDir, col))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.ID == id {
				return e.Node() // exact match wins immediately
			}
			if strings.HasPrefix(e.ID, id) {
				matches = append(matches, e)
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return matches[0].Node()
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("ambiguous ID %q — did you mean one of: %s", id, strings.Join(ids, ", "))
	}
}
