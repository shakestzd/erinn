package main

import (
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

const metaKeyLastIndexedCommit = "last_indexed_commit"

func reindexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Sync HTML work items to SQLite index",
		Long: `Reads HTML work item files from .wipnote/ and upserts them into the SQLite index.

By default runs incrementally: only files changed since the last successful reindex
are reparsed. Use --full to force a complete reparse of all files.`,
		RunE: runReindex,
	}
	cmd.Flags().Bool("full", false, "Force full reindex of all HTML files (ignores git diff)")
	cmd.Flags().BoolP("verbose", "v", false, "Print one line per error encountered during reindex")
	cmd.AddCommand(reindexBackfillOrphansCmd())
	return cmd
}

func runReindex(cmd *cobra.Command, _ []string) error {
	fullFlag, _ := cmd.Flags().GetBool("full")
	verboseFlag, _ := cmd.Flags().GetBool("verbose")

	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	projectDir := filepath.Dir(wipnoteDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return fmt.Errorf("ensure db dir: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	dbClosed := false
	closeDB := func() {
		if !dbClosed {
			_ = database.Close()
			dbClosed = true
		}
	}
	defer closeDB()
	currentCommit := gitHeadCommit(projectDir)

	lastCommit, _ := dbpkg.GetMetadata(database, metaKeyLastIndexedCommit)
	useIncremental := !fullFlag && lastCommit != "" && currentCommit != ""

	var total, upserted, errCount int
	validIDs := make(map[string]bool)

	// Rebuild agent_events from session HTML activity logs. projectDir is
	// passed through so parseSessionHTML can attribute sessions whose HTML
	// files predate the data-project-dir attribute (bug-a52d5bf9). The full
	// path runs this mid-pass (see below) because collectSessionIDs reads the
	// table it fills; sessionsIndexed keeps it from running twice.
	sessDir := filepath.Join(wipnoteDir, "sessions")
	var sessTotal, sessUpserted, sessErrs int
	sessionsIndexed := false

	if useIncremental {
		if !gitCommitExists(projectDir, lastCommit) {
			useIncremental = false
		}
	}

	// Force a full reindex when an archive ledger changed since lastCommit. The
	// incremental path keys off per-node file paths; an archive ledger is a
	// multi-row table (not a single-node file), and archiving DELETES the
	// individual files (which the incremental path would purge) while only the
	// ledger gains the rows. A full pass is the simplest correct response — it is
	// ledger-aware end-to-end (nodes, edges, and validIDs completeness for edge
	// targets that did not change this window).
	if useIncremental && archiveLedgerChangedSince(projectDir, wipnoteDir, lastCommit) {
		useIncremental = false
	}

	if useIncremental {
		total, upserted, errCount = runIncrementalReindex(database, wipnoteDir, projectDir, lastCommit, validIDs, verboseFlag)
		fmt.Printf("Reindexed (incremental): %d upserted, %d errors (of %d changed HTML files)\n",
			upserted, errCount, total)
	} else {
		trackTotal, trackUpserted, trackErrs := reindexTracks(database, wipnoteDir, projectDir, validIDs, verboseFlag)
		total += trackTotal
		upserted += trackUpserted
		errCount += trackErrs

		for _, dir := range []string{"features", "bugs", "spikes"} {
			t, u, e := reindexFeatureDir(database, wipnoteDir, projectDir, dir, validIDs, verboseFlag)
			total += t
			upserted += u
			errCount += e
		}

		// Archived work items: compacted out of individual files into
		// .wipnote/archive/<type>s.html ledgers. They are still canonical, so
		// they index into the same features table and must register in validIDs
		// BEFORE purgeStaleEntries — otherwise the purge would treat a compacted
		// item as a deleted file and drop it (and its edges) from the index.
		arcTotal, arcUpserted, arcErrs := reindexWorkitemLedgerNodes(database, wipnoteDir, projectDir, validIDs, verboseFlag)
		total += arcTotal
		upserted += arcUpserted
		errCount += arcErrs

		// Plans are canonical nodes with their own HTML/YAML, but they are
		// never indexed into the features or tracks tables, so no other pass
		// registers their IDs. Without this, indexNodeEdges' target-validity
		// gate silently drops every edge POINTING AT a plan (feature →
		// planned_in → plan, track → contains → plan, …) and purgeStaleEntries
		// deletes any such edge left over from a prior run. Must run BEFORE
		// purgeStaleEntries, and must land together with the "plans" entry in
		// reindexEdges — registering only the scan direction re-breaks the
		// feature-side edges, and registering only the IDs leaves plan-sourced
		// edges unscanned (bug-d5eaf6a4).
		collectPlanIDs(wipnoteDir, validIDs)

		// Sessions must be indexed BEFORE collectSessionIDs. On a from-scratch
		// DB the sessions table is empty until reindexSessions populates it, so
		// collectSessionIDs would register nothing and every work-item →
		// implemented_in → session edge would fail the target-validity gate —
		// a loss that only manifests on a cold rebuild (bug-6ec28063). The node
		// passes above still have to run first: agent_events.feature_id carries
		// a foreign key to features(id), so events would be rejected if the
		// features table were still empty.
		sessTotal, sessUpserted, sessErrs = reindexSessions(database, sessDir, projectDir)
		sessionsIndexed = true

		// The seam for the canonical sessions ledger (feat-1b08a194): its
		// contract makes the ledger, not this derived table, the authority for
		// session validity. That is a swap of the collector below — an id it
		// registers classifies EdgeTargetLive and indexes with no tombstone
		// marker, with no change to graph.ClassifyEdgeTarget, which asks only
		// whether an id is in validIDs and never where it came from.
		//
		// Whatever replaces or joins this call must stay ABOVE
		// purgeStaleEntries, for the same reason collectPlanIDs does: the purge
		// judges targets against validIDs as it stands at that moment.
		collectSessionIDs(database, validIDs)
		purged, edgesPurged := purgeStaleEntries(database, validIDs)
		reindexEdges(database, wipnoteDir, validIDs)
		reindexWorkitemLedgerEdges(database, wipnoteDir, validIDs, verboseFlag)
		fixImplementedInEdges(database)
		fmt.Printf("Reindexed: %d upserted, %d errors (of %d HTML files)\n",
			upserted, errCount, total)
		if purged > 0 || edgesPurged > 0 {
			fmt.Printf("Purged: %d stale features, %d stale edges\n", purged, edgesPurged)
		}
	}

	if !sessionsIndexed {
		sessTotal, sessUpserted, sessErrs = reindexSessions(database, sessDir, projectDir)
	}
	if sessUpserted > 0 || sessErrs > 0 {
		fmt.Printf("  sessions: %d events upserted, %d errors (of %d session files)\n",
			sessUpserted, sessErrs, sessTotal)
	}

	// Parse git commit trailers (Refs:/Fixes:) to backfill feature attribution.
	trailerCount, trailerErr := reindexCommitTrailers(database, projectDir)
	if trailerErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: commit trailer ingestion: %v\n", trailerErr)
	} else if trailerCount > 0 {
		fmt.Printf("  commit trailers: %d feature links from Refs/Fixes trailers\n", trailerCount)
	}

	// Rebuild feature_files from git_commits.
	fileCount, ffErr := reindexFeatureFiles(database, projectDir)
	if ffErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: feature_files rebuild: %v\n", ffErr)
	} else if fileCount > 0 {
		fmt.Printf("  feature_files: %d file associations rebuilt\n", fileCount)
	}

	// Ingest arch cards (.wipnote/arch/*.md) into the arch_cards read index.
	archTotal, archUpserted, archErrs := reindexArchCards(database, wipnoteDir, verboseFlag)
	if archUpserted > 0 || archErrs > 0 {
		fmt.Printf("  arch cards: %d upserted, %d errors (of %d card files)\n",
			archUpserted, archErrs, archTotal)
	}
	errCount += archErrs

	// Ingest the claim ledger (.wipnote/claims/*.html) into claim_episodes. Runs
	// on the incremental path too — see reindexClaimEpisodes for why a
	// git-diff-driven pass would miss episodes that are written but not yet
	// flushed by the commit queue.
	claimFiles, claimRows, claimErrs := reindexClaimEpisodes(database, wipnoteDir, verboseFlag)
	if claimRows > 0 || claimErrs > 0 {
		fmt.Printf("  claim episodes: %d upserted, %d errors (of %d ledger files)\n",
			claimRows, claimErrs, claimFiles)
	}
	errCount += claimErrs

	// Gate ledger (feat-0e5ca43e). Backfill runs FIRST: it moves gate runs
	// recorded before the ledger existed into it, and only then can the
	// projection below treat them as rebuildable. Both run on the incremental
	// path, for the same reason reindexClaimEpisodes does — ledger writes commit
	// asynchronously, so a git-diff-driven pass would miss a run that is written
	// but not yet flushed.
	backfilled, backfillErrs := backfillGateLedgerFromIndex(database, wipnoteDir, verboseFlag)
	if backfilled > 0 || backfillErrs > 0 {
		fmt.Printf("  gate ledger: %d pre-ledger gate runs given a canonical home (%d errors)\n",
			backfilled, backfillErrs)
	}
	errCount += backfillErrs

	gateRows, gateErrs := reindexGateRecords(database, wipnoteDir, verboseFlag)
	if gateRows > 0 || gateErrs > 0 {
		fmt.Printf("  gate records: %d projected from the canonical ledger, %d errors\n",
			gateRows, gateErrs)
	}
	errCount += gateErrs

	// Ingest recap artifacts (.wipnote/recaps/*.html) into the recaps read index.
	recapTotal, recapUpserted, recapErrs := reindexRecaps(database, wipnoteDir, projectDir, verboseFlag)
	if recapUpserted > 0 || recapErrs > 0 {
		fmt.Printf("  recaps: %d upserted, %d errors (of %d recap files)\n",
			recapUpserted, recapErrs, recapTotal)
	}
	errCount += recapErrs

	// Slice 9 (feat-229f3333): rebuild graph_edges derived from plan YAML
	// dependency lists. The HTML edge pass above only covers <a data-*-id>
	// attributes; plan YAML slice deps are a separate canonical source.
	if !useIncremental {
		planFiles, planEdges, planErrs := reindexPlanEdges(database, wipnoteDir)
		if planEdges > 0 || planErrs > 0 {
			fmt.Printf("  plan edges: %d edges from %d plan YAML files (%d errors)\n",
				planEdges, planFiles, planErrs)
		}
		errCount += planErrs

		// bug-eca8141d: replay slice approval state from canonical plan YAML into
		// plan_feedback so the finalize gate works after a cache rebuild. Rows are
		// inserted with INSERT OR IGNORE — live interactive rows win.
		_, approvalRows, approvalErrs := reindexPlanApprovals(database, wipnoteDir)
		if approvalRows > 0 || approvalErrs > 0 {
			fmt.Printf("  plan approvals: %d slice approval rows replayed (%d errors)\n",
				approvalRows, approvalErrs)
		}
		// bug-fddf5820 (finding 6): approvalErrs (and the sibling planErrs /
		// archErrs passes) were never folded into errCount, so a rebuild with
		// failed approval replays still set the "last indexed commit" metadata
		// as if it were clean — suppressing the next incremental pass's retry.
		errCount += approvalErrs
	}

	if currentCommit != "" && errCount == 0 {
		_ = dbpkg.SetMetadata(database, metaKeyLastIndexedCommit, currentCommit)
	}

	// Close the read-pool handle before opening the OTel writer. Slice 6's
	// writer service uses a dedicated writable connection — opening it on
	// the same DB file while another writer is active is the contention
	// pattern slice 6 is engineered to AVOID, not exercise. Sequencing the
	// open/close keeps reindex a single writer at any moment.
	closeDB()

	// Slice 9 (feat-229f3333): replay every per-session events.ndjson back
	// into otel_signals. This is the canonical-first recovery path — the
	// dashboard's OTel-derived event surface is fully rebuildable from
	// NDJSON, exactly the rebuild promise slice 9 ratifies.
	if !useIncremental {
		otelSess, otelIter, otelErrs := reindexOtelEvents(dbPath, wipnoteDir)
		if otelSess > 0 || otelErrs > 0 {
			fmt.Printf("  otel events: replayed %d session NDJSON files in %d iterations (%d errors)\n",
				otelSess, otelIter, otelErrs)
		}
	}

	return nil
}

