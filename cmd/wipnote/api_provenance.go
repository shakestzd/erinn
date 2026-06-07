package main

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/htmlparse"
)

// provenanceResponse is the JSON shape for /api/provenance/{id}.
type provenanceResponse struct {
	Node       provenanceNode   `json:"node"`
	Upstream   []provenanceLink `json:"upstream"`
	Downstream []provenanceLink `json:"downstream"`
}

type provenanceNode struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	CreatedByAgent  string `json:"created_by_agent,omitempty"`
	CreatedByModel  string `json:"created_by_model,omitempty"`
	CreatedByRole   string `json:"created_by_role,omitempty"`
	CreatedByCLIVer string `json:"created_by_cli_ver,omitempty"`
}

type provenanceLink struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Title        string `json:"title"`
	Relationship string `json:"relationship"`
}

// commitResult is the JSON shape for /api/graph/commits items.
type commitResult struct {
	CommitHash string `json:"commit_hash"`
	Message    string `json:"message"`
	SessionID  string `json:"session_id"`
	Timestamp  string `json:"timestamp"`
}

// fileResult is the JSON shape for /api/graph/files items.
type fileResult struct {
	FilePath   string `json:"file_path"`
	SessionID  string `json:"session_id"`
	ChangeType string `json:"change_type"`
}

// sessionResult is the JSON shape for /api/graph/sessions items.
type sessionResult struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// provenanceHandler handles GET /api/provenance/{id}.
// It returns the node's metadata plus upstream and downstream causal links.
// wipnoteDir is used to read provenance attrs from HTML files for work items.
func provenanceHandler(database *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/provenance/")
		if id == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			respondJSON(w, map[string]string{"error": "not found"})
			return
		}

		node, ok := resolveProvenanceNode(database, id, wipnoteDir)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			respondJSON(w, map[string]string{"error": "not found"})
			return
		}

		upstream := loadUpstreamLinks(database, id, node.Type)
		downstream := loadDownstreamLinks(database, id, node.Type)

		respondJSON(w, provenanceResponse{
			Node:       node,
			Upstream:   upstream,
			Downstream: downstream,
		})
	}
}

// resolveProvenanceNode looks up node metadata from features, sessions, tracks,
// git_commits, feature_files, or agent sources. Agent nodes have no dedicated
// table — they are derived from distinct agent names in agent_lineage_trace
// and sessions.agent_assigned, so resolution comes last.
// wipnoteDir is used to read provenance attrs from HTML files for work items.
func resolveProvenanceNode(database *sql.DB, id string, wipnoteDir string) (provenanceNode, bool) {
	var node provenanceNode
	err := database.QueryRow(
		`SELECT id, COALESCE(type,'feature'), COALESCE(title,''), COALESCE(status,'todo')
		 FROM features WHERE id = ?`, id,
	).Scan(&node.ID, &node.Type, &node.Title, &node.Status)
	if err == nil {
		// Enrich with provenance from HTML file.
		node = enrichNodeProvenanceFromHTML(node, wipnoteDir)
		return node, true
	}

	err = database.QueryRow(
		`SELECT id, 'track', COALESCE(title,''), COALESCE(status,'todo')
		 FROM tracks WHERE id = ?`, id,
	).Scan(&node.ID, &node.Type, &node.Title, &node.Status)
	if err == nil {
		node = enrichNodeProvenanceFromHTML(node, wipnoteDir)
		return node, true
	}

	err = database.QueryRow(
		`SELECT session_id, 'session', COALESCE(title,''), COALESCE(status,'')
		 FROM sessions WHERE session_id = ?`, id,
	).Scan(&node.ID, &node.Type, &node.Title, &node.Status)
	if err == nil {
		return node, true
	}

	var hash, msg string
	err = database.QueryRow(
		`SELECT commit_hash, COALESCE(message,'') FROM git_commits WHERE commit_hash = ? LIMIT 1`, id,
	).Scan(&hash, &msg)
	if err == nil {
		return provenanceNode{ID: hash, Type: "commit", Title: msg, Status: "done"}, true
	}

	var filePath string
	err = database.QueryRow(
		`SELECT file_path FROM feature_files WHERE file_path = ? LIMIT 1`, id,
	).Scan(&filePath)
	if err == nil {
		return provenanceNode{ID: filePath, Type: "file", Title: filePath, Status: ""}, true
	}

	// Agent nodes: id IS the agent_name.
	var agentName string
	err = database.QueryRow(
		`SELECT name FROM (
			SELECT agent_name AS name FROM agent_lineage_trace WHERE agent_name != ''
			UNION
			SELECT agent_assigned AS name FROM sessions WHERE agent_assigned != ''
		) WHERE name = ? LIMIT 1`, id,
	).Scan(&agentName)
	if err == nil {
		return provenanceNode{ID: agentName, Type: "agent", Title: agentName, Status: ""}, true
	}

	return provenanceNode{}, false
}

