package projection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/claimledger"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/sessionledger"
	"github.com/shakestzd/wipnote/core/workitem"
)

func TestLoadBuildsCanonicalGraphProjection(t *testing.T) {
	wipnoteDir := newProject(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	track := mustCreateTrack(t, p, "Durable Projection")
	blocker := mustCreateFeature(t, p, "Blocker", "done", "")
	feature := mustCreateFeature(t, p, "Consumer", "todo", track.ID)
	_, err = p.Features.AddEdge(feature.ID, models.Edge{
		TargetID:     blocker.ID,
		Relationship: "blocked_by",
		Properties: map[string]string{
			"origin":     graph.EdgeOriginBatchApply,
			"confidence": "asserted",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Nodes[feature.ID].TrackID; got != track.ID {
		t.Fatalf("feature TrackID = %q, want %q", got, track.ID)
	}
	if !hasEdge(snap, feature.ID, track.ID, "part_of", "") {
		t.Fatalf("missing implicit part_of edge from feature to track")
	}
	if !hasEdge(snap, track.ID, feature.ID, "contains", "") {
		t.Fatalf("missing derived contains edge from track to feature")
	}
	if !hasEdge(snap, feature.ID, blocker.ID, "blocked_by", "asserted") {
		t.Fatalf("missing metadata-preserving blocked_by edge")
	}
	bottlenecks := snap.Bottlenecks()
	if len(bottlenecks) != 1 || bottlenecks[0].ID != blocker.ID {
		t.Fatalf("bottlenecks = %#v, want blocker %s", bottlenecks, blocker.ID)
	}
}

func TestExecuteDSLUsesCanonicalProjection(t *testing.T) {
	wipnoteDir := newProject(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	blocker := mustCreateFeature(t, p, "Finished Blocker", "done", "")
	feature := mustCreateFeature(t, p, "Todo Consumer", "todo", "")
	if _, err := p.Features.AddEdge(feature.ID, models.Edge{
		TargetID:     blocker.ID,
		Relationship: "blocked_by",
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := snap.ExecuteDSL("features[status=todo] -> blocked_by -> features[status=done]")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != blocker.ID {
		t.Fatalf("query returned %#v, want %s", got, blocker.ID)
	}
}

func TestExecuteDSLDerivesTrackContainsFromCanonicalTrackID(t *testing.T) {
	wipnoteDir := newProject(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	track := mustCreateTrack(t, p, "Query Track")
	feature := mustCreateFeature(t, p, "Tracked Query Feature", "todo", track.ID)
	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := snap.ExecuteDSL("tracks -> contains -> features[status=todo]")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != feature.ID {
		t.Fatalf("query returned %#v, want %s", got, feature.ID)
	}
}

func TestLoadAddsSessionAndClaimLedgerProjection(t *testing.T) {
	wipnoteDir := newProject(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	feature := mustCreateFeature(t, p, "Claimed Feature", "todo", "")
	start := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	sessionID := "sess-11111111-1111-1111-1111-111111111111"
	if _, err := sessionledger.NewStore(wipnoteDir).Open(sessionledger.Record{
		SessionID: sessionID,
		Harness:   "codex",
		StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	_, written, err := claimledger.NewStore(wipnoteDir).Open(sessionID, claimledger.Episode{
		WorkItemID:    feature.ID,
		SessionID:     sessionID,
		RootSessionID: sessionID,
		AgentID:       "__root__",
		StartedAt:     start,
	})
	if err != nil || !written {
		t.Fatalf("claim open written=%v err=%v", written, err)
	}
	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Nodes[sessionID]; got.Type != "session" || got.Agent != "codex" {
		t.Fatalf("session node = %#v", got)
	}
	sessions := snap.SessionsForFeature(feature.ID)
	if len(sessions) != 1 || sessions[0].SessionID != sessionID {
		t.Fatalf("sessions = %#v, want %s", sessions, sessionID)
	}
}

func TestLoadAddsPlanYAMLDerivedEdges(t *testing.T) {
	wipnoteDir := newProject(t)
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	planYAML := `
meta:
  id: plan-canonical
  title: Canonical Plan
  status: review
slices:
  - num: 1
    id: feat-foundation
  - num: 2
    id: feat-dependent
    deps: [1]
`
	if err := os.WriteFile(filepath.Join(wipnoteDir, "plans", "plan-canonical.yaml"), []byte(strings.TrimSpace(planYAML)), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Nodes["plan-canonical"]; got.Type != "plan" || got.Title != "Canonical Plan" {
		t.Fatalf("plan node = %#v", got)
	}
	if !hasEdge(snap, "feat-foundation", "plan-canonical", "planned_in", "") {
		t.Fatalf("missing YAML planned_in edge")
	}
	var dep Edge
	for _, e := range snap.Out["feat-dependent"] {
		if e.Relationship == "blocked_by" && e.ToID == "feat-foundation" {
			dep = e
		}
	}
	if dep.Metadata["origin"] != graph.EdgeOriginPlanSlice || dep.Metadata["dep_slice_num"] != "1" {
		t.Fatalf("blocked_by metadata = %#v", dep.Metadata)
	}
	bottlenecks := snap.Bottlenecks()
	if len(bottlenecks) != 0 {
		t.Fatalf("plan-slice dependency counted as bottleneck: %#v", bottlenecks)
	}
}

func TestLoadDerivesSessionImplementsFromClaims(t *testing.T) {
	wipnoteDir := newProject(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	feature := mustCreateFeature(t, p, "Claim-only Feature", "todo", "")
	sessionID := "sess-22222222-2222-2222-2222-222222222222"
	start := time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC)
	if _, err := sessionledger.NewStore(wipnoteDir).Open(sessionledger.Record{
		SessionID: sessionID,
		Harness:   "codex",
		StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = claimledger.NewStore(wipnoteDir).Open(sessionID, claimledger.Episode{
		WorkItemID:    feature.ID,
		SessionID:     sessionID,
		RootSessionID: sessionID,
		AgentID:       "__root__",
		StartedAt:     start,
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEdge(snap, feature.ID, sessionID, "implemented_in", "") {
		t.Fatalf("missing claim-derived implemented_in edge")
	}
	if !hasEdge(snap, sessionID, feature.ID, "implements", "") {
		t.Fatalf("missing reverse implements edge")
	}
}

// TestLoadTombstonesEdgeToPrunedSession is the regression for defect 1
// (feat-fc3cc9e0): graph.ClassifyEdgeTarget/MarkEdgeTombstoned are applied
// only by the SQL reindex pass (cmd/wipnote/reindex.go), never by
// projection.Load, so an implemented_in edge whose session target has since
// been pruned loses its tombstone marker and `wipnote lineage` renders it as
// a blank, unmarked neighbour instead of "(session pruned)" — exactly what
// core/graph/tombstone.go warns against (bug-10e166d8). The target here is
// session-shaped (graph.IsSessionShapedID) but was never registered with the
// session ledger, simulating a pruned session.
func TestLoadTombstonesEdgeToPrunedSession(t *testing.T) {
	wipnoteDir := newProject(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	feature := mustCreateFeature(t, p, "Implemented Somewhere", "done", "")
	prunedSession := "sess-44444444-4444-4444-4444-444444444444"
	if _, err := p.Features.AddEdge(feature.ID, models.Edge{
		TargetID:     prunedSession,
		Relationship: "implemented_in",
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	var found *Edge
	for i, e := range snap.Out[feature.ID] {
		if e.ToID == prunedSession {
			found = &snap.Out[feature.ID][i]
		}
	}
	if found == nil {
		t.Fatalf("expected implemented_in edge to a pruned session to survive as a tombstone, got Out=%#v", snap.Out[feature.ID])
	}
	if found.Metadata[graph.EdgeMetaTombstoned] != graph.EdgeTombstoneSession {
		t.Fatalf("edge metadata = %#v, want %s=%s", found.Metadata, graph.EdgeMetaTombstoned, graph.EdgeTombstoneSession)
	}
}

// TestLoadDropsEdgeToGenuinelyMissingTarget is the companion case to the
// tombstone test above: a declared edge whose target is NEITHER a resolvable
// node NOR session-shaped is a genuine dangling reference (something never
// created, or deleted outright) and must still be dropped, matching
// graph.ClassifyEdgeTarget's EdgeTargetDangling branch — the same policy the
// SQL reindex pass has always applied.
func TestLoadDropsEdgeToGenuinelyMissingTarget(t *testing.T) {
	wipnoteDir := newProject(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	feature := mustCreateFeature(t, p, "Points At Nothing", "todo", "")
	if _, err := p.Features.AddEdge(feature.ID, models.Edge{
		TargetID:     "feat-doesnotexist",
		Relationship: "blocked_by",
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Out[feature.ID] {
		if e.ToID == "feat-doesnotexist" {
			t.Fatalf("expected dangling (non-session-shaped, unresolved) edge to be dropped, found %#v", e)
		}
	}
}

// TestLoadDeduplicatesRepeatedEdgeDeclarations is the regression for defect 2
// (feat-fc3cc9e0): the SQL-era graph_edges table had edge_id TEXT PRIMARY KEY
// plus INSERT OR REPLACE, which silently collapsed duplicate (from, rel, to)
// declarations. models.Node.AddEdge (core/models/node.go) has always been an
// unconditional append — safe only because the primary key absorbed
// duplicates on read. Running the equivalent of `wipnote link add` twice (two
// AddEdge calls with an identical edge) now produces two literal anchors in
// canonical HTML, and without projection-side dedup that means
// Bottlenecks/Hubs double-count and the dashboard edge badge disagrees with
// the edge list.
func TestLoadDeduplicatesRepeatedEdgeDeclarations(t *testing.T) {
	wipnoteDir := newProject(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	blocker := mustCreateFeature(t, p, "Repeated Blocker", "done", "")
	feature := mustCreateFeature(t, p, "Repeated Consumer", "todo", "")
	edge := models.Edge{TargetID: blocker.ID, Relationship: "blocked_by"}
	if _, err := p.Features.AddEdge(feature.ID, edge); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Features.AddEdge(feature.ID, edge); err != nil {
		t.Fatal(err)
	}

	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snap.Out[feature.ID]); got != 1 {
		t.Fatalf("Out[%s] = %d edges, want 1 (deduplicated)", feature.ID, got)
	}
	bottlenecks := snap.Bottlenecks()
	if len(bottlenecks) != 1 || bottlenecks[0].BlockCount != 1 {
		t.Fatalf("bottlenecks = %#v, want single entry with BlockCount=1", bottlenecks)
	}
}

func newProject(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".wipnote")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustCreateFeature(t *testing.T, p *workitem.Project, title, status, trackID string) *models.Node {
	t.Helper()
	opts := []workitem.FeatureOption{workitem.FeatWithStatus(status)}
	if trackID != "" {
		opts = append(opts, workitem.FeatWithTrack(trackID))
	}
	n, err := p.Features.Create(title, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func mustCreateTrack(t *testing.T, p *workitem.Project, title string) *models.Node {
	t.Helper()
	n, err := p.Tracks.Create(title)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func hasEdge(snap *Snapshot, from, to, rel, confidence string) bool {
	for _, e := range snap.Out[from] {
		if e.ToID != to || e.Relationship != rel {
			continue
		}
		return confidence == "" || e.Metadata["confidence"] == confidence
	}
	return false
}
