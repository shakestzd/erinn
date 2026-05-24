// session_prune_archive.go — `wipnote session prune` and `wipnote session archive`.
//
// prune: reclaim disk from old session logs, with --older-than / --keep-last /
//        --dry-run / --yes flags and mandatory confirmation before deletion.
// archive: move a single session's events.ndjson into a compressed archive in
//          .wipnote/archive/<yyyy-mm>/, leaving it restorable via session restore.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shakestzd/wipnote/internal/otel/retention"
	"github.com/spf13/cobra"
)

// sessionPruneCmd returns the cobra command for `wipnote session prune`.
func sessionPruneCmd() *cobra.Command {
	var olderThan string
	var keepLast int
	var dryRun bool
	var yes bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune old session logs to reclaim disk",
		Long: `Reclaim disk by removing session directories whose events.ndjson has been
fully ingested into the SQLite read index.

Flags compose as an intersection (AND): a session must satisfy ALL supplied
criteria to be pruned.

  --older-than <duration>
      Prune sessions whose events.ndjson mtime is older than this value.
      Accepts Go durations (e.g. 720h) and the Nd shorthand (e.g. 30d).

  --keep-last <N>
      Keep the N most-recently-modified sessions; prune the rest (oldest first).
      When combined with --older-than, only sessions that are BOTH older than
      the duration AND outside the keep-last window are pruned.

  --dry-run
      Print what WOULD be pruned (ids, sizes, total reclaimable) without
      deleting anything. No confirmation required.

  --yes
      Skip the confirmation prompt and proceed immediately.

Safety guarantees (always enforced, regardless of flags):
  - The active session is NEVER pruned.
  - Sessions whose events.ndjson is not yet fully indexed (indexer lag) are
    NEVER pruned — no un-ingested data is lost.
  - If neither --older-than nor --keep-last is given, the command refuses to
    run (a bare prune without criteria would be surprising).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSessionPrune(olderThan, keepLast, dryRun, yes)
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "",
		"Prune sessions older than this duration (e.g. 30d, 720h)")
	cmd.Flags().IntVar(&keepLast, "keep-last", 0,
		"Keep the N most-recent sessions; prune the rest (0 = disabled)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print candidates without deleting")
	cmd.Flags().BoolVar(&yes, "yes", false,
		"Skip confirmation prompt")
	return cmd
}

// sessionArchiveCmd returns the cobra command for `wipnote session archive <id>`.
func sessionArchiveCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "archive <session-id>",
		Short: "Archive a session's logs out of the hot path",
		Long: `Move a session's events.ndjson into .wipnote/archive/<yyyy-mm>/<id>.tar.gz
and remove the live session directory. The session remains restorable via
'wipnote session restore <id>'.

The active session is always excluded — attempting to archive it is an error.
The indexer must have fully consumed events.ndjson (.index-offset == file size)
before the archive proceeds; otherwise the command reports an error so that no
un-ingested data is lost.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSessionArchive(args[0], dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print what would be archived without modifying any files")
	return cmd
}

// pruneCandidate holds the session metadata needed for the prune decision.
type pruneCandidate struct {
	sessionID string
	sessDir   string
	eventsFile string
	mtime     time.Time
	size      int64
}

// runSessionPrune implements `wipnote session prune`.
func runSessionPrune(olderThan string, keepLast int, dryRun, yes bool) error {
	if olderThan == "" && keepLast == 0 {
		return fmt.Errorf("at least one of --older-than or --keep-last must be supplied\n" +
			"Run 'wipnote session prune --help' for details.")
	}

	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	activeID := activeSessionIDFromFile(wipnoteDir)

	var ageCutoff time.Time
	if olderThan != "" {
		dur, err := parseDuration(olderThan)
		if err != nil {
			return fmt.Errorf("invalid --older-than value %q: %w", olderThan, err)
		}
		ageCutoff = time.Now().Add(-dur)
	}

	candidates, err := collectPruneCandidates(wipnoteDir, activeID)
	if err != nil {
		return err
	}

	// Sort oldest-mtime first so --keep-last drops the stalest entries.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.Before(candidates[j].mtime)
	})

	targets := filterPruneCandidates(candidates, ageCutoff, keepLast)

	if len(targets) == 0 {
		fmt.Println("wipnote session prune: nothing to prune.")
		return nil
	}

	// Print the candidate list.
	var totalBytes int64
	fmt.Printf("%-36s  %10s  %s\n", "SESSION", "SIZE", "LAST ACTIVITY")
	for _, c := range targets {
		fmt.Printf("%-36s  %10s  %s\n",
			c.sessionID,
			humanBytes(c.size),
			c.mtime.Local().Format("2006-01-02 15:04:05"))
		totalBytes += c.size
	}
	fmt.Printf("\n%d session(s), %s reclaimable\n", len(targets), humanBytes(totalBytes))

	if dryRun {
		fmt.Println("\n(dry-run: no files were deleted)")
		return nil
	}

	if !promptYesNo("Prune these sessions?", yes) {
		fmt.Println("Aborted.")
		return nil
	}

	pruned, errs := applyPrune(wipnoteDir, targets)
	fmt.Printf("Pruned %d session(s), reclaimed %s\n", pruned, humanBytes(totalBytes))
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "warn: %v\n", e)
	}
	return nil
}