// enrichNodeProvenanceFromHTML reads provenance attrs from the work-item HTML
// file for node.ID and fills in CreatedBy* fields when they are absent from
// the DB row. Silently skips when wipnoteDir is empty or the file is missing.
func enrichNodeProvenanceFromHTML(node provenanceNode, wipnoteDir string) provenanceNode {
	if wipnoteDir == "" {
		return node
	}
	for _, sub := range []string{"features", "bugs", "spikes", "tracks"} {
		path := filepath.Join(wipnoteDir, sub, node.ID+".html")
		parsed, err := htmlparse.ParseFile(path)
		if err != nil || parsed == nil {
			continue
		}
		node.CreatedByAgent = parsed.CreatedByAgent
		node.CreatedByModel = parsed.CreatedByModel
		node.CreatedByRole = parsed.CreatedByRole
		node.CreatedByCLIVer = parsed.CreatedByCLIVersion
		return node
	}
	return node
}

// loadUpstreamLinks returns nodes that point TO the given id, combining
// persisted graph_edges with query-time-derived edges (the same ones the
// /api/graph endpoint produces for commit/file/agent/session nodes).
func loadUpstreamLinks(database *sql.DB, id string, nodeType string) []provenanceLink {
	links := loadPersistedEdges(database, id, true)
	links = append(links, loadDerivedEdges(database, id, nodeType, true)...)
	links = dedupeLinks(links)
	resolveLinkMetadata(database, links)
	if links == nil {
		return []provenanceLink{}
	}
	return links
}

// loadDownstreamLinks returns nodes that the given id points TO.
func loadDownstreamLinks(database *sql.DB, id string, nodeType string) []provenanceLink {
	links := loadPersistedEdges(database, id, false)
	links = append(links, loadDerivedEdges(database, id, nodeType, false)...)
	links = dedupeLinks(links)
	resolveLinkMetadata(database, links)
	if links == nil {
		return []provenanceLink{}
	}
	return links
}

// loadPersistedEdges returns IDs and relationship types from graph_edges.
// If upstream is true, returns edges pointing TO id; otherwise edges FROM id.
func loadPersistedEdges(database *sql.DB, id string, upstream bool) []provenanceLink {
	var query string
	if upstream {
		query = `SELECT from_node_id, relationship_type FROM graph_edges WHERE to_node_id = ?`
	} else {
		query = `SELECT to_node_id, relationship_type FROM graph_edges WHERE from_node_id = ?`
	}
	rows, err := database.Query(query, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var links []provenanceLink
	for rows.Next() {
		var peerID, rel string
		if err := rows.Scan(&peerID, &rel); err != nil {
			continue
		}
		links = append(links, provenanceLink{ID: peerID, Relationship: rel})
	}
	return links
}

// loadDerivedEdges returns IDs and relationship types from the same sources
// as /api/graph derives at query time — git_commits, feature_files,
// agent_lineage_trace, sessions.parent_session_id, sessions.agent_assigned.
// These edges are not persisted to graph_edges, so a pure graph_edges query
// would miss them.
func loadDerivedEdges(database *sql.DB, id string, nodeType string, upstream bool) []provenanceLink {
	var links []provenanceLink

	switch nodeType {
	case "feature", "bug", "spike":
		if upstream {
			// commits committed_for -> this feature
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT commit_hash FROM git_commits WHERE feature_id = ?`,
				id, "committed_for")...)
			// files touched_by -> this feature
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT file_path FROM feature_files WHERE feature_id = ?`,
				id, "touched_by")...)
			// agents worked_on -> this feature
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT agent_name FROM agent_lineage_trace WHERE feature_id = ? AND agent_name != ''`,
				id, "worked_on")...)
		}
	case "session":
		if upstream {
			// agent ran_as -> this session. Pull from BOTH sources
			// (agent_lineage_trace.agent_name AND sessions.agent_assigned)
			// so a session whose agent was assigned (not lineage-traced)
			// still surfaces its agent link in the provenance panel.
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT name FROM (
					SELECT agent_name AS name FROM agent_lineage_trace
					  WHERE session_id = ? AND agent_name != ''
					UNION
					SELECT agent_assigned AS name FROM sessions
					  WHERE session_id = ? AND agent_assigned != ''
				 )`,
				id, "ran_as", id)...)
			// parent session spawned -> this session
			links = append(links, queryPeerLinks(database,
				`SELECT parent_session_id FROM sessions WHERE session_id = ? AND parent_session_id IS NOT NULL AND parent_session_id != ''`,
				id, "spawned")...)
		} else {
			// this session spawned -> child sessions
			links = append(links, queryPeerLinks(database,
				`SELECT session_id FROM sessions WHERE parent_session_id = ?`,
				id, "spawned")...)
			// this session worked_on -> features. Union of both sources:
			// agent_lineage_trace.feature_id (orchestrator-declared work)
			// and agent_events.feature_id (events emitted during the
			// session). Matches what /api/graph surfaces for session
			// nodes so the panel and canvas agree.
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT feature_id FROM (
					SELECT feature_id FROM agent_lineage_trace
					  WHERE session_id = ? AND feature_id != ''
					UNION
					SELECT feature_id FROM agent_events
					  WHERE session_id = ? AND feature_id != ''
				 )`,
				id, "worked_on", id)...)
			// this session produced -> commits
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT commit_hash FROM git_commits WHERE session_id = ?`,
				id, "produced_by")...)
			// this session produced -> files
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT file_path FROM feature_files WHERE session_id = ?`,
				id, "produced_in")...)
		}
	case "commit":
		if !upstream {
			// commit committed_for -> features
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT feature_id FROM git_commits WHERE commit_hash = ? AND feature_id IS NOT NULL AND feature_id != ''`,
				id, "committed_for")...)
			// commit produced_by -> sessions
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT session_id FROM git_commits WHERE commit_hash = ? AND session_id IS NOT NULL AND session_id != ''`,
				id, "produced_by")...)
		}
	case "file":
		if !upstream {
			// file touched_by -> features
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT feature_id FROM feature_files WHERE file_path = ? AND feature_id IS NOT NULL AND feature_id != ''`,
				id, "touched_by")...)
			// file produced_in -> sessions
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT session_id FROM feature_files WHERE file_path = ? AND session_id IS NOT NULL AND session_id != ''`,
				id, "produced_in")...)
		}
	case "agent":
		if !upstream {
			// agent ran_as -> sessions
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT session_id FROM agent_lineage_trace WHERE agent_name = ?`,
				id, "ran_as")...)
			// agent worked_on -> features
			links = append(links, queryPeerLinks(database,
				`SELECT DISTINCT feature_id FROM agent_lineage_trace WHERE agent_name = ? AND feature_id IS NOT NULL AND feature_id != ''`,
				id, "worked_on")...)
		}
	}
	return links
}