// runIncrementalReindex parses only files changed between lastCommit and HEAD.
func runIncrementalReindex(
	database *sql.DB,
	wipnoteDir, projectDir, lastCommit string,
	validIDs map[string]bool,
	verbose bool,
) (int, int, int) {
	added, deleted := gitChangedFiles(projectDir, lastCommit, wipnoteDir)

	for _, path := range deleted {
		id := idFromHTMLPath(path)
		if id != "" {
			if isRecapHTMLPath(path, wipnoteDir) {
				database.Exec(`DELETE FROM recaps WHERE id = ?`, id)
			} else {
				database.Exec(`DELETE FROM features WHERE id = ?`, id)
				database.Exec(`DELETE FROM tracks WHERE id = ?`, id)
			}
		}
	}

	if len(added) == 0 {
		return 0, 0, 0
	}

	var total, upserted, errCount int
	for _, path := range added {
		total++
		if isRecapHTMLPath(path, wipnoteDir) {
			id := idFromHTMLPath(path)
			row, parseErr := parseRecapHTML(path, id)
			if parseErr != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex recaps: error: %s: %v\n", path, parseErr)
				}
				continue
			}
			createdAt, updatedAt := applyGitTimestamps(projectDir, path, time.Time{}, time.Time{})
			if !createdAt.IsZero() {
				t := createdAt
				row.CreatedAt = &t
			}
			if !updatedAt.IsZero() {
				t := updatedAt
				row.UpdatedAt = &t
			}
			if err := dbpkg.UpsertRecap(database, row); err != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex recaps: error: %s: %v\n", path, err)
				}
				continue
			}
			upserted++
			continue
		}

		node, parseErr := htmlparse.ParseFile(path)
		if parseErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex: error: %s: %v\n", path, parseErr)
			}
			continue
		}

		createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)
		createdAt, updatedAt = applyGitTimestamps(projectDir, path, createdAt, updatedAt)

		if node.Type == "track" {
			track := &dbpkg.Track{
				ID:        node.ID,
				Type:      "track",
				Title:     node.Title,
				Priority:  string(node.Priority),
				Status:    normalizeStatus(string(node.Status)),
				CreatedAt: createdAt,
				UpdatedAt: updatedAt,
			}
			if err := dbpkg.UpsertTrack(database, track); err != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex: error: %s: %v\n", path, err)
				}
				continue
			}
		} else {
			desc := node.Content
			if len([]rune(desc)) > 500 {
				desc = string([]rune(desc)[:499]) + "\u2026"
			}
			stepsTotal := len(node.Steps)
			stepsCompleted := 0
			for _, s := range node.Steps {
				if s.Completed {
					stepsCompleted++
				}
			}
			feat := &dbpkg.Feature{
				ID:             node.ID,
				Type:           mapNodeType(node.Type),
				Title:          node.Title,
				Description:    desc,
				Status:         normalizeStatus(string(node.Status)),
				Priority:       string(node.Priority),
				AssignedTo:     node.AgentAssigned,
				TrackID:        node.TrackID,
				CreatedAt:      createdAt,
				UpdatedAt:      updatedAt,
				StepsTotal:     stepsTotal,
				StepsCompleted: stepsCompleted,
			}
			if err := dbpkg.UpsertFeature(database, feat); err != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex: error: %s: %v\n", path, err)
				}
				continue
			}
		}
		validIDs[node.ID] = true
		upserted++
	}
	return total, upserted, errCount
}

