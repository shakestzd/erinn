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

	handler := graphAPIHandler(database)

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

	handler := graphAPIHandler(database)
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

	handler := graphAPIHandler(database)
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
