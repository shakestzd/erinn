package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

// lineageKind classifies the routing target for a `wipnote lineage <id>`
// invocation. Routing is purely string-based: prefix → kind.
type lineageKind int

const (
	kindUnknown lineageKind = iota
	kindFeature
	kindBug
	kindSpike
	kindPlan
	kindTrack
	kindSession
	kindCommit
	kindFile
)

// String makes lineageKind printable for test failures.
func (k lineageKind) String() string {
	switch k {
	case kindFeature:
		return "feature"
	case kindBug:
		return "bug"
	case kindSpike:
		return "spike"
	case kindPlan:
		return "plan"
	case kindTrack:
		return "track"
	case kindSession:
		return "session"
	case kindCommit:
		return "commit"
	case kindFile:
		return "file"
	default:
		return "unknown"
	}
}

// lineageHexRe matches commit-shaped hex strings (7-40 chars).
var lineageHexRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// detectLineageKind inspects a CLI argument and returns its routing kind.
// Order matters: ID prefixes win over file path heuristics so an exotic file
// named "feat-x" is still parsed as a work item by intent.
//
// Note: session-ID routing is prefix-only (no length/hex constraint) because
// upstream generators emit multiple schemes — real sessions are `sess-<hex8>`
// but tests and ingest tooling also produce `sess-root-0001`, `sess-orch-abc`,
// etc. Commit-ID routing uses the stricter hex regex because SHAs have a
// fixed alphabet and any accidental collision there would be a bug.
func detectLineageKind(arg string) lineageKind {
	switch {
	case strings.HasPrefix(arg, "feat-"):
		return kindFeature
	case strings.HasPrefix(arg, "bug-"):
		return kindBug
	case strings.HasPrefix(arg, "spk-"):
		return kindSpike
	case strings.HasPrefix(arg, "plan-"):
		return kindPlan
	case strings.HasPrefix(arg, "trk-"):
		return kindTrack
	case strings.HasPrefix(arg, "sess-"):
		return kindSession
	}
	if lineageHexRe.MatchString(arg) {
		return kindCommit
	}
	if strings.ContainsAny(arg, "/.") {
		return kindFile
	}
	return kindUnknown
}

// lineageOpts is the flag bundle for `wipnote lineage`.
//
// depthSet and timelineSet record whether the user explicitly passed the
// corresponding flag on the command line. The commit and file routes reject
// those flags instead of silently ignoring them, so we can't rely on raw
// values — depth defaults to 5 and timeline defaults to false, both of which
// could collide with a deliberate user input.
type lineageOpts struct {
	depth       int
	jsonOut     bool
	timeline    bool
	depthSet    bool
	timelineSet bool
}

// lineageNode is one hop in a forward or backward chain. It is the wire format
// for --json output and a convenient internal representation for tree rendering.
type lineageNode struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	EdgeType string `json:"edge_type"`
	Depth    int    `json:"depth"`
	// Parent is the node ID that this hop was discovered from during BFS. For
	// the pivot's direct neighbours it equals the pivot. Used by the tree
	// renderer to build a real adjacency structure so branched walks don't
	// visually attach grandchildren to the wrong parent.
	Parent string `json:"parent,omitempty"`
	// timestamp is populated for --timeline rendering by joining git_commits /
	// agent_events. Empty when no temporal data is available.
	Timestamp string `json:"timestamp,omitempty"`
}

// lineageJSON is the stable schema emitted by `wipnote lineage --json`.
//
//	{
//	  "root":     "<id>",
//	  "kind":     "feature|bug|...",
//	  "forward":  [{id,type,title,edge_type,depth,timestamp?}, ...],
//	  "backward": [{id,type,title,edge_type,depth,timestamp?}, ...],
//	  "agent_tree": "<indented text>"   // only for session roots
//	}
//
// Forward edges follow `from_node_id = root` outward; backward edges follow
// `to_node_id = root` inward. Each list is depth-ordered (BFS). For session
// roots the agent spawn tree is included as preformatted text so the --json
// output carries the same information as the human-readable view.
type lineageJSON struct {
	Root      string        `json:"root"`
	Kind      string        `json:"kind"`
	Forward   []lineageNode `json:"forward"`
	Backward  []lineageNode `json:"backward"`
	AgentTree string        `json:"agent_tree,omitempty"`
}

// allLineageRels lists all 10 relationship types we traverse. We do NOT subset:
// any of these can carry causal meaning depending on the slice in question.
var allLineageRels = []string{
	string(models.RelBlocks),
	string(models.RelBlockedBy),
	string(models.RelRelatesTo),
	string(models.RelImplements),
	string(models.RelCausedBy),
	string(models.RelSpawnedFrom),
	string(models.RelImplementedIn),
	string(models.RelPartOf),
	string(models.RelContains),
	string(models.RelPlannedIn),
}

