package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/claimledger"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/projection"
	"github.com/shakestzd/wipnote/core/sessionledger"
	"github.com/shakestzd/wipnote/core/workitem"
)

func TestGraphAPI_TypesFilter(t *testing.T) {
	wipnoteDir := graphAPIFixture(t)
	handler := graphAPIHandler(nil, wipnoteDir)

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
	wipnoteDir := graphAPIFixture(t)
	handler := graphAPIHandler(nil, wipnoteDir)
	req := httptest.NewRequest("GET", "/api/graph?all=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	var data graphData
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	types := make(map[string]bool)
	for _, n := range data.Nodes {
		types[n.Type] = true
	}
	if !types["feature"] || !types["bug"] || !types["track"] || !types["session"] {
		t.Errorf("expected feature, bug, track, session types; got %v", types)
	}
}

func TestGraphAPI_PerTypeCaps(t *testing.T) {
	wipnoteDir := newGraphAPIWipnoteDir(t)
	store := sessionledger.NewStore(wipnoteDir)
	for i := 0; i < 5; i++ {
		id := "sess-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa" + string(rune('0'+i))
		if _, err := store.Open(sessionledger.Record{
			SessionID: id,
			Harness:   "claude",
			StartedAt: time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
	}

	handler := graphAPIHandler(nil, wipnoteDir)
	req := httptest.NewRequest("GET", "/api/graph?all=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	var data graphData
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Caps != nil {
		if ci, ok := data.Caps["session"]; ok && ci.Total != ci.Shown {
			t.Errorf("expected no truncation for 5 sessions, got total=%d shown=%d", ci.Total, ci.Shown)
		}
	}
}

// TestFilterByAgent_AssignedOnlySource exercises the INDIRECT claim-ledger
// branch of filterByAgentProjection (api_graph.go's loop over snap.Claims),
// not just the trivial n.Agent == agentName branch. The previous version of
// this test set the claiming session's own Harness to the exact filtered
// agent name, so the work item was already kept by the first (trivial) loop
// before the claim-ledger loop ever ran — and its negative assertion checked
// a literal "feat-other" string that workitem.GenerateID's random-hash IDs
// can never actually produce, so it always passed regardless of correctness.
// Both defects made this test pass for the wrong reason (feat-fc3cc9e0).
//
// This version claims a work item through a ROOT session that ran as the
// filtered agent while the actual executing (child) session ran as a
// DIFFERENT agent. addClaimImplementationEdges only wires an edge from the
// work item to the CHILD session, never to the root, so nothing but the
// claim-ledger loop can connect the root session — the only node whose Agent
// literally equals the filter — to either the child session or the claimed
// work item.
func TestFilterByAgent_AssignedOnlySource(t *testing.T) {
	wipnoteDir := newGraphAPIWipnoteDir(t)
	p, err := workitem.Open(wipnoteDir, "unrelated-creator")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := p.Features.Create("Indirectly Claimed", workitem.FeatWithStatus("todo"))
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := p.Features.Create("Unrelated", workitem.FeatWithStatus("todo"))
	if err != nil {
		t.Fatal(err)
	}

	rootSession := "sess-11111111-1111-1111-1111-111111111111"
	childSession := "sess-22222222-2222-2222-2222-222222222222"
	start := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)
	sessions := sessionledger.NewStore(wipnoteDir)
	if _, err := sessions.Open(sessionledger.Record{SessionID: rootSession, Harness: "graph-agent", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Open(sessionledger.Record{SessionID: childSession, Harness: "child-worker", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := claimledger.NewStore(wipnoteDir).Open(childSession, claimledger.Episode{
		WorkItemID: claimed.ID, SessionID: childSession, RootSessionID: rootSession,
		AgentID: "__root__", StartedAt: start.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := projection.Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := projectionGraphPayload(snap)
	filtered := filterByAgentProjection(snap, nodes, "graph-agent")

	kept := map[string]bool{}
	for _, n := range filtered {
		kept[n.ID] = true
	}
	if !kept[claimed.ID] {
		t.Errorf("expected work item %s to be kept via the indirect claim-ledger branch (root session ran as graph-agent)", claimed.ID)
	}
	if !kept[childSession] {
		t.Errorf("expected claiming (child) session %s to be kept", childSession)
	}
	if !kept[rootSession] {
		t.Errorf("expected root session %s to be kept", rootSession)
	}
	if kept[unrelated.ID] {
		t.Errorf("expected unrelated work item %s to be filtered out", unrelated.ID)
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

func TestGraphAPI_NoDanglingEdgeEndpoints(t *testing.T) {
	wipnoteDir := graphAPIFixture(t)
	for _, url := range []string{"/api/graph?all=true", "/api/graph"} {
		t.Run(url, func(t *testing.T) {
			handler := graphAPIHandler(nil, wipnoteDir)
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
				if !present[e.Source] || !present[e.Target] {
					t.Errorf("edge %s -%s-> %s names missing endpoint", e.Source, e.Type, e.Target)
				}
			}
		})
	}
	handler := graphAPIHandler(nil, wipnoteDir)
	req := httptest.NewRequest("GET", "/api/graph?all=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	var data graphData
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Edges) == 0 {
		t.Fatalf("payload has no edges at all — the endpoint check is vacuous")
	}
}

// TestGraphAPI_EdgeBadgeMatchesActualEdgeCount is the dispatcher-level
// regression for defect 2 (feat-fc3cc9e0): the SQL-era graph_edges table had
// edge_id TEXT PRIMARY KEY plus INSERT OR REPLACE, which silently collapsed a
// declaration repeated in canonical HTML (e.g. `wipnote link add` run twice)
// to one row. models.Node.AddEdge has always been an unconditional append —
// safe only because that primary key absorbed the duplicate on read. Without
// projection-side dedup, a real /api/graph response computed the node's edge
// badge as len(snap.Out)+len(snap.In) (api_graph.go's projectionGraphPayload)
// while the edges array went through a separate deduplicateEdges pass — an
// internally inconsistent payload where the badge says 2 but only 1 edge is
// drawn.
func TestGraphAPI_EdgeBadgeMatchesActualEdgeCount(t *testing.T) {
	wipnoteDir := newGraphAPIWipnoteDir(t)
	p, err := workitem.Open(wipnoteDir, "graph-agent")
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := p.Features.Create("Repeated Blocker", workitem.FeatWithStatus("done"))
	if err != nil {
		t.Fatal(err)
	}
	feature, err := p.Features.Create("Repeated Consumer", workitem.FeatWithStatus("todo"))
	if err != nil {
		t.Fatal(err)
	}
	edge := models.Edge{TargetID: blocker.ID, Relationship: "blocked_by"}
	if _, err := p.Features.AddEdge(feature.ID, edge); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Features.AddEdge(feature.ID, edge); err != nil {
		t.Fatal(err)
	}

	handler := graphAPIHandler(nil, wipnoteDir)
	req := httptest.NewRequest("GET", "/api/graph?all=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var data graphData
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var featureNode *graphNode
	for i, n := range data.Nodes {
		if n.ID == feature.ID {
			featureNode = &data.Nodes[i]
		}
	}
	if featureNode == nil {
		t.Fatalf("feature node %s missing from /api/graph response", feature.ID)
	}
	if featureNode.Edges != 1 {
		t.Errorf("node edge badge = %d, want 1 (deduplicated)", featureNode.Edges)
	}

	matching := 0
	for _, e := range data.Edges {
		if e.Source == feature.ID && e.Target == blocker.ID && e.Type == "blocked_by" {
			matching++
		}
	}
	if matching != 1 {
		t.Errorf("edges array has %d blocked_by entries for %s->%s, want 1", matching, feature.ID, blocker.ID)
	}
	if featureNode.Edges != matching {
		t.Errorf("edge badge (%d) disagrees with the drawn edge count (%d) — internally inconsistent payload", featureNode.Edges, matching)
	}
}

func graphAPIFixture(t *testing.T) string {
	t.Helper()
	wipnoteDir := newGraphAPIWipnoteDir(t)
	p, err := workitem.Open(wipnoteDir, "graph-agent")
	if err != nil {
		t.Fatal(err)
	}
	track, err := p.Tracks.Create("track 1")
	if err != nil {
		t.Fatal(err)
	}
	feature, err := p.Features.Create("feat 1", workitem.FeatWithStatus("done"), workitem.FeatWithTrack(track.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Bugs.Create("bug 1", workitem.BugWithStatus("done")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Features.Create("Other", workitem.FeatWithStatus("todo")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Features.AddEdge(feature.ID, models.Edge{TargetID: track.ID, Relationship: models.RelPartOf}); err != nil {
		t.Fatal(err)
	}
	addGraphAPISessionClaim(t, wipnoteDir, feature.ID)
	return wipnoteDir
}

func addGraphAPISessionClaim(t *testing.T, wipnoteDir, featureID string) {
	t.Helper()
	sessionID := "sess-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	start := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)
	if _, err := sessionledger.NewStore(wipnoteDir).Open(sessionledger.Record{
		SessionID: sessionID,
		Harness:   "graph-agent",
		StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err := claimledger.NewStore(wipnoteDir).Open(sessionID, claimledger.Episode{
		WorkItemID: featureID, SessionID: sessionID, RootSessionID: sessionID,
		AgentID: "__root__", StartedAt: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newGraphAPIWipnoteDir(t *testing.T) string {
	t.Helper()
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return wipnoteDir
}