func isRecapHTMLPath(path, wipnoteDir string) bool {
	rel, err := filepath.Rel(filepath.Join(wipnoteDir, "recaps"), path)
	if err != nil {
		return false
	}
	return rel != "." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && strings.HasSuffix(path, ".html")
}

// isArchiveLedgerPath reports whether path is a work-item archive ledger
// (.wipnote/archive/<type>s.html), as opposed to an individual work-item file.
func isArchiveLedgerPath(path, wipnoteDir string) bool {
	rel, err := filepath.Rel(filepath.Join(wipnoteDir, graph.ArchiveDirName), path)
	if err != nil {
		return false
	}
	return rel != "." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		rel != ".." && strings.HasSuffix(path, ".html")
}

// archiveLedgerChangedSince reports whether any archive ledger under
// .wipnote/archive/ was added, modified, or deleted between fromCommit and HEAD.
// When true, runReindex falls back to a full (ledger-aware) reindex.
func archiveLedgerChangedSince(projectDir, wipnoteDir, fromCommit string) bool {
	added, deleted := gitChangedFiles(projectDir, fromCommit, wipnoteDir)
	for _, path := range append(append([]string{}, added...), deleted...) {
		if isArchiveLedgerPath(path, wipnoteDir) {
			return true
		}
	}
	return false
}