// newLineageCmd registers `wipnote lineage <id>` — the headline unified
// causal chain command. It auto-detects the input type, walks graph_edges in
// both directions across all 10 relationship types, and renders a tree.
func newLineageCmd() *cobra.Command {
	opts := lineageOpts{depth: 5}
	cmd := &cobra.Command{
		Use:   "lineage <id>",
		Short: "Walk the causal chain for any work item, session, commit, or file",
		Long: `Auto-detects the ID type and renders the bidirectional causal chain.

Supported inputs:
  feat-/bug-/spk-/plan-/trk- ID  — graph walk across all 10 edge types
  sess-<id>                      — graph walk plus agent spawn tree
  <commit-sha>                   — git commit attribution
  <file/path.go>                 — file-to-feature attribution

Examples:
  wipnote lineage feat-48b3783c
  wipnote lineage plan-3b0d5133 --depth 8
  wipnote lineage sess-abc123 --json
  wipnote lineage feat-48b3783c --timeline`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			// bug-7dbaf552: lineage performs ZERO writes through this
			// handle — bfsWalk, annotateTimestamps, ResolveToMap,
			// RenderAgentTree, TraceCommit and TraceFile are all SELECT-only
			// (trace.go already opens the same TraceCommit/TraceFile
			// primitives read-only). openReadOnlyDB bootstraps the schema /
			// runs migrations on a brief writable handle FIRST (roborev
			// followup: restores the migrate-on-open guarantee for fresh /
			// schema-behind workspaces that a bare mode=ro open dropped) and
			// then hands back a mode=ro handle, so the long bfsWalk read path
			// can never hold the writer's RESERVED lock and the engine
			// hard-rejects any accidental write.
			db, err := openReadOnlyDB(dir)
			if err != nil {
				return err
			}
			defer db.Close()
			opts.depthSet = cmd.Flags().Changed("depth")
			opts.timelineSet = cmd.Flags().Changed("timeline")
			return runLineage(os.Stdout, db, args[0], opts)
		},
	}
	cmd.Flags().IntVar(&opts.depth, "depth", 5, "maximum hop count for graph walk")
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "emit structured JSON output")
	cmd.Flags().BoolVar(&opts.timeline, "timeline", false, "sort results chronologically instead of as a tree")
	return cmd
}

// runLineage is the testable entry point. It dispatches based on
// detectLineageKind, walks the graph in both directions, and renders.
//
// Commit SHAs and file paths short-circuit to the existing attribution
// primitives (TraceCommit / TraceFile) because graph_edges does not store
// commit or file nodes — a bfsWalk rooted at a commit or file would always
// return empty. Work-item, plan, track, and session kinds go through the
// bidirectional graph walker.
func runLineage(w io.Writer, db *sql.DB, arg string, opts lineageOpts) error {
	if opts.depth <= 0 {
		opts.depth = 5
	}
	kind := detectLineageKind(arg)

	switch kind {
	case kindCommit:
		return runLineageCommit(w, db, arg, opts)
	case kindFile:
		return runLineageFile(w, db, arg, opts)
	}

	forward, err := forwardWalk(db, arg, allLineageRels, opts.depth)
	if err != nil {
		return fmt.Errorf("forward walk: %w", err)
	}
	backward, err := backwardWalk(db, arg, allLineageRels, opts.depth)
	if err != nil {
		return fmt.Errorf("backward walk: %w", err)
	}

	if opts.timeline {
		annotateTimestamps(db, forward)
		annotateTimestamps(db, backward)
	}

	// Session roots carry an agent spawn tree as a secondary view. Render it
	// once so both the JSON and text outputs can include it.
	var agentTree string
	if kind == kindSession {
		if tree, treeErr := RenderAgentTree(db, arg); treeErr == nil {
			agentTree = tree
		}
	}

	if opts.jsonOut {
		return renderLineageJSON(w, arg, kind, forward, backward, agentTree)
	}

	if err := renderLineageTree(w, db, arg, kind, forward, backward, opts.timeline); err != nil {
		return err
	}

	if strings.TrimSpace(agentTree) != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Agent spawn chain:")
		fmt.Fprint(w, agentTree)
	}
	return nil
}

// forwardWalk performs a BFS following from_node_id = current outward.
// Returns nodes in BFS order, each annotated with the edge type that reached
// it and the hop depth (1-indexed).
func forwardWalk(db *sql.DB, root string, rels []string, maxDepth int) ([]lineageNode, error) {
	return bfsWalk(db, root, rels, maxDepth, true)
}

