package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/observe/otel/indexer"
	otelreceiver "github.com/shakestzd/wipnote/observe/otel/receiver"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
	"github.com/spf13/cobra"
)

func reindexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Reparse every canonical .wipnote artifact and report what it yields",
		Long: `Reparses every canonical artifact under .wipnote/ — work-item HTML, the
archive/claim/session/gate ledgers, plans, arch cards, recaps, session activity
logs and git history — into a compatibility projection, then reports what that
projection contains and every error hit on the way.

The projection is an in-memory SQLite database private to this process. It is
discarded when the command exits: no project database, WAL/SHM sidecar, or cache
directory is created or updated. Every command builds its own projection on
demand from the same canonical files, so there is no persistent index left to
"sync" and nothing here that a later command inherits.

What the command is FOR, therefore, is verification: it is the only place that
parses the full corpus in one pass and tells you which artifacts fail to parse,
and it reports the row counts a projection built from them yields.`,
		RunE: runReindex,
	}
	cmd.Flags().Bool("full", false, "Accepted for compatibility and ignored — every pass is full (see --help)")
	cmd.Flags().BoolP("verbose", "v", false, "Print one line per error encountered during reindex")
	cmd.AddCommand(reindexBackfillOrphansCmd())
	return cmd
}

// projectionCount is one row of the reindex report.
type projectionCount struct {
	label string
	table string
}

// reportedProjectionTables are the projection tables `wipnote reindex` reports
// on. Counting the rebuilt table is deliberately preferred over threading a
// counter through every pass: it reports what the projection actually ENDS UP
// containing rather than what each pass believed it wrote, which is the number
// a reader of this command cares about.
var reportedProjectionTables = []projectionCount{
	{"work items", "features"},
	{"tracks", "tracks"},
	{"graph edges", "graph_edges"},
	{"sessions", "sessions"},
	{"agent events", "agent_events"},
	{"claim episodes", "claim_episodes"},
	{"gate records", "gate_records"},
	{"recaps", "recaps"},
	{"arch cards", "arch_cards"},
	{"feature files", "feature_files"},
	{"git commits", "git_commits"},
}