func gitHeadCommit(projectDir string) string {
	out, err := exec.Command("git", "-C", projectDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitCommitExists(projectDir, commit string) bool {
	err := exec.Command("git", "-C", projectDir, "cat-file", "-e", commit+"^{commit}").Run()
	return err == nil
}

func gitChangedFiles(projectDir, fromCommit, wipnoteDir string) (added []string, deleted []string) {
	relHg, err := filepath.Rel(projectDir, wipnoteDir)
	if err != nil {
		return nil, nil
	}

	out, err := exec.Command(
		"git", "-C", projectDir,
		"diff", "--name-status", fromCommit, "HEAD", "--", relHg,
	).Output()
	if err != nil {
		return nil, nil
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		if strings.HasPrefix(status, "R") && len(parts) == 3 {
			oldPath := filepath.Join(projectDir, parts[1])
			newPath := filepath.Join(projectDir, parts[2])
			if strings.HasSuffix(newPath, ".html") {
				added = append(added, newPath)
			}
			if strings.HasSuffix(oldPath, ".html") {
				deleted = append(deleted, oldPath)
			}
			continue
		}
		filePath := filepath.Join(projectDir, parts[1])
		if !strings.HasSuffix(filePath, ".html") {
			continue
		}
		switch status {
		case "A", "M":
			added = append(added, filePath)
		case "D":
			deleted = append(deleted, filePath)
		}
	}

	untrackedOut, err := exec.Command(
		"git", "-C", projectDir,
		"ls-files", "--others", "--exclude-standard", "--", relHg,
	).Output()
	if err == nil {
		for _, rel := range strings.Split(strings.TrimSpace(string(untrackedOut)), "\n") {
			if rel == "" {
				continue
			}
			path := filepath.Join(projectDir, rel)
			if strings.HasSuffix(path, ".html") {
				added = append(added, path)
			}
		}
	}

	// Include working-tree dirty files: modifications not yet committed (staged
	// or unstaged). Commands like `bug move` write the HTML without committing,
	// so git diff HEAD..HEAD misses them. Use `git diff --name-status` (unstaged)
	// and `git diff --cached --name-status` (staged) to catch both cases, and
	// distinguish modifications (A, M, R) from deletions (D).
	added, deleted = appendDirtyHTMLFiles(projectDir, relHg, added, deleted)

	return deduplicatePaths(added), deleted
}

// appendDirtyHTMLFiles appends any .wipnote HTML files that are modified or
// deleted in the working tree (staged or unstaged) but not yet committed.
// It uses git diff --name-status to distinguish modifications from deletions:
// - A (added), M (modified), R (renamed) go to added list (upsert)
// - D (deleted) goes to deleted list (remove from SQLite)
func appendDirtyHTMLFiles(projectDir, relHg string, added, deleted []string) ([]string, []string) {
	for _, args := range [][]string{
		{"diff", "--name-status", "--", relHg},
		{"diff", "--cached", "--name-status", "--", relHg},
	} {
		out, err := exec.Command("git", append([]string{"-C", projectDir}, args...)...).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) < 2 {
				continue
			}
			status := parts[0]
			// Handle renames: old path is deleted, new path is added.
			if strings.HasPrefix(status, "R") && len(parts) == 3 {
				oldPath := filepath.Join(projectDir, parts[1])
				newPath := filepath.Join(projectDir, parts[2])
				if strings.HasSuffix(newPath, ".html") {
					added = append(added, newPath)
				}
				if strings.HasSuffix(oldPath, ".html") {
					deleted = append(deleted, oldPath)
				}
				continue
			}
			filePath := filepath.Join(projectDir, parts[1])
			if !strings.HasSuffix(filePath, ".html") {
				continue
			}
			switch status {
			case "A", "M":
				added = append(added, filePath)
			case "D":
				deleted = append(deleted, filePath)
			}
		}
	}
	return added, deleted
}

