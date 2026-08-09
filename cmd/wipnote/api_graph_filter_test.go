package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

func TestGraphAPI_TypesFilter(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Seed features and tracks.
	database.Exec(`INSERT INTO features (id, type, title, status) VALUES ('f1', 'feature', 'feat 1', 'done')`)
	database.Exec(`INSERT INTO features (id, type, title, status) VALUES ('b1', 'bug', 'bug 1', 'done')`)
	database.Exec(`INSERT INTO tracks (id, title, status) VALUES ('t1', 'track 1', 'done')`)

	handler := graphAPIHandler(database, t.TempDir())

	// Request only features.
	req := httptest.NewRequest("GET", "/api/graph?types=feature&all=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	var data graphData
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, n := range data.Nodes {
		if n.Type != "feature" {
			t.Errorf("expected only features, got type=%q id=%q", n.Type, n.ID)
		}
	}
}

func TestGraphAPI_DefaultReturnsAllTypes(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	database.Exec(`INSERT INTO features (id, type, title, status) VALUES ('f1', 'feature', 'feat 1', 'done')`)
	database.Exec(`INSERT INTO features (id, type, title, status) VALUES ('b1', 'bug', 'bug 1', 'done')`)
	database.Exec(`INSERT INTO tracks (id, title, status) VALUES ('t1', 'track 1', 'done')`)

	handler := graphAPIHandler(database, t.TempDir())
	req := httptest.NewRequest("GET", "/api/graph?all=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	var data graphData
	json.Unmarshal(w.Body.Bytes(), &data)

	types := make(map[string]bool)
	for _, n := range data.Nodes {
		types[n.Type] = true
	}
	if !types["feature"] || !types["bug"] || !types["track"] {
		t.Errorf("expected feature, bug, track types; got %v", types)
	}
}

func TestGraphAPI_PerTypeCaps(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Insert 5 sessions with parent_session_id so they qualify.
	for i := 0; i < 5; i++ {
		database.Exec(`INSERT INTO sessions (session_id, agent_assigned, parent_session_id, status, created_at) VALUES (?, 'claude', 'parent', 'completed', '2026-04-16')`,
			"sess-"+string(rune('A'+i)))
	}

	handler := graphAPIHandler(database, t.TempDir())
	req := httptest.NewRequest("GET", "/api/graph?all=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	var data graphData
	json.Unmarshal(w.Body.Bytes(), &data)

	// With only 5 sessions, cap of 300 should not truncate.
	if data.Caps != nil {
		if ci, ok := data.Caps["session"]; ok && ci.Total != ci.Shown {
			t.Errorf("expected no truncation for 5 sessions, got total=%d shown=%d", ci.Total, ci.Shown)
		}
	}
}

// TestFilterByAgent_AssignedOnlySource is a regression test for the
// case where an agent appears in sessions.agent_assigned but not in
// agent_lineage_trace. agentsHandler lists the agent, so
// filterByAgent must also match it or the dropdown selection yields
// an empty graph. See roborev job 109 finding #1.
func TestFilterByAgent_AssignedOnlySource(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Seed: a feature, a session with agent_assigned only (no lineage
	// row), and an agent_event tying the session to the feature.
	_, err = database.Exec(`INSERT INTO features (id, type, title, status) VALUES ('feat-a', 'feature', 'Feat A', 'done')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES ('sess-x', 'assigned-only-agent', 'completed', '2026-04-16')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO agent_events (event_id, session_id, agent_id, feature_id, event_type, created_at) VALUES ('evt-1', 'sess-x', 'any', 'feat-a', 'tool_call', '2026-04-16T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}

	nodes := []graphNode{
		{ID: "feat-a", Type: "feature", Title: "Feat A"},
		{ID: "sess-x", Type: "session", Title: "sess"},
		{ID: "feat-other", Type: "feature", Title: "Other"},
	}
	filtered := filterByAgent(database, nodes, "assigned-only-agent")

	kept := map[string]bool{}
	for _, n := range filtered {
		kept[n.ID] = true
	}
	if !kept["sess-x"] {
		t.Error("expected assigned-only session sess-x to be kept")
	}
	if !kept["feat-a"] {
		t.Error("expected feature feat-a (linked via agent_events) to be kept")
	}
	if kept["feat-other"] {
		t.Error("expected feat-other to be filtered out")
	}
}

func TestSortByActivity(t *testing.T) {
	nodes := []graphNode{
		{ID: "a", Activity: 10},
		{ID: "b", Activity: 50},
		{ID: "c", Activity: 30},
	}
	indices := []int{0, 1, 2}
	sortByActivity(nodes, indices)
	if indices[0] != 1 || indices[1] != 2 || indices[2] != 0 {
		t.Errorf("expected [1,2,0], got %v", indices)
	}
}

// TestGraphAPI_NoDanglingEdgeEndpoints pins the invariant the D3 renderer
// depends on: every edge in the payload names two nodes that are also in the
// payload. d3.forceLink throws on a link referencing an unknown id, and one
// throw blanks the whole graph.
//
// It is checked under ?all=true because that path used to skip the endpoint
// filter entirely. The tombstone policy (feat-d1439606) makes the gap
// reachable in normal operation: an item→pruned-session edge is now kept in
// graph_edges by design, and a pruned session has no sessions row to become a
// node from.
func TestGraphAPI_NoDanglingEdgeEndpoints(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	database.Exec(`INSERT INTO features (id, type, title, status) VALUES ('feat-graph-1', 'feature', 'feat 1', 'done')`)
	database.Exec(`INSERT INTO tracks (id, title, status) VALUES ('trk-graph-1', 'track 1', 'done')`)

	const prunedSession = "aaaa1111-bbbb-2222-cccc-333344445555"
	if err := dbpkg.InsertEdge(database,
		"feat-graph-1-implemented_in-"+prunedSession, "feat-graph-1", "feature",
		prunedSession, "unknown", "implemented_in",
		map[string]string{"tombstoned": "session"},
	); err != nil {
		t.Fatalf("insert tombstoned edge: %v", err)
	}
	if err := dbpkg.InsertEdge(database,
		"feat-graph-1-part_of-trk-graph-1", "feat-graph-1", "feature",
		"trk-graph-1", "track", "part_of", nil,
	); err != nil {
		t.Fatalf("insert live edge: %v", err)
	}

	for _, url := range []string{"/api/graph?all=true", "/api/graph"} {
		t.Run(url, func(t *testing.T) {
			handler := graphAPIHandler(database, t.TempDir())
			req := httptest.NewRequest("GET", url, nil)
			w := httptest.NewRecorder()
			handler(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status: %d", w.Code)
			}
			var data graphData
			if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			present := make(map[string]bool, len(data.Nodes))
			for _, n := range data.Nodes {
				present[n.ID] = true
			}
			for _, e := range data.Edges {
				if !present[e.Source] {
					t.Errorf("edge %s -%s-> %s names a SOURCE that is not a node in the payload",
						e.Source, e.Type, e.Target)
				}
				if !present[e.Target] {
					t.Errorf("edge %s -%s-> %s names a TARGET that is not a node in the payload",
						e.Source, e.Type, e.Target)
				}
			}
		})
	}

	// Guard against the check passing because the payload is empty.
	handler := graphAPIHandler(database, t.TempDir())
	req := httptest.NewRequest("GET", "/api/graph?all=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	var data graphData
	json.Unmarshal(w.Body.Bytes(), &data)
	if len(data.Edges) == 0 {
		t.Fatalf("payload has no edges at all — the endpoint check is vacuous")
	}
}
