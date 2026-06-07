package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

// purgeSpikesCmd scans .wipnote/spikes/, identifies stale todo spikes, and
// deletes them. Kept spikes: done/in-progress, track-linked, or manual filenames.
func purgeSpikesCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "purge-spikes",
		Short: "Remove stale todo spikes, keeping linked and done spikes",
		Long: `Scans .wipnote/spikes/ and deletes stale todo spikes.

A spike is KEPT if any of the following are true:
  - Status is done or in-progress
  - data-track-id attribute is non-empty
  - The spike ID appears in any track's contains edges
  - The filename does not follow the auto-generated spk-XXXXXXXX.html pattern

After deletion, runs a full reindex to sync SQLite.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPurgeSpikes(cmd, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Show what would be deleted without deleting")
	return cmd
}

func runPurgeSpikes(cmd *cobra.Command, dryRun bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	spikesDir := filepath.Join(wipnoteDir, "spikes")

	// 1. Build the set of spike IDs linked from any track.
	linkedFromTrack, err := collectTrackLinkedSpikeIDs(wipnoteDir)
	if err != nil {
		return fmt.Errorf("scan tracks: %w", err)
	}

	// 2. Scan all spike files.
	pattern := filepath.Join(spikesDir, "*.html")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob spikes: %w", err)
	}

	total := len(files)
	fmt.Fprintf(cmd.OutOrStdout(), "Scanning %s... %d spikes found\n", spikesDir, total)

	var toKeep, toDelete []string
	var keepReasons []string

	for _, f := range files {
		keep, reason := shouldKeepSpike(f, linkedFromTrack)
		if keep {
			toKeep = append(toKeep, f)
			keepReasons = append(keepReasons, reason)
		} else {
			toDelete = append(toDelete, f)
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Keeping: %d\n", len(toKeep))
	fmt.Fprintf(cmd.OutOrStdout(), "Deleting: %d stale todo spikes\n", len(toDelete))

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(),
			"\n[dry-run] Would delete %d files. Run without --dry-run to execute.\n",
			len(toDelete))
		if len(toKeep) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "\nKept spikes:")
			for i, f := range toKeep {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-40s  (%s)\n",
					filepath.Base(f), keepReasons[i])
			}
		}
		return nil
	}

	// 3. Delete the stale spikes.
	deleted := 0
	for _, f := range toDelete {
		if err := os.Remove(f); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: delete %s: %v\n",
				filepath.Base(f), err)
			continue
		}
		deleted++
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\nDeleted %d stale spikes. Reindexing...\n", deleted)

	// 4. Full reindex to sync SQLite after deletion.
	if reindexErr := runFullReindex(wipnoteDir, cmd); reindexErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: reindex: %v\n", reindexErr)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Reindex complete. %d spikes remaining.\n", len(toKeep))
	return nil
}

// runFullReindex opens the SQLite db and performs a full reindex of all HTML
// files in the given wipnoteDir.
func runFullReindex(wipnoteDir string, cmd *cobra.Command) error {
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(wipnoteDir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	projectDir := filepath.Dir(wipnoteDir)
	validIDs := make(map[string]bool)

	trackTotal, trackUpserted, trackErrs := reindexTracks(database, wipnoteDir, projectDir, validIDs, false)
	featTotal, featUpserted, featErrs := reindexFeatureDir(database, wipnoteDir, projectDir, "features", validIDs, false)
	bugTotal, bugUpserted, bugErrs := reindexFeatureDir(database, wipnoteDir, projectDir, "bugs", validIDs, false)
	spikeTotal, spikeUpserted, spikeErrs := reindexFeatureDir(database, wipnoteDir, projectDir, "spikes", validIDs, false)

	total := trackTotal + featTotal + bugTotal + spikeTotal
	upserted := trackUpserted + featUpserted + bugUpserted + spikeUpserted
	errCount := trackErrs + featErrs + bugErrs + spikeErrs

	purged, edgesPurged := purgeStaleEntries(database, validIDs)

	fmt.Fprintf(cmd.OutOrStdout(), "  Reindexed: %d upserted, %d errors (of %d HTML files)\n",
		upserted, errCount, total)
	if purged > 0 || edgesPurged > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  Purged: %d stale entries, %d stale edges\n",
			purged, edgesPurged)
	}

	return nil
}

// shouldKeepSpike returns (true, reason) if the spike should be kept.
// Returns (false, "") if it should be deleted.
func shouldKeepSpike(filePath string, linkedFromTrack map[string]bool) (bool, string) {
	base := filepath.Base(filePath)

	// Keep spikes with non-standard filenames (manually created investigations).
	if !isAutoGeneratedSpikeFile(base) {
		return true, "manual filename"
	}

	node, err := htmlparse.ParseFile(filePath)
	if err != nil {
		// Cannot parse — keep to be safe.
		return true, "parse error"
	}

	// Keep if status is not todo.
	if node.Status != models.StatusTodo {
		return true, fmt.Sprintf("status=%s", node.Status)
	}

	// Keep if data-track-id is set.
	if node.TrackID != "" {
		return true, fmt.Sprintf("track=%s", node.TrackID)
	}

	// Keep if the spike ID appears in any track's contains edges.
	if linkedFromTrack[node.ID] {
		return true, "linked from track"
	}

	return false, ""
}

// isAutoGeneratedSpikeFile returns true if the filename matches the pattern
// spk-XXXXXXXX.html (exactly 8 lowercase hex chars after the prefix).
func isAutoGeneratedSpikeFile(filename string) bool {
	if !strings.HasPrefix(filename, "spk-") || !strings.HasSuffix(filename, ".html") {
		return false
	}
	inner := strings.TrimPrefix(strings.TrimSuffix(filename, ".html"), "spk-")
	if len(inner) != 8 {
		return false
	}
	for _, c := range inner {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex {
			return false
		}
	}
	return true
}

// collectTrackLinkedSpikeIDs scans all track HTML files and returns the set of
// spike IDs (spk-*) that appear in any track's graph edges.
func collectTrackLinkedSpikeIDs(wipnoteDir string) (map[string]bool, error) {
	linked := make(map[string]bool)

	pattern := filepath.Join(wipnoteDir, "tracks", "*.html")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return linked, fmt.Errorf("glob tracks: %w", err)
	}

	for _, f := range files {
		node, parseErr := htmlparse.ParseFile(f)
		if parseErr != nil {
			continue
		}
		for _, edges := range node.Edges {
			for _, e := range edges {
				if strings.HasPrefix(e.TargetID, "spk-") {
					linked[e.TargetID] = true
				}
			}
		}
	}

	return linked, nil
}