// deduplicatePaths returns paths with duplicates removed, preserving order.
func deduplicatePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := paths[:0:0]
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func idFromHTMLPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".html")
}

func reindexTracks(database *sql.DB, wipnoteDir, projectDir string, validIDs map[string]bool, verbose bool) (int, int, int) {
	patterns := []string{
		filepath.Join(wipnoteDir, "tracks", "*.html"),
		filepath.Join(wipnoteDir, "tracks", "*", "index.html"),
	}

	seen := make(map[string]bool)
	var total, upserted, errCount int
	var allFiles []string

	for _, pattern := range patterns {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			if seen[f] {
				continue
			}
			seen[f] = true
			allFiles = append(allFiles, f)
		}
	}

	// One batched git-log walk for every track file instead of two `git log`
	// subprocesses per file (bug-4e5816f4).
	batch := batchGitFileTimestamps(projectDir, allFiles)

	for _, f := range allFiles {
		total++

		node, parseErr := htmlparse.ParseFile(f)
		if parseErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex: error: %s: %v\n", f, parseErr)
			}
			continue
		}

		htmlCreated, htmlUpdated := normalizeTimes(node.CreatedAt, node.UpdatedAt)
		var createdAt, updatedAt time.Time
		if c, u, ok := timestampsFromBatch(batch, f, htmlCreated, htmlUpdated); ok {
			createdAt, updatedAt = c, u
		} else {
			createdAt, updatedAt = applyGitTimestamps(projectDir, f, htmlCreated, htmlUpdated)
		}
		track := &dbpkg.Track{
			ID:        node.ID,
			Type:      "track",
			Title:     node.Title,
			Priority:  string(node.Priority),
			Status:    normalizeStatus(string(node.Status)),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		}

		if upsertErr := dbpkg.UpsertTrack(database, track); upsertErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex: error: %s: %v\n", f, upsertErr)
			}
			continue
		}
		validIDs[node.ID] = true
		upserted++
	}
	return total, upserted, errCount
}