// backwardWalk performs a BFS following to_node_id = current inward — i.e.
// "who points at me?". This is the inline reverse query the plan calls for.
func backwardWalk(db *sql.DB, root string, rels []string, maxDepth int) ([]lineageNode, error) {
	return bfsWalk(db, root, rels, maxDepth, false)
}

// bfsWalk is the shared BFS engine for both directions. When forward=true it
// follows from->to edges; when false it follows to->from edges.
func bfsWalk(db *sql.DB, root string, rels []string, maxDepth int, forward bool) ([]lineageNode, error) {
	if maxDepth <= 0 || len(rels) == 0 {
		return nil, nil
	}

	placeholders := strings.Repeat("?,", len(rels))
	placeholders = placeholders[:len(placeholders)-1]
	var query string
	if forward {
		query = fmt.Sprintf(
			`SELECT to_node_id, to_node_type, relationship_type
			 FROM graph_edges
			 WHERE from_node_id = ? AND relationship_type IN (%s)`,
			placeholders,
		)
	} else {
		query = fmt.Sprintf(
			`SELECT from_node_id, from_node_type, relationship_type
			 FROM graph_edges
			 WHERE to_node_id = ? AND relationship_type IN (%s)`,
			placeholders,
		)
	}

	type queueEntry struct {
		id    string
		depth int
	}
	visited := map[string]bool{root: true}
	queue := []queueEntry{{id: root, depth: 0}}
	var result []lineageNode

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		args := make([]any, 0, 1+len(rels))
		args = append(args, cur.id)
		for _, r := range rels {
			args = append(args, r)
		}
		// bug-7dbaf552: the retry unit is THIS QUERY ONLY, never the BFS
		// iteration. On a contended DELETE-journal DB a single neighbour
		// query can transiently SQLITE_BUSY; without a retry the whole
		// `wipnote lineage` invocation fails. RetryOnBusy re-runs only the
		// db.Query call. Critical lock-hygiene: a *sql.Rows from a failed
		// attempt holds a read lock until closed, so if the closure ever
		// captured rows on a BUSY path we MUST close it before the next
		// attempt. Here db.Query returns err WITHOUT a usable rows handle on
		// BUSY (rows is nil / already finalised by the driver), so there is
		// nothing to leak between attempts; rows is assigned only on the
		// terminal (success or hard-error) attempt and is closed exactly
		// once below after iteration. We defensively Close any non-nil rows
		// from a BUSY attempt before retrying to make the invariant explicit
		// and robust to driver changes.
		var rows *sql.Rows
		err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
			r, qerr := db.Query(query, args...)
			if qerr != nil {
				if r != nil { // defensive: never carry an open lock into a retry
					r.Close()
				}
				return qerr
			}
			rows = r
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("query neighbors of %s: %w", cur.id, err)
		}
		for rows.Next() {
			var nid, ntype, rel string
			if err := rows.Scan(&nid, &ntype, &rel); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan neighbor: %w", err)
			}
			if visited[nid] {
				continue
			}
			visited[nid] = true
			node := lineageNode{
				ID:       nid,
				Type:     ntype,
				EdgeType: rel,
				Depth:    cur.depth + 1,
				Parent:   cur.id,
			}
			result = append(result, node)
			queue = append(queue, queueEntry{id: nid, depth: cur.depth + 1})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate neighbors of %s: %w", cur.id, err)
		}
		rows.Close()
	}

	// Resolve titles in one shot for display.
	if len(result) > 0 {
		ids := make([]string, len(result))
		for i, n := range result {
			ids[i] = n.ID
		}
		labels := graph.ResolveToMap(db, ids)
		for i := range result {
			if r, ok := labels[result[i].ID]; ok {
				result[i].Title = r.Title
			}
		}
	}

	return result, nil
}

// sortLineageTimeline sorts nodes in place by ascending Timestamp, pushing
// nodes without a timestamp to the END so "oldest first" rendering is honest
// even when only part of the walk has temporal data.
func sortLineageTimeline(nodes []lineageNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		ai, bj := nodes[i].Timestamp, nodes[j].Timestamp
		if ai == "" && bj == "" {
			return false
		}
		if ai == "" {
			return false
		}
		if bj == "" {
			return true
		}
		return ai < bj
	})
}