// queryPeerLinks runs a query that returns a single ID column and wraps the
// results as provenanceLink entries with the given relationship. Extra
// positional args after rel are forwarded to database.Query as bind params,
// so a UNION query that needs the same ID bound twice can pass it twice.
func queryPeerLinks(database *sql.DB, query, arg, rel string, extraArgs ...any) []provenanceLink {
	args := append([]any{arg}, extraArgs...)
	rows, err := database.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var links []provenanceLink
	for rows.Next() {
		var peerID string
		if err := rows.Scan(&peerID); err != nil || peerID == "" {
			continue
		}
		links = append(links, provenanceLink{ID: peerID, Relationship: rel})
	}
	return links
}

// dedupeLinks removes duplicate (id, relationship) pairs so derived edges
// that also appear in graph_edges aren't reported twice.
func dedupeLinks(links []provenanceLink) []provenanceLink {
	if len(links) == 0 {
		return links
	}
	seen := make(map[string]struct{}, len(links))
	out := make([]provenanceLink, 0, len(links))
	for _, l := range links {
		key := l.ID + "|" + l.Relationship
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, l)
	}
	return out
}

// resolveLinkMetadata populates Type and Title for each link in place.
func resolveLinkMetadata(database *sql.DB, links []provenanceLink) {
	for i := range links {
		if node, ok := resolveProvenanceNode(database, links[i].ID, ""); ok {
			links[i].Type = node.Type
			links[i].Title = node.Title
		}
	}
}

// commitsForFeatureHandler handles GET /api/graph/commits?feature=X.
func commitsForFeatureHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		featureID := r.URL.Query().Get("feature")
		rows, err := database.Query(
			`SELECT commit_hash, COALESCE(message,''), COALESCE(session_id,''), COALESCE(timestamp,'')
			 FROM git_commits WHERE feature_id = ?
			 ORDER BY timestamp DESC`, featureID,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		results := []commitResult{}
		for rows.Next() {
			var c commitResult
			if err := rows.Scan(&c.CommitHash, &c.Message, &c.SessionID, &c.Timestamp); err != nil {
				continue
			}
			results = append(results, c)
		}
		respondJSON(w, results)
	}
}

// filesForFeatureHandler handles GET /api/graph/files?feature=X.
func filesForFeatureHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		featureID := r.URL.Query().Get("feature")
		rows, err := database.Query(
			`SELECT file_path, COALESCE(session_id,''), COALESCE(operation,'')
			 FROM feature_files WHERE feature_id = ?
			 ORDER BY file_path`, featureID,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		results := []fileResult{}
		for rows.Next() {
			var f fileResult
			if err := rows.Scan(&f.FilePath, &f.SessionID, &f.ChangeType); err != nil {
				continue
			}
			results = append(results, f)
		}
		respondJSON(w, results)
	}
}

// sessionsForFeatureHandler handles GET /api/graph/sessions?feature=X.
func sessionsForFeatureHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		featureID := r.URL.Query().Get("feature")
		rows, err := database.Query(
			`SELECT DISTINCT s.session_id, COALESCE(s.agent_assigned,''), COALESCE(s.status,''),
			        COALESCE(s.created_at,'')
			 FROM sessions s
			 JOIN agent_events e ON e.session_id = s.session_id
			 WHERE e.feature_id = ?`, featureID,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		results := []sessionResult{}
		for rows.Next() {
			var s sessionResult
			if err := rows.Scan(&s.SessionID, &s.Agent, &s.Status, &s.CreatedAt); err != nil {
				continue
			}
			results = append(results, s)
		}
		respondJSON(w, results)
	}
}