func reindexFeatureDir(database *sql.DB, wipnoteDir, projectDir, dir string, validIDs map[string]bool, verbose bool) (int, int, int) {
	pattern := filepath.Join(wipnoteDir, dir, "*.html")
	files, _ := filepath.Glob(pattern)

	// One batched git-log walk for every file in this directory instead of
	// two `git log` subprocesses per file (bug-4e5816f4).
	batch := batchGitFileTimestamps(projectDir, files)

	var total, upserted, errCount int
	for _, f := range files {
		total++
		node, parseErr := htmlparse.ParseFile(f)
		if parseErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex: error: %s: %v\n", f, parseErr)
			}
			continue
		}
		if indexWorkitemNode(database, node, projectDir, f, batch, validIDs, verbose) {
			upserted++
		} else {
			errCount++
		}
	}
	return total, upserted, errCount
}

// indexWorkitemNode upserts a single parsed work-item node into the features
// read index. gitPath is the file the node came from (an individual .wipnote
// HTML file, or "" for ledger-backed archived items that have no standalone
// file) and is used only to refine timestamps from git history. batch is an
// optional pre-computed lookup from batchGitFileTimestamps (nil is fine —
// callers that don't have one, or whose gitPath is "", simply fall back to
// applyGitTimestamps's own per-file `git log` calls). Returns true on a
// successful upsert. This is the shared indexing path used by both
// reindexFeatureDir (file-backed) and reindexWorkitemLedger (archive-backed) so
// archived rows index identically to live files.
func indexWorkitemNode(database *sql.DB, node *models.Node, projectDir, gitPath string, batch map[string]fileTimestamps, validIDs map[string]bool, verbose bool) bool {
	createdAt, updatedAt := normalizeTimes(node.CreatedAt, node.UpdatedAt)
	if gitPath != "" {
		if c, u, ok := timestampsFromBatch(batch, gitPath, createdAt, updatedAt); ok {
			createdAt, updatedAt = c, u
		} else {
			createdAt, updatedAt = applyGitTimestamps(projectDir, gitPath, createdAt, updatedAt)
		}
	}
	desc := node.Content
	if len([]rune(desc)) > 500 {
		desc = string([]rune(desc)[:499]) + "\u2026"
	}

	stepsTotal := len(node.Steps)
	stepsCompleted := 0
	for _, s := range node.Steps {
		if s.Completed {
			stepsCompleted++
		}
	}

	feat := &dbpkg.Feature{
		ID:             node.ID,
		Type:           mapNodeType(node.Type),
		Title:          node.Title,
		Description:    desc,
		Status:         normalizeStatus(string(node.Status)),
		Priority:       string(node.Priority),
		AssignedTo:     node.AgentAssigned,
		TrackID:        node.TrackID,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		StepsTotal:     stepsTotal,
		StepsCompleted: stepsCompleted,
	}

	if upsertErr := dbpkg.UpsertFeature(database, feat); upsertErr != nil {
		if verbose {
			fmt.Printf("reindex: error: %s: %v\n", node.ID, upsertErr)
		}
		return false
	}
	validIDs[node.ID] = true
	return true
}