// annotateTimestamps fills in lineageNode.Timestamp by joining git_commits
// (commit_hash) and agent_events (session_id). Best-effort: missing rows
// silently leave Timestamp empty so timeline rendering still includes them.
func annotateTimestamps(db *sql.DB, nodes []lineageNode) {
	for i := range nodes {
		var ts sql.NullString
		// bug-7dbaf552: best-effort timestamp lookups still wrapped in
		// RetryOnBusy so a transient SQLITE_BUSY doesn't silently blank the
		// timeline column under contention. QueryRow().Scan() consumes and
		// closes its single row internally, so there is no *sql.Rows to leak
		// across retries — the retry unit is naturally the whole Scan.
		// sql.ErrNoRows is NOT a BusyError, so RetryOnBusy returns it
		// immediately and we keep the original best-effort (ignore-error)
		// behaviour.
		// Try git_commits first (commit-shaped IDs).
		_ = dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
			return db.QueryRow(
				`SELECT timestamp FROM git_commits WHERE commit_hash = ? LIMIT 1`,
				nodes[i].ID,
			).Scan(&ts)
		})
		if !ts.Valid || ts.String == "" {
			// Fall back to agent_events.timestamp via session_id.
			_ = dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
				return db.QueryRow(
					`SELECT MIN(timestamp) FROM agent_events WHERE session_id = ?`,
					nodes[i].ID,
				).Scan(&ts)
			})
		}
		if ts.Valid {
			nodes[i].Timestamp = ts.String
		}
	}
}

