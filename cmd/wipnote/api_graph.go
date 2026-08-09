package main

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/projection"
)

// graphNode represents a work item node in the graph response.
type graphNode struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Kind     string `json:"kind,omitempty"`
	Edges    int    `json:"edges"`
	Activity int    `json:"activity"` // agent_events count for this node
}

// graphEdge represents a directed edge between two nodes.
type graphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

// graphData is the full response shape for /api/graph.
type graphData struct {
	Nodes []graphNode        `json:"nodes"`
	Edges []graphEdge        `json:"edges"`
	Caps  map[string]capInfo `json:"caps,omitempty"`
}

// capInfo shows how many nodes of a type were available vs shown.
type capInfo struct {
	Total int `json:"total"`
	Shown int `json:"shown"`
}

// perTypeCaps limits high-cardinality types. Uncapped types are absent.
var perTypeCaps = map[string]int{
	"session": 300,
	"commit":  200,
	"file":    200,
	"agent":   100,
}

// graphAPIHandler returns a force-directed graph payload for the dashboard.
// By default it filters to nodes that have at least one edge; pass ?all=true
// to include orphan nodes as well.
func graphAPIHandler(database *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		includeAll := r.URL.Query().Get("all") == "true"

		snap, err := projection.Load(wipnoteDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		nodes, edges := projectionGraphPayload(snap)

		// Type filter: ?types=feature,session limits to those types.
		caps := make(map[string]capInfo)
		if typesParam := r.URL.Query().Get("types"); typesParam != "" {
			allowed := make(map[string]bool)
			for _, t := range strings.Split(typesParam, ",") {
				allowed[strings.TrimSpace(t)] = true
			}
			filtered := make([]graphNode, 0, len(nodes))
			for _, n := range nodes {
				if allowed[n.Type] {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
		}

		// Agent filter: ?agent=<name> restricts nodes to the set of
		// sessions, features, and files that the named agent interacted
		// with (via agent_lineage_trace). Tracks/plans/bugs/spikes
		// survive only if they're reachable from one of those
		// interactions, so the graph contracts to "what this agent
		// actually touched."
		if agentName := r.URL.Query().Get("agent"); agentName != "" {
			nodes = filterByAgentProjection(snap, nodes, agentName)
		}

		// Per-type caps: sort capped types by activity DESC, truncate.
		byType := make(map[string][]int) // type → indices into nodes
		for i, n := range nodes {
			byType[n.Type] = append(byType[n.Type], i)
		}
		keep := make(map[int]bool, len(nodes))
		for t, indices := range byType {
			cap, capped := perTypeCaps[t]
			total := len(indices)
			if capped && total > cap {
				// Sort by activity DESC — keep highest-activity nodes.
				sortByActivity(nodes, indices)
				for _, idx := range indices[:cap] {
					keep[idx] = true
				}
				caps[t] = capInfo{Total: total, Shown: cap}
			} else {
				for _, idx := range indices {
					keep[idx] = true
				}
				if capped {
					caps[t] = capInfo{Total: total, Shown: total}
				}
			}
		}
		if len(caps) > 0 {
			capped := make([]graphNode, 0, len(keep))
			for i, n := range nodes {
				if keep[i] {
					capped = append(capped, n)
				}
			}
			nodes = capped
		}

		// Filter orphans unless ?all=true.
		//
		// Two thresholds by type to match visual weight:
		//   - Work-item nodes (feature/bug/spike/plan/track) need >=1 edge.
		//   - High-cardinality provenance types (commit/file/session/agent)
		//     need >=2 edges — degree-1 nodes are decorative leaves that
		//     add clutter without contributing to any causal chain beyond
		//     their one anchor.
		if !includeAll {
			highCardinality := map[string]bool{
				"commit": true, "file": true, "session": true, "agent": true,
			}
			filtered := make([]graphNode, 0, len(nodes))
			for _, n := range nodes {
				threshold := 1
				if highCardinality[n.Type] {
					threshold = 2
				}
				if n.Edges >= threshold {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
		}

		// Drop edges whose endpoints are not in the payload. This runs on EVERY
		// path, including ?all=true, because d3.forceLink throws on a link
		// naming an unknown node id — one dangling edge blanks the whole graph.
		//
		// The tombstone policy makes that reachable in normal operation: an
		// item→pruned-session edge is now kept in graph_edges by design
		// (bug-10e166d8), and a pruned session has no sessions row to become a
		// node from. The graph view already drops edges to live-but-quiet
		// sessions the same way — loadGraphNodes only admits sessions with
		// meaningful activity — so tombstones inherit that existing policy
		// rather than a new one. `wipnote lineage` and /api/provenance are the
		// surfaces that render them, each with its own pruned marker.
		nodeSet := make(map[string]struct{}, len(nodes))
		for _, n := range nodes {
			nodeSet[n.ID] = struct{}{}
		}
		presentEdges := make([]graphEdge, 0, len(edges))
		for _, e := range edges {
			if _, ok := nodeSet[e.Source]; !ok {
				continue
			}
			if _, ok := nodeSet[e.Target]; !ok {
				continue
			}
			presentEdges = append(presentEdges, e)
		}
		edges = presentEdges

		if nodes == nil {
			nodes = []graphNode{}
		}
		if edges == nil {
			edges = []graphEdge{}
		}

		var capsOut map[string]capInfo
		if len(caps) > 0 {
			capsOut = caps
		}
		respondJSON(w, graphData{Nodes: nodes, Edges: edges, Caps: capsOut})
	}
}

func projectionGraphPayload(snap *projection.Snapshot) ([]graphNode, []graphEdge) {
	nodes := make([]graphNode, 0, len(snap.NodeOrder))
	for _, id := range snap.NodeOrder {
		n := snap.Nodes[id]
		nodes = append(nodes, graphNode{
			ID:       n.ID,
			Type:     n.Type,
			Title:    n.Title,
			Status:   n.Status,
			Edges:    len(snap.Out[n.ID]) + len(snap.In[n.ID]),
			Activity: projectionActivity(snap, n.ID),
		})
	}
	edges := make([]graphEdge, 0, len(snap.Edges))
	for _, e := range snap.Edges {
		edges = append(edges, graphEdge{Source: e.FromID, Target: e.ToID, Type: e.Relationship})
	}
	return nodes, deduplicateEdges(edges)
}

func projectionActivity(snap *projection.Snapshot, id string) int {
	var count int
	for _, c := range snap.Claims {
		if c.WorkItemID == id || c.SessionID == id || c.RootSessionID == id {
			count++
		}
	}
	for _, g := range snap.Gates {
		if g.WorkItemID == id || g.SessionID == id {
			count++
		}
	}
	return count
}

// loadGraphNodes fetches all work items (features, bugs, spikes from the
// features table) plus tracks from the tracks table, and architecture cards
// from archSrc. Returns nodes and a parallel slice of track IDs for implicit
// edge derivation. A nil archSrc omits architecture nodes.
func loadGraphNodes(database *sql.DB, archSrc graph.ArchSource) ([]graphNode, []string, error) {
	var nodes []graphNode
	var trackIDs []string

	// Features, bugs, spikes (all stored in features table).
	rows, err := database.Query(`
		SELECT id, COALESCE(type,'feature'), title, COALESCE(status,'todo'),
		       COALESCE(track_id,'')
		FROM features
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n graphNode
		var tid string
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Status, &tid); err != nil {
			continue
		}
		nodes = append(nodes, n)
		trackIDs = append(trackIDs, tid)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Tracks (separate table).
	trows, err := database.Query(`
		SELECT id, 'track', title, COALESCE(status,'todo')
		FROM tracks
		ORDER BY created_at DESC`)
	if err != nil {
		return nodes, trackIDs, nil // non-fatal, tracks table may not exist
	}
	defer trows.Close()
	for trows.Next() {
		var n graphNode
		if err := trows.Scan(&n.ID, &n.Type, &n.Title, &n.Status); err != nil {
			continue
		}
		nodes = append(nodes, n)
		trackIDs = append(trackIDs, "") // tracks don't have a parent track
	}

	// Architecture memory cards, read from their canonical HTML store rather
	// than the retired arch_cards mirror (spk-e6e82b5a).
	for _, c := range archGraphNodes(archSrc) {
		nodes = append(nodes, c)
		trackIDs = append(trackIDs, "")
	}

	// Sessions: include sessions with meaningful activity (>5 events and
	// at least one message) OR sessions that appear in agent_lineage_trace
	// (subagent sessions) OR sessions with a parent_session_id set.
	// Capped at 500 to avoid overwhelming the graph.
	//
	// The SELECT pulls enough for a useful node label: the sessions.title
	// column (set by the background titler for human sessions), the first
	// user message (as a fallback title source), the created_at timestamp
	// (for a last-resort "Apr 11 06:57" label), and the agent type. A
	// graph-local pickSessionLabel helper then picks the best of the three.
	srows, serr := database.Query(`
		SELECT s.session_id,
		       COALESCE(s.agent_assigned, 'session'),
		       COALESCE(s.status, 'completed'),
		       COALESCE(s.title, '') AS title,
		       COALESCE((SELECT SUBSTR(m.content, 1, 160)
		                 FROM messages m
		                 WHERE m.session_id = s.session_id AND m.role = 'user'
		                 ORDER BY m.ordinal LIMIT 1), '') AS first_msg,
		       COALESCE(s.created_at, '') AS created_at
		FROM sessions s
		WHERE (
		    EXISTS (
		        SELECT 1 FROM agent_events e
		        WHERE e.session_id = s.session_id AND e.feature_id != ''
		        GROUP BY e.session_id HAVING COUNT(*) > 5
		    )
		    AND EXISTS (
		        SELECT 1 FROM messages m WHERE m.session_id = s.session_id
		    )
		) OR s.session_id IN (
		    SELECT session_id FROM agent_lineage_trace
		    UNION
		    SELECT session_id FROM sessions
		    WHERE parent_session_id IS NOT NULL AND parent_session_id != ''
		)
		LIMIT 500`)
	if serr == nil {
		defer srows.Close()
		for srows.Next() {
			var n graphNode
			var agent, title, firstMsg, createdAt string
			if err := srows.Scan(&n.ID, &agent, &n.Status, &title, &firstMsg, &createdAt); err != nil {
				continue
			}
			n.Type = "session"
			n.Title = pickSessionLabel(n.ID, title, firstMsg, createdAt)
			nodes = append(nodes, n)
			trackIDs = append(trackIDs, "")
		}
	}

	// File nodes — deduplicated by file_path across all features.
	// Agent and commit nodes are intentionally NOT loaded: agents are
	// exposed via the "Filter by agent" dropdown (the actor, not a
	// node), and commits are sub-attributes of the session/feature
	// that produced them (visible in the provenance panel, not as
	// standalone nodes in the graph).
	ffRows, ffErr := database.Query(`
		SELECT file_path, COALESCE(feature_id, ''),
		       COALESCE(session_id, '')
		FROM feature_files
		GROUP BY file_path
		ORDER BY file_path
		LIMIT 500`)
	if ffErr == nil {
		defer ffRows.Close()
		for ffRows.Next() {
			var path, fid, sid string
			if err := ffRows.Scan(&path, &fid, &sid); err != nil {
				continue
			}
			nodes = append(nodes, graphNode{
				ID:    path,
				Type:  "file",
				Title: filepath.Base(path),
			})
			trackIDs = append(trackIDs, "")
		}
	}

	return nodes, trackIDs, nil
}

// pickSessionLabel returns the best human-readable label for a session node
// in the graph. Priority:
//
//  1. sessions.title — set by the background titler for human sessions.
//     Rejected when empty, when it starts with the "[wipnote-titler]"
//     sentinel (placeholder not yet replaced with a real summary), or when
//     it's an obviously-empty placeholder like "--" or "-".
//  2. First user message — truncated to ~56 chars with a trailing ellipsis.
//     Rejected when empty or when it starts with the titler sentinel.
//  3. Time prefix — "MM-DD HH:MM · <short id>" built from created_at.
//  4. Last resort — "session · <short id>".
//
// The function is deliberately display-only; it never touches the database.
func pickSessionLabel(sessionID, title, firstMsg, createdAt string) string {
	const sentinel = "[wipnote-titler]"
	short := sessionID
	if len(short) > 8 {
		short = short[:8]
	}

	if cleanTitle := sanitizeSessionTitle(title, sentinel); cleanTitle != "" {
		return truncateForNodeLabel(cleanTitle)
	}

	if cleanMsg := sanitizeFirstMessage(firstMsg, sentinel); cleanMsg != "" {
		return truncateForNodeLabel(cleanMsg)
	}

	if createdAt != "" {
		// created_at is stored as RFC3339 or "YYYY-MM-DD HH:MM:SS". Both
		// forms start with "YYYY-MM-DD" and contain "HH:MM" at a fixed
		// offset, so a substring slice is enough for a cheap, readable
		// fallback without the time package overhead.
		if len(createdAt) >= 16 {
			datePart := createdAt[5:10]  // "MM-DD"
			timePart := createdAt[11:16] // "HH:MM"
			return datePart + " " + timePart + " · " + short
		}
	}

	return "session · " + short
}

// sanitizeFirstMessage turns a raw first user message into a single-line
// label suitable for a graph node. The heavy lift is unwrapping Claude
// Code slash-command invocations like
//
//	<command-message>wipnote:execute</command-message>
//	<command-name>/wipnote:execute</command-name>
//	<command-args>trk-d8aef97a</command-args>
//
// into the clean form "/wipnote:execute trk-d8aef97a" which reads as a
// proper session description instead of a lump of XML. Falls back to a
// whitespace-collapsed version of the original message for non-command
// sessions.
func sanitizeFirstMessage(msg, sentinel string) string {
	m := strings.TrimSpace(msg)
	if m == "" || strings.HasPrefix(m, sentinel) {
		return ""
	}

	// Slash-command invocations from Claude Code arrive wrapped in
	// <command-name> + <command-args> blocks. Pull the pieces out and
	// stitch them into a clean command line.
	if strings.Contains(m, "<command-name>") {
		name := extractTagContent(m, "command-name")
		args := extractTagContent(m, "command-args")
		var label string
		switch {
		case name != "" && args != "":
			label = name + " " + args
		case name != "":
			label = name
		default:
			label = extractTagContent(m, "command-message")
		}
		label = strings.Join(strings.Fields(label), " ")
		if label != "" {
			return label
		}
	}

	return strings.Join(strings.Fields(m), " ")
}

// extractTagContent returns the text between <tag> and </tag> in s, or ""
// when the tag is missing. Matches the first occurrence only.
func extractTagContent(s, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(s, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(s[start:], close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(s[start : start+end])
}

// sanitizeSessionTitle strips obviously-empty placeholders that still pass
// a plain emptiness check — namely the titler sentinel prefix and dash-only
// strings written by older ingestion paths — and returns "" in those cases
// so pickSessionLabel can fall through to the next source.
func sanitizeSessionTitle(title, sentinel string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, sentinel) {
		return ""
	}
	// Reject titles that are just dashes / underscores — legacy placeholder.
	if strings.Trim(t, "-_ ") == "" {
		return ""
	}
	return t
}

// truncateForNodeLabel clips a label to a length that still fits inside
// graph node circles when rendered by wrapTextInCircle. Soft cut at word
// boundaries when possible, hard cut otherwise.
func truncateForNodeLabel(s string) string {
	const maxLen = 56
	if len(s) <= maxLen {
		return s
	}
	// Prefer cutting at the last space before maxLen so we don't chop a
	// word in half. Fall back to a hard cut when there's no whitespace.
	cut := maxLen
	if idx := strings.LastIndex(s[:maxLen], " "); idx > 20 {
		cut = idx
	}
	return strings.TrimRight(s[:cut], " ,.;:") + "…"
}

// loadSessionFeatureEdges derives edges from agent_events — sessions that
// worked on features create a "worked_on" relationship.
func loadSessionFeatureEdges(database *sql.DB) []graphEdge {
	rows, err := database.Query(`
		SELECT DISTINCT session_id, feature_id
		FROM agent_events
		WHERE feature_id != '' AND session_id != ''
		LIMIT 500`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var edges []graphEdge
	for rows.Next() {
		var sid, fid string
		if err := rows.Scan(&sid, &fid); err != nil {
			continue
		}
		edges = append(edges, graphEdge{
			Source: fid,
			Target: sid,
			Type:   "worked_on",
		})
	}
	return edges
}

// loadActivityCounts returns agent_event counts per feature_id.
// Used for node sizing — more activity = bigger node.
func loadActivityCounts(database *sql.DB) map[string]int {
	counts := make(map[string]int)
	rows, err := database.Query(`
		SELECT feature_id, COUNT(*) FROM agent_events
		WHERE feature_id != ''
		GROUP BY feature_id`)
	if err != nil {
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err == nil {
			counts[id] = n
		}
	}
	return counts
}

// loadTrackCooccurrenceEdges derives track-to-track relationships from
// shared sessions: if a single session worked on features belonging to
// two different tracks, those tracks are related ("co_session").
//
// The previous implementation did a 4-table self-join over the full
// agent_events table (e1 × e2) which was O(events × events) and took
// ~4.5s on a 43k-row table. The replacement below first collapses
// agent_events to its distinct (session_id, track_id) pairs via a CTE
// — typically a few hundred rows — and then self-joins that much
// smaller set. Same result, ~55× faster in practice (bug-72e5a0a8,
// feat-7e313ad6).
func loadTrackCooccurrenceEdges(database *sql.DB) []graphEdge {
	rows, err := database.Query(`
		WITH session_tracks AS (
			SELECT DISTINCT e.session_id, f.track_id
			FROM agent_events e
			JOIN features f ON f.id = e.feature_id
			WHERE f.track_id != ''
		)
		SELECT DISTINCT s1.track_id, s2.track_id
		FROM session_tracks s1
		JOIN session_tracks s2 ON s2.session_id = s1.session_id
		WHERE s1.track_id < s2.track_id
		LIMIT 200`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var edges []graphEdge
	for rows.Next() {
		var src, tgt string
		if err := rows.Scan(&src, &tgt); err != nil {
			continue
		}
		edges = append(edges, graphEdge{
			Source: src,
			Target: tgt,
			Type:   "co_session",
		})
	}
	return edges
}

// loadCommitEdges derives two edge types from git_commits:
//   - committed_for: commit -> feature (when feature_id is set)
//   - produced_by:   commit -> session (when session_id is set)
//
// git_commits has composite PK (commit_hash, session_id), so a single commit
// may appear with multiple (feature_id, session_id) tuples. Query DISTINCT
// pairs separately for each edge type — grouping by commit_hash alone would
// silently drop all but one edge per commit.
func loadCommitEdges(database *sql.DB) []graphEdge {
	var edges []graphEdge

	// committed_for edges: distinct (commit, feature) pairs.
	fRows, err := database.Query(`
		SELECT DISTINCT commit_hash, feature_id FROM git_commits
		WHERE feature_id IS NOT NULL AND feature_id != ''`)
	if err == nil {
		defer fRows.Close()
		for fRows.Next() {
			var hash, fid string
			if err := fRows.Scan(&hash, &fid); err != nil {
				continue
			}
			edges = append(edges, graphEdge{Source: hash, Target: fid, Type: "committed_for"})
		}
	}

	// produced_by edges: distinct (commit, session) pairs.
	sRows, err := database.Query(`
		SELECT DISTINCT commit_hash, session_id FROM git_commits
		WHERE session_id IS NOT NULL AND session_id != ''`)
	if err == nil {
		defer sRows.Close()
		for sRows.Next() {
			var hash, sid string
			if err := sRows.Scan(&hash, &sid); err != nil {
				continue
			}
			edges = append(edges, graphEdge{Source: hash, Target: sid, Type: "produced_by"})
		}
	}
	return edges
}

// loadFileEdges derives two edge types from feature_files:
//   - produced_in: file -> session (when session_id is non-null)
//   - touched_by:  file -> feature (when feature_id is set)
func loadFileEdges(database *sql.DB) []graphEdge {
	rows, err := database.Query(`
		SELECT DISTINCT file_path, COALESCE(feature_id, ''),
		       COALESCE(session_id, '')
		FROM feature_files`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var edges []graphEdge
	for rows.Next() {
		var path, fid, sid string
		if err := rows.Scan(&path, &fid, &sid); err != nil {
			continue
		}
		if sid != "" {
			edges = append(edges, graphEdge{Source: path, Target: sid, Type: "produced_in"})
		}
		if fid != "" {
			edges = append(edges, graphEdge{Source: path, Target: fid, Type: "touched_by"})
		}
	}
	return edges
}

// loadSessionHierarchyEdges derives parent→child "spawned" edges from the
// sessions.parent_session_id column.
func loadSessionHierarchyEdges(database *sql.DB) []graphEdge {
	rows, err := database.Query(`
		SELECT session_id, parent_session_id FROM sessions
		WHERE parent_session_id IS NOT NULL AND parent_session_id != ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var edges []graphEdge
	for rows.Next() {
		var childID, parentID string
		if err := rows.Scan(&childID, &parentID); err != nil {
			continue
		}
		edges = append(edges, graphEdge{
			Source: parentID, Target: childID, Type: "spawned",
		})
	}
	return edges
}

// loadAgentLineageEdges derives edges from agent_lineage_trace:
//   - "spawned": root_session_id → session_id (excludes self-edges)
//   - "worked_on": session_id → feature_id (when set)
//
// Deduplication with loadSessionHierarchyEdges is handled by
// deduplicateEdges in graphAPIHandler.
func loadAgentLineageEdges(database *sql.DB) []graphEdge {
	rows, err := database.Query(`
		SELECT session_id, root_session_id, COALESCE(feature_id, '')
		FROM agent_lineage_trace
		WHERE session_id != root_session_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var edges []graphEdge
	for rows.Next() {
		var sessionID, rootSessionID, featureID string
		if err := rows.Scan(&sessionID, &rootSessionID, &featureID); err != nil {
			continue
		}
		edges = append(edges, graphEdge{
			Source: rootSessionID, Target: sessionID, Type: "spawned",
		})
		if featureID != "" {
			edges = append(edges, graphEdge{
				Source: sessionID, Target: featureID, Type: "worked_on",
			})
		}
	}
	return edges
}

// loadAgentEdges derives agent→session (ran_as) and agent→feature (worked_on)
// edges from agent_lineage_trace.
func loadAgentEdges(database *sql.DB) []graphEdge {
	rows, err := database.Query(`
		SELECT agent_name, session_id, COALESCE(feature_id, '')
		FROM agent_lineage_trace
		WHERE agent_name != ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var edges []graphEdge
	for rows.Next() {
		var agent, sid, fid string
		if err := rows.Scan(&agent, &sid, &fid); err != nil {
			continue
		}
		edges = append(edges, graphEdge{Source: agent, Target: sid, Type: "ran_as"})
		if fid != "" {
			edges = append(edges, graphEdge{Source: agent, Target: fid, Type: "worked_on"})
		}
	}
	return edges
}

// filterByAgent returns only the nodes the named agent touched. The
// agent dropdown (agentsHandler) lists agents from a UNION of
// agent_lineage_trace.agent_name AND sessions.agent_assigned, so this
// filter must match BOTH sources or "assigned-only" agents will
// appear in the dropdown but produce an empty graph when selected.
//
// Kept nodes:
//   - sessions where agent_lineage_trace.agent_name = X OR sessions.agent_assigned = X
//   - features referenced by those sessions (via lineage.feature_id OR agent_events.feature_id)
//   - files produced by those sessions (feature_files.session_id)
//   - tracks that contain any kept feature (features.track_id FK)
func filterByAgent(database *sql.DB, nodes []graphNode, agentName string) []graphNode {
	keep := make(map[string]bool)
	sessionIDs := make([]string, 0, 32)

	// Pass 1: sessions + features from agent_lineage_trace.
	sfRows, err := database.Query(`
		SELECT DISTINCT session_id, COALESCE(feature_id, '')
		FROM agent_lineage_trace WHERE agent_name = ?`, agentName)
	if err != nil {
		return nodes
	}
	for sfRows.Next() {
		var sid, fid string
		if err := sfRows.Scan(&sid, &fid); err != nil {
			continue
		}
		if sid != "" {
			if !keep[sid] {
				sessionIDs = append(sessionIDs, sid)
			}
			keep[sid] = true
		}
		if fid != "" {
			keep[fid] = true
		}
	}
	sfRows.Close()

	// Pass 2: sessions from sessions.agent_assigned. Features linked to
	// these sessions are pulled via agent_events.feature_id below.
	assignedRows, aerr := database.Query(`
		SELECT DISTINCT session_id FROM sessions
		WHERE agent_assigned = ? AND session_id != ''`, agentName)
	if aerr == nil {
		for assignedRows.Next() {
			var sid string
			if err := assignedRows.Scan(&sid); err == nil && sid != "" {
				if !keep[sid] {
					sessionIDs = append(sessionIDs, sid)
				}
				keep[sid] = true
			}
		}
		assignedRows.Close()
	}

	if len(sessionIDs) > 0 {
		placeholders := make([]string, len(sessionIDs))
		args := make([]any, len(sessionIDs))
		for i, id := range sessionIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		inClause := strings.Join(placeholders, ",")

		// Features referenced by any agent_event in those sessions —
		// covers the assigned-only path which has no agent_lineage_trace row.
		aeRows, aeerr := database.Query(
			`SELECT DISTINCT feature_id FROM agent_events
			 WHERE session_id IN (`+inClause+`) AND feature_id != ''`, args...)
		if aeerr == nil {
			for aeRows.Next() {
				var fid string
				if err := aeRows.Scan(&fid); err == nil {
					keep[fid] = true
				}
			}
			aeRows.Close()
		}

		// Files produced by any of those sessions.
		fRows, ferr := database.Query(
			`SELECT DISTINCT file_path FROM feature_files
			 WHERE session_id IN (`+inClause+`)`, args...)
		if ferr == nil {
			for fRows.Next() {
				var fp string
				if err := fRows.Scan(&fp); err == nil {
					keep[fp] = true
				}
			}
			fRows.Close()
		}
	}

	// Tracks kept if any surviving feature belongs to them (track_id FK).
	// Query the combined feature set from BOTH agent sources.
	trackKeep := make(map[string]bool)
	tRows, terr := database.Query(`
		SELECT DISTINCT f.track_id FROM features f WHERE f.id IN (
			SELECT feature_id FROM agent_lineage_trace
			  WHERE agent_name = ? AND feature_id != ''
			UNION
			SELECT e.feature_id FROM agent_events e
			  JOIN sessions s ON s.session_id = e.session_id
			  WHERE s.agent_assigned = ? AND e.feature_id != ''
		) AND f.track_id != ''`, agentName, agentName)
	if terr == nil {
		for tRows.Next() {
			var tid string
			if err := tRows.Scan(&tid); err == nil {
				trackKeep[tid] = true
			}
		}
		tRows.Close()
	}

	filtered := make([]graphNode, 0, len(keep))
	for _, n := range nodes {
		if keep[n.ID] || (n.Type == "track" && trackKeep[n.ID]) {
			filtered = append(filtered, n)
		}
	}
	return filtered
}

func filterByAgentProjection(snap *projection.Snapshot, nodes []graphNode, agentName string) []graphNode {
	keep := make(map[string]bool)
	for _, n := range snap.Nodes {
		if n.Agent == agentName {
			keep[n.ID] = true
		}
	}
	for _, c := range snap.Claims {
		if sessionAgent(snap, c.SessionID) != agentName && sessionAgent(snap, c.RootSessionID) != agentName {
			continue
		}
		keep[c.SessionID] = true
		keep[c.RootSessionID] = true
		keep[c.WorkItemID] = true
	}
	for _, e := range snap.Edges {
		if keep[e.FromID] || keep[e.ToID] {
			keep[e.FromID] = true
			keep[e.ToID] = true
		}
	}
	filtered := make([]graphNode, 0, len(nodes))
	for _, n := range nodes {
		if keep[n.ID] {
			filtered = append(filtered, n)
		}
	}
	return filtered
}

func sessionAgent(snap *projection.Snapshot, sessionID string) string {
	if n, ok := snap.Nodes[sessionID]; ok && n.Type == "session" {
		return n.Agent
	}
	return ""
}

// agentsHandler returns the distinct agent names for the filter
// dropdown. Ordered by activity DESC so the most-used agents appear
// first.
func agentsHandler(database *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		snap, err := projection.Load(wipnoteDir)
		if err != nil {
			respondJSON(w, []string{})
			return
		}
		counts := map[string]int{}
		for _, n := range snap.Nodes {
			if n.Agent == "" {
				continue
			}
			counts[n.Agent] += projectionActivity(snap, n.ID)
			if counts[n.Agent] == 0 {
				counts[n.Agent] = 1
			}
		}
		out := make([]string, 0, len(counts))
		for name := range counts {
			out = append(out, name)
		}
		sort.Slice(out, func(i, j int) bool { return counts[out[i]] > counts[out[j]] })
		respondJSON(w, out)
	}
}

// loadGraphEdges fetches all rows from graph_edges.
func loadGraphEdges(database *sql.DB) ([]graphEdge, error) {
	rows, err := database.Query(`
		SELECT from_node_id, to_node_id, relationship_type
		FROM graph_edges`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []graphEdge
	for rows.Next() {
		var e graphEdge
		if err := rows.Scan(&e.Source, &e.Target, &e.Type); err != nil {
			continue
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// sortByActivity sorts the given index slice by the activity of the
// corresponding nodes (descending). Used to keep highest-activity nodes
// when applying per-type caps.
func sortByActivity(nodes []graphNode, indices []int) {
	sort.Slice(indices, func(i, j int) bool {
		return nodes[indices[i]].Activity > nodes[indices[j]].Activity
	})
}

// deduplicateEdges removes duplicate (source, target, type) triples.
func deduplicateEdges(edges []graphEdge) []graphEdge {
	seen := make(map[string]struct{}, len(edges))
	result := make([]graphEdge, 0, len(edges))
	for _, e := range edges {
		key := e.Source + "|" + e.Target + "|" + e.Type
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, e)
	}
	return result
}