func collectSessionIDs(database *sql.DB, validIDs map[string]bool) {
	rows, err := database.Query("SELECT session_id FROM sessions")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && id != "" {
			validIDs[id] = true
		}
	}
}

// collectPlanIDs registers every plan ID in validIDs. Plans are canonical
// nodes, but unlike tracks/features/bugs/spikes they have no row in any node
// table, so no indexing pass ever registers them. validIDs is a target-validity
// whitelist rather than a node table (session IDs live in it for the same
// reason), and both indexNodeEdges and purgeStaleEntries consult it — so an
// unregistered plan ID means every edge touching a plan is dropped on rebuild.
//
// HTML is the canonical node file, so its parsed article ID wins; the YAML stem
// is registered too because reindexPlanEdges emits edges keyed off the YAML and
// a plan can exist as YAML before its HTML has been rendered.
func collectPlanIDs(wipnoteDir string, validIDs map[string]bool) {
	htmlFiles, _ := filepath.Glob(filepath.Join(wipnoteDir, "plans", "*.html"))
	for _, f := range htmlFiles {
		node, err := htmlparse.ParseFile(f)
		if err != nil || node.ID == "" {
			continue
		}
		validIDs[node.ID] = true
	}

	yamlFiles, _ := filepath.Glob(filepath.Join(wipnoteDir, "plans", "*.yaml"))
	for _, f := range yamlFiles {
		if id := strings.TrimSuffix(filepath.Base(f), ".yaml"); id != "" {
			validIDs[id] = true
		}
	}
}