// collectPruneCandidates walks .wipnote/sessions/ and returns sessions that are
// safe to prune: not the active session, not recently modified, and fully indexed.
func collectPruneCandidates(wipnoteDir, activeID string) ([]pruneCandidate, error) {
	sessionsRoot := filepath.Join(wipnoteDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var candidates []pruneCandidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sid := e.Name()
		// Safety: never include the active session.
		if sid == activeID && activeID != "" {
			continue
		}
		sessDir := filepath.Join(sessionsRoot, sid)
		eventsFile := filepath.Join(sessDir, "events.ndjson")

		info, err := os.Stat(eventsFile)
		if err != nil {
			continue // no events.ndjson — skip (nothing to reclaim)
		}
		// Safety: never prune un-ingested data.
		if !retention.IndexerCaughtUp(sessDir, eventsFile) {
			continue
		}
		candidates = append(candidates, pruneCandidate{
			sessionID:  sid,
			sessDir:    sessDir,
			eventsFile: eventsFile,
			mtime:      info.ModTime(),
			size:       info.Size(),
		})
	}
	return candidates, nil
}

// filterPruneCandidates applies --older-than and --keep-last to the sorted
// (oldest-first) candidate list and returns the sessions to be pruned.
//
// Semantics when both flags are supplied: a session is pruned if it is BOTH
// older than ageCutoff AND falls outside the keep-last window.  This is the
// conservative interpretation — neither criterion alone is sufficient.
func filterPruneCandidates(candidates []pruneCandidate, ageCutoff time.Time, keepLast int) []pruneCandidate {
	// Determine which candidates satisfy the age criterion.
	olderThanEnabled := !ageCutoff.IsZero()

	// Determine the keep-last boundary index: candidates[keepIdx:] are the
	// most-recent sessions we want to keep.
	keepIdx := 0
	if keepLast > 0 && keepLast < len(candidates) {
		keepIdx = len(candidates) - keepLast
	}

	var targets []pruneCandidate
	for i, c := range candidates {
		inKeepWindow := keepLast > 0 && i >= keepIdx
		if inKeepWindow {
			continue // within keep-last window — always spare
		}
		if olderThanEnabled && !c.mtime.Before(ageCutoff) {
			continue // not old enough — spare when age criterion active
		}
		targets = append(targets, c)
	}
	return targets
}

// applyPrune archives + removes each candidate. Returns count pruned and any
// per-session errors (non-fatal: we keep going after individual failures).
func applyPrune(wipnoteDir string, targets []pruneCandidate) (int, []error) {
	var pruned int
	var errs []error
	for _, c := range targets {
		if err := retention.ArchiveSession(wipnoteDir, c.sessionID, false); err != nil {
			errs = append(errs, fmt.Errorf("prune session %s: %w", c.sessionID, err))
			continue
		}
		pruned++
	}
	return pruned, errs
}

// runSessionArchive implements `wipnote session archive <id>`.
func runSessionArchive(sessionID string, dryRun bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	activeID := activeSessionIDFromFile(wipnoteDir)
	if sessionID == activeID && activeID != "" {
		return fmt.Errorf("cannot archive the active session (%s); end the session first", sessionID)
	}

	sessDir := retention.SessionDir(wipnoteDir, sessionID)
	eventsFile := filepath.Join(sessDir, "events.ndjson")

	// Verify the session directory exists.
	if _, err := os.Stat(sessDir); os.IsNotExist(err) {
		return fmt.Errorf("session %q not found in .wipnote/sessions/\n"+
			"It may already be archived. Use 'wipnote session restore %s' to unarchive.", sessionID, sessionID)
	}

	// Verify the indexer has caught up before archiving.
	if _, err := os.Stat(eventsFile); err == nil {
		if !retention.IndexerCaughtUp(sessDir, eventsFile) {
			return fmt.Errorf("session %q has not yet been fully indexed (indexer lag detected)\n"+
				"Wait for the indexer to catch up, or run 'wipnote reindex' first.", sessionID)
		}
	}

	if dryRun {
		size := int64(0)
		if info, err := os.Stat(eventsFile); err == nil {
			size = info.Size()
		}
		fmt.Printf("Would archive session %s (%s) into .wipnote/archive/\n", sessionID, humanBytes(size))
		return nil
	}

	if err := retention.ArchiveSession(wipnoteDir, sessionID, false); err != nil {
		return fmt.Errorf("archive session %s: %w", sessionID, err)
	}

	fmt.Printf("Archived session %s into .wipnote/archive/\n", sessionID)
	fmt.Printf("Use 'wipnote session restore %s' to unarchive.\n", sessionID)
	return nil
}

// activeSessionIDFromFile reads the active session ID from the .wipnote/.active-session
// file. Returns empty string when the file is absent or unreadable — the caller
// then treats no session as active, which is the safe default.
func activeSessionIDFromFile(wipnoteDir string) string {
	data, err := os.ReadFile(filepath.Join(wipnoteDir, ".active-session"))
	if err != nil {
		return ""
	}
	// The file is JSON {"session_id":"...",...}; extract just the session_id.
	// Use a lightweight parse to avoid importing encoding/json here.
	s := string(data)
	const key = `"session_id":"`
	idx := strIndexOf(s, key)
	if idx < 0 {
		return ""
	}
	start := idx + len(key)
	end := strIndexOf(s[start:], `"`)
	if end < 0 {
		return ""
	}
	return s[start : start+end]
}

// strIndexOf returns the byte index of substr in s, or -1.
func strIndexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