// runReindex rebuilds the compatibility projection from canonical artifacts and
// reports the result.
//
// The previous incremental machinery (reparse only files changed since
// metadata's last_indexed_commit, else fall back to a full pass) is gone with
// the persistent database it served. It cannot be adapted: incrementality was
// defined against an index that survived between runs, and the projection now
// starts EMPTY every time. A git-diff-scoped pass over an empty projection does
// not produce a stale index, it produces a projection containing only the files
// that happened to change — which is worse than useless, so --full is now the
// only behaviour and the flag is inert.
func runReindex(cmd *cobra.Command, _ []string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	verbose, _ := cmd.Flags().GetBool("verbose")
	out := cmd.OutOrStdout()

	database, err := dbpkg.OpenEphemeralProjection()
	if err != nil {
		return fmt.Errorf("open ephemeral projection: %w", err)
	}
	defer database.Close()

	started := time.Now()
	errCount, err := rebuildFullProjection(database, wipnoteDir, verbose, out, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\nRebuilt the compatibility projection from canonical .wipnote artifacts in %s.\n",
		time.Since(started).Truncate(time.Millisecond))
	for _, c := range reportedProjectionTables {
		var n int
		if err := database.QueryRow("SELECT COUNT(*) FROM " + c.table).Scan(&n); err != nil {
			fmt.Fprintf(out, "  %-16s (unavailable: %v)\n", c.label, err)
			continue
		}
		fmt.Fprintf(out, "  %-16s %d\n", c.label, n)
	}
	if errCount > 0 {
		fmt.Fprintf(out, "\n%d canonical artifact(s) failed to parse or index.", errCount)
		if !verbose {
			fmt.Fprint(out, " Re-run with --verbose to see each one.")
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "\nThe projection is in-memory and process-local — it was discarded when this")
	fmt.Fprintln(out, "command exited. No project database or cache file was written.")
	return nil
}

// rebuildFullProjection populates database with EVERYTHING derivable from
// canonical state, and returns how many artifacts failed along the way.
//
// It is the full sweep: hydrateCompatibilityDB (what every command builds) plus
// the passes too expensive to run on an ordinary command's pre-run path — the
// ones that walk git history, the whole sessions tree, or every telemetry
// shard. Split out from runReindex so tests can build the same projection the
// command builds instead of re-deriving a partial one.
func rebuildFullProjection(database *sql.DB, wipnoteDir string, verbose bool, out, errOut io.Writer) (int, error) {
	projectDir := filepath.Dir(wipnoteDir)

	errCount := 0

	// The node and edge halves of the SAME rebuild every command performs via
	// openDB, so what this command verifies is exactly what other commands will
	// get — not a parallel implementation that can drift from it.
	//
	// reindexSessions goes BETWEEN the halves, not after. It parses per-session
	// activity HTML into agent_events and the richer sessions rows, so it must
	// follow the node passes (agent_events.feature_id is a foreign key into
	// features) and precede the edge passes (which resolve implemented_in
	// targets against the sessions table, and would otherwise let the ledger's
	// placeholder row win). It is not in hydrateCompatibilityDB itself because
	// walking the whole sessions tree is exactly the kind of cost that must not
	// land on an ordinary command's pre-run path (bug-1f338b5b, bug-4e5816f4).
	validIDs := make(map[string]bool)
	hydrateNodePasses(database, wipnoteDir, validIDs)

	sessTotal, sessUpserted, sessErrs := reindexSessions(database, filepath.Join(wipnoteDir, "sessions"), projectDir)
	errCount += sessErrs
	if sessUpserted > 0 || sessErrs > 0 {
		fmt.Fprintf(out, "  sessions: %d events upserted, %d errors (of %d session files)\n",
			sessUpserted, sessErrs, sessTotal)
	}

	hydrateEdgePasses(database, wipnoteDir, validIDs)

	// The remaining passes walk git history or every telemetry shard — same
	// cost argument, same reason they live only here.
	trailerCount, trailerErr := reindexCommitTrailers(database, projectDir)
	if trailerErr != nil {
		errCount++
		fmt.Fprintf(errOut, "warning: commit trailer ingestion: %v\n", trailerErr)
	} else if trailerCount > 0 {
		fmt.Fprintf(out, "  commit trailers: %d feature links from Refs/Fixes trailers\n", trailerCount)
	}

	fileCount, ffErr := reindexFeatureFiles(database, projectDir)
	if ffErr != nil {
		errCount++
		fmt.Fprintf(errOut, "warning: feature_files rebuild: %v\n", ffErr)
	} else if fileCount > 0 {
		fmt.Fprintf(out, "  feature_files: %d file associations rebuilt\n", fileCount)
	}

	archTotal, archUpserted, archErrs := reindexArchCards(database, wipnoteDir, verbose)
	errCount += archErrs
	if archUpserted > 0 || archErrs > 0 {
		fmt.Fprintf(out, "  arch cards: %d upserted, %d errors (of %d card files)\n",
			archUpserted, archErrs, archTotal)
	}

	// Telemetry: replay every per-session events.ndjson into otel_signals.
	// Sessions must already be indexed above — the indexer skips shards with no
	// row in the sessions table (its orphan filter), so a telemetry pass that
	// ran first would silently index nothing.
	signals, signalErrs := replayTelemetryShards(database, wipnoteDir)
	errCount += signalErrs
	if signals > 0 || signalErrs > 0 {
		fmt.Fprintf(out, "  telemetry: %d signals replayed from session NDJSON (%d errors)\n", signals, signalErrs)
	}

	// Two passes the legacy full path ran that are deliberately NOT here:
	//
	//   • purgeStaleEntries — it deleted index rows whose canonical file had
	//     disappeared. A projection built fresh from the files that exist right
	//     now cannot contain a row for a file that does not, so there is
	//     nothing stale to purge.
	//   • backfillGateLedgerFromIndex — it moved gate runs recorded in the
	//     persistent index BEFORE the canonical ledger existed into that
	//     ledger. Those rows lived only in the database that is now gone; the
	//     projection's gate_records are themselves projected from the ledger
	//     (reindexGateRecords), so running the backfill could only copy the
	//     ledger onto itself.

	return errCount, nil
}

// maxTelemetryDrainPasses bounds the replay loop below. The indexer consumes at
// most maxBytesPerTick per session per pass so one huge shard cannot monopolise
// the writer, which means a full replay needs several passes. bug-b2471635 was
// an unbounded version of this loop spinning forever on a shard that could not
// advance, so the ceiling is explicit and progress-checked rather than trusted.
const maxTelemetryDrainPasses = 512

// replayTelemetryShards drives the NDJSON indexer over every session shard until
// it stops making progress, materialising otel_signals into database. Returns
// the signal count landed and an error count.
//
// This replaces reindexOtelEvents, which took a DB PATH, opened its own writable
// handle, and reset every .index-offset to zero first. None of that survives the
// cutover: there is no path to open, and the indexer's read position is now
// in-memory and starts at zero anyway — so resetting the on-disk marker would
// only have clobbered the progress signal that retention, prune and the
// SessionEnd hook read (see observe/otel/indexer's package comment).
func replayTelemetryShards(database *sql.DB, wipnoteDir string) (int, int) {
	writer, err := otelreceiver.NewWriterFromDB(database)
	if err != nil {
		return 0, 1
	}
	idxr := indexer.New(wipnoteDir, sqls.New(writer)).
		WithDB(database).
		WithWriteDB(database)

	ctx := context.Background()
	for pass := 0; pass < maxTelemetryDrainPasses; pass++ {
		idxr.RunOnce(ctx)
		caughtUp := true
		for _, info := range idxr.Status() {
			if info.LagBytes > 0 {
				caughtUp = false
				break
			}
		}
		if caughtUp {
			break
		}
	}

	errCount := 0
	for _, info := range idxr.Status() {
		if info.LastError != "" {
			errCount++
		}
	}
	var signals int
	if err := database.QueryRow(`SELECT COUNT(*) FROM otel_signals`).Scan(&signals); err != nil {
		return 0, errCount + 1
	}
	return signals, errCount
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

	// Batched git-log walks for every file in this directory instead of two
	// `git log` subprocesses per file (bug-4e5816f4), and since feat-2bd74c58
	// the walks are shared across every work-item directory and the per-file
	// --follow call fires only for paths a bulk walk cannot resolve
	// (renames/copies) rather than for all of them (bug-085e3337).
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