// renderLineageJSON emits the stable {root, kind, forward, backward, agent_tree?} schema.
func renderLineageJSON(w io.Writer, root string, kind lineageKind, forward, backward []lineageNode, agentTree string) error {
	out := lineageJSON{
		Root:      root,
		Kind:      kind.String(),
		Forward:   forward,
		Backward:  backward,
		AgentTree: agentTree,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// renderLineageTree prints a human-readable indented tree with the query node
// as the pivot. Backward chains print above the pivot, forward chains below.
// When timeline=true, the same nodes render as a chronological list.
func renderLineageTree(
	w io.Writer,
	db *sql.DB,
	root string,
	kind lineageKind,
	forward, backward []lineageNode,
	timeline bool,
) error {
	rootLabel := graph.FormatNodeLabel(root, graph.ResolveToMap(db, []string{root}))

	sep := strings.Repeat("─", 60)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  Lineage: %s  [%s]\n", rootLabel, kind)
	fmt.Fprintln(w, sep)

	if timeline {
		all := make([]lineageNode, 0, len(forward)+len(backward))
		all = append(all, backward...)
		all = append(all, forward...)
		sortLineageTimeline(all)
		fmt.Fprintln(w, "\n  Timeline (oldest first):")
		if len(all) == 0 {
			fmt.Fprintln(w, "    (no related nodes)")
			return nil
		}
		for _, n := range all {
			ts := n.Timestamp
			if ts == "" {
				ts = "—"
			}
			fmt.Fprintf(w, "    %s  %s  (%s, d%d)\n", ts, n.ID, n.EdgeType, n.Depth)
		}
		return nil
	}

	if len(backward) > 0 {
		fmt.Fprintf(w, "\n  Ancestors (%d):\n", len(backward))
		printLineageBranches(w, root, backward)
	}
	fmt.Fprintf(w, "\n  Pivot: %s\n", rootLabel)
	if len(forward) > 0 {
		fmt.Fprintf(w, "\n  Descendants (%d):\n", len(forward))
		printLineageBranches(w, root, forward)
	}
	if len(forward) == 0 && len(backward) == 0 {
		fmt.Fprintln(w, "\n  (no related nodes — try `wipnote trace` for file/commit attribution)")
	}
	return nil
}

// runLineageCommit dispatches a commit SHA to the existing TraceCommit
// primitive and renders the result. Commits are not graph_edges nodes, so a
// bidirectional bfsWalk would return empty — this is the correct surface.
func runLineageCommit(w io.Writer, db *sql.DB, sha string, opts lineageOpts) error {
	if opts.timelineSet || opts.depthSet {
		return fmt.Errorf("--timeline and --depth are not supported for commit inputs; use `wipnote lineage <work-item-id>` for graph traversal")
	}
	commits, err := dbpkg.TraceCommit(db, sha)
	if err != nil {
		return fmt.Errorf("trace commit: %w", err)
	}
	if opts.jsonOut {
		out := struct {
			Root    string              `json:"root"`
			Kind    string              `json:"kind"`
			Commits []dbpkg.TraceResult `json:"commits"`
		}{Root: sha, Kind: kindCommit.String(), Commits: commits}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	sep := strings.Repeat("─", 60)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  Lineage: %s  [commit]\n", truncate(sha, 10))
	fmt.Fprintln(w, sep)
	if len(commits) == 0 {
		fmt.Fprintln(w, "  (no matching commit — run 'wipnote ingest commits')")
		return nil
	}
	for _, c := range commits {
		fmt.Fprintf(w, "  Commit    %s\n", truncate(c.CommitHash, 10))
		if c.Message != "" {
			fmt.Fprintf(w, "  Message   %s\n", truncate(c.Message, 55))
		}
		if c.SessionID != "" {
			fmt.Fprintf(w, "  Session   %s\n", c.SessionID)
		}
		if c.FeatureID != "" {
			fmt.Fprintf(w, "  Feature   %s\n", c.FeatureID)
		}
		if c.TrackID != "" {
			fmt.Fprintf(w, "  Track     %s\n", c.TrackID)
		}
	}
	return nil
}

// runLineageFile dispatches a file path to the existing TraceFile primitive
// and renders the result. Same rationale as runLineageCommit: files are not
// graph_edges nodes.
func runLineageFile(w io.Writer, db *sql.DB, filePath string, opts lineageOpts) error {
	if opts.timelineSet || opts.depthSet {
		return fmt.Errorf("--timeline and --depth are not supported for file inputs; use `wipnote lineage <work-item-id>` for graph traversal")
	}
	results, err := dbpkg.TraceFile(db, filePath)
	if err != nil {
		return fmt.Errorf("trace file: %w", err)
	}
	if opts.jsonOut {
		out := struct {
			Root     string                  `json:"root"`
			Kind     string                  `json:"kind"`
			Features []dbpkg.FileTraceResult `json:"features"`
		}{Root: filePath, Kind: kindFile.String(), Features: results}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	sep := strings.Repeat("─", 60)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  Lineage: %s  [file]\n", filePath)
	fmt.Fprintln(w, sep)
	if len(results) == 0 {
		fmt.Fprintln(w, "  (no features touch this file — run 'wipnote reindex')")
		return nil
	}
	fmt.Fprintf(w, "\n  Features (%d):\n", len(results))
	for _, r := range results {
		status := r.Status
		if status == "" {
			status = "unknown"
		}
		fmt.Fprintf(w, "    %s  [%s]  %s\n", r.FeatureID, status, truncate(r.Title, 40))
		if r.TrackID != "" {
			fmt.Fprintf(w, "      Track: %s\n", r.TrackID)
		}
	}
	return nil
}

// printLineageBranches renders nodes as a real tree by walking the parent
// adjacency built from each node's Parent field. Prior versions indented by
// `Depth` alone, which was wrong for branched walks: BFS order like
// [A,C,B,D] (where B is under A and D is under C) would print B immediately
// after C at indent 2, visually attaching B to C instead of A. Building a
// children-of-parent map and recursing from the pivot preserves true
// parentage no matter how BFS interleaves siblings.
func printLineageBranches(w io.Writer, pivot string, nodes []lineageNode) {
	byParent := make(map[string][]int, len(nodes))
	for i, n := range nodes {
		byParent[n.Parent] = append(byParent[n.Parent], i)
	}
	var dfs func(parent string, indentLevel int)
	dfs = func(parent string, indentLevel int) {
		for _, idx := range byParent[parent] {
			n := nodes[idx]
			indent := strings.Repeat("  ", indentLevel)
			label := n.ID
			if n.Title != "" {
				label = fmt.Sprintf("%s (%s)", n.ID, truncate(n.Title, 40))
			}
			fmt.Fprintf(w, "  %s[%s] %s\n", indent, n.EdgeType, label)
			dfs(n.ID, indentLevel+1)
		}
	}
	// Render every node reachable from the pivot first, then any orphans that
	// landed in the walk with a missing parent entry — they become additional
	// roots rather than being dropped silently.
	dfs(pivot, 1)
	seen := map[string]bool{pivot: true}
	var collectSeen func(parent string)
	collectSeen = func(parent string) {
		for _, idx := range byParent[parent] {
			seen[nodes[idx].ID] = true
			collectSeen(nodes[idx].ID)
		}
	}
	collectSeen(pivot)
	for _, n := range nodes {
		if seen[n.ID] {
			continue
		}
		// Orphan — its Parent wasn't reachable from the pivot. Render as a
		// new root so partial walks degrade gracefully.
		label := n.ID
		if n.Title != "" {
			label = fmt.Sprintf("%s (%s)", n.ID, truncate(n.Title, 40))
		}
		fmt.Fprintf(w, "  [%s] %s  (orphan)\n", n.EdgeType, label)
		seen[n.ID] = true
		collectSeen(n.ID)
	}
}