func reindexEdges(database *sql.DB, wipnoteDir string, validIDs map[string]bool) {
	dirs := []struct {
		subdir   string
		nodeType string
	}{
		{"tracks", "track"},
		{"features", "feature"},
		{"bugs", "bug"},
		{"spikes", "spike"},
		// Plans declare edges in their own <nav data-graph-edges> block
		// (contains / blocks / blocked_by / relates_to / implemented_in).
		// They are only scannable because collectPlanIDs registered their IDs
		// — the loop below gates the SOURCE on validIDs too (bug-d5eaf6a4).
		{"plans", "plan"},
	}
	for _, d := range dirs {
		pattern := filepath.Join(wipnoteDir, d.subdir, "*.html")
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			node, err := htmlparse.ParseFile(f)
			if err != nil || !validIDs[node.ID] {
				continue
			}
			indexNodeEdges(database, node, d.nodeType, validIDs)
		}
	}
}

// indexNodeEdges inserts every graph edge declared by node, applying the
// target-validity gate. Shared by reindexEdges (file-backed) and
// reindexWorkitemLedger (archive-backed) so archived items keep their lineage
// edges in graph_edges and remain traversable by wipnote lineage/trace.
//
// Both callers gate the SOURCE on validIDs before calling, so every edge that
// reaches here is declared by a canonical HTML node. That is what licenses the
// tombstone half of the gate: the declaration is git-tracked and permanent even
// when the session it names has been pruned, so graph.EdgeTargetTombstoned
// keeps the edge with a marker rather than erasing the provenance record
// (bug-10e166d8). A target that is neither valid nor session-shaped is still a
// genuine dangling reference and is still dropped.
func indexNodeEdges(database *sql.DB, node *models.Node, fromNodeType string, validIDs map[string]bool) {
	for _, edges := range node.Edges {
		for _, e := range edges {
			props := e.Properties
			switch graph.ClassifyEdgeTarget(e.TargetID, validIDs) {
			case graph.EdgeTargetDangling:
				continue
			case graph.EdgeTargetTombstoned:
				props = graph.MarkEdgeTombstoned(props)
			case graph.EdgeTargetLive:
				// Unchanged: a live target indexes exactly as it always has.
			}
			edgeID := fmt.Sprintf("%s-%s-%s", node.ID, string(e.Relationship), e.TargetID)
			_ = dbpkg.InsertEdge(
				database,
				edgeID, node.ID, fromNodeType,
				e.TargetID, inferNodeTypeFromID(e.TargetID),
				string(e.Relationship),
				props,
			)
		}
	}
}

func inferNodeTypeFromID(id string) string {
	switch {
	case len(id) > 5 && id[:5] == "feat-":
		return "feature"
	case len(id) > 4 && id[:4] == "bug-":
		return "bug"
	case len(id) > 4 && id[:4] == "spk-":
		return "spike"
	case len(id) > 4 && id[:4] == "trk-":
		return "track"
	case len(id) > 5 && id[:5] == "plan-":
		return "plan"
	case len(id) > 5 && id[:5] == "spec-":
		return "spec"
	case len(id) > 5 && id[:5] == "sess-":
		return "session"
	case len(id) > 6 && id[:6] == "recap-":
		return "recap"
	default:
		return "unknown"
	}
}

// fixImplementedInEdges corrects implemented_in edges that have to_node_type='unknown'.
// Session IDs are UUIDs (not prefixed), so inferNodeTypeFromID returns 'unknown' by
// default. This function updates all implemented_in edges with unknown target types
// by re-inferring the correct type from the target ID.
func fixImplementedInEdges(database *sql.DB) {
	// Fetch all implemented_in edges with unknown target type.
	rows, err := database.Query(`
		SELECT id, to_node_id FROM graph_edges
		WHERE relationship_type = 'implemented_in' AND to_node_type = 'unknown'
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	var toFix []struct {
		id       string
		toNodeID string
	}
	for rows.Next() {
		var edge struct {
			id       string
			toNodeID string
		}
		if err := rows.Scan(&edge.id, &edge.toNodeID); err != nil {
			continue
		}
		toFix = append(toFix, edge)
	}

	// Update each edge with the correct inferred type.
	for _, edge := range toFix {
		correctType := inferNodeTypeFromID(edge.toNodeID)
		_, _ = database.Exec(
			`UPDATE graph_edges SET to_node_type = ? WHERE id = ?`,
			correctType, edge.id,
		)
	}
}
