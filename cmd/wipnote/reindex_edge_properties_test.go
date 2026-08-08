package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// End-to-end edge-property round-trip (bug-eb141e88).
//
// The census fixtures hand-write their HTML, which pins the read half of the
// contract but would still pass if the writer emitted no property markup at
// all — the state this bug describes. These tests therefore go through the
// real writer: models.Edge → workitem.WriteNodeHTML → canonical HTML →
// reindex → SQLite, with the read index destroyed in between, and then assert
// the two live consumers still behave.

const (
	propsTrackID    = "trk-props-001"
	propsSourceID   = "feat-props-src0"
	propsPlanBlock  = "feat-props-blck"
	propsRealBlock  = "feat-props-real"
	propsDupTarget  = "bug-props-dup0"
	propsDupScore   = "0.842"
	propsDupTagName = "needs-triage-dup"
)

// buildEdgePropertyFixture writes a .wipnote/ tree using the production writer.
// Returns the project root.
func buildEdgePropertyFixture(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	for _, sub := range []string{"tracks", "features", "bugs"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	write := func(sub string, node *models.Node) {
		node.CreatedAt = now
		node.UpdatedAt = now
		if node.Status == "" {
			node.Status = models.StatusTodo
		}
		if node.Priority == "" {
			node.Priority = models.PriorityMedium
		}
		if _, err := workitem.WriteNodeHTML(filepath.Join(wipnoteDir, sub), node); err != nil {
			t.Fatalf("WriteNodeHTML %s: %v", node.ID, err)
		}
	}

	write("tracks", &models.Node{ID: propsTrackID, Title: "Property track", Type: "track"})
	write("features", &models.Node{ID: propsPlanBlock, Title: "Plan-order blocker", Type: "feature", TrackID: propsTrackID})
	write("features", &models.Node{ID: propsRealBlock, Title: "Asserted blocker", Type: "feature", TrackID: propsTrackID})
	write("bugs", &models.Node{ID: propsDupTarget, Title: "Suspected duplicate", Type: "bug", TrackID: propsTrackID})

	write("features", &models.Node{
		ID:      propsSourceID,
		Title:   "Property source",
		Type:    "feature",
		TrackID: propsTrackID,
		Edges: map[string][]models.Edge{
			"blocked_by": {
				{
					// Plan authoring order, not an asserted dependency:
					// graph.FindBottlenecks must keep excluding it after a
					// rebuild, which it can only do if origin survives.
					TargetID:     propsPlanBlock,
					Relationship: models.RelBlockedBy,
					Since:        now,
					Properties: map[string]string{
						"origin":        graph.EdgeOriginPlanSlice,
						"plan_id":       "plan-props-0001",
						"slice_num":     "2",
						"dep_slice_num": "1",
					},
				},
				{
					// A human `link add` assertion — no properties, and it
					// must still count as a bottleneck.
					TargetID:     propsRealBlock,
					Relationship: models.RelBlockedBy,
					Since:        now,
				},
			},
			"relates_to": {{
				TargetID:     propsDupTarget,
				Relationship: models.RelRelatesTo,
				Title:        propsDupTagName + ": " + propsDupTarget,
				Since:        now,
				Properties: map[string]string{
					"tag":              propsDupTagName,
					"similarity_score": propsDupScore,
				},
			}},
		},
	})

	return projectDir
}

// storedEdgeProps reads one edge's metadata out of the rebuilt read index.
func storedEdgeProps(t *testing.T, projectDir, from, rel, to string) map[string]string {
	t.Helper()
	db := openCachedDB(t, projectDir)
	defer db.Close()
	return edgeMetadata(t, db, from, to, rel)
}

// TestReindex_DedupEdgeKeepsSimilarityScoreAcrossRebuild is live case 1: the
// dedup heuristic's similarity_score and needs-triage-dup tag are the only
// confidence signal in the schema and `wipnote lineage` renders them. Before
// the writer emitted property markup they existed solely in SQLite and a
// rebuild erased them.
func TestReindex_DedupEdgeKeepsSimilarityScoreAcrossRebuild(t *testing.T) {
	projectDir := buildEdgePropertyFixture(t)
	setupReindexTestEnv(t, projectDir)

	want := map[string]string{"tag": propsDupTagName, "similarity_score": propsDupScore}

	runReindexInDir(t, projectDir)
	got := storedEdgeProps(t, projectDir, propsSourceID, "relates_to", propsDupTarget)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedup edge properties wrong after first index:\n got %#v\nwant %#v", got, want)
	}

	// Destroy the read index and rebuild from unchanged HTML.
	deleteCacheDB(t, projectDir)
	runReindexInDir(t, projectDir)

	got = storedEdgeProps(t, projectDir, propsSourceID, "relates_to", propsDupTarget)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedup edge properties lost on rebuild:\n got %#v\nwant %#v", got, want)
	}
}

// TestReindex_BottlenecksStayFilteredAcrossRebuild is live case 2: the origin
// stamp is what keeps a plan's foundational slice out of the bottleneck report
// (bug-d0489158). If origin does not round-trip through HTML, a rebuild
// silently un-filters the report.
func TestReindex_BottlenecksStayFilteredAcrossRebuild(t *testing.T) {
	projectDir := buildEdgePropertyFixture(t)
	setupReindexTestEnv(t, projectDir)

	runReindexInDir(t, projectDir)
	assertBottleneckFiltering(t, projectDir, "first index")

	deleteCacheDB(t, projectDir)
	runReindexInDir(t, projectDir)
	assertBottleneckFiltering(t, projectDir, "rebuild from unchanged HTML")

	props := storedEdgeProps(t, projectDir, propsSourceID, "blocked_by", propsPlanBlock)
	if props["origin"] != graph.EdgeOriginPlanSlice {
		t.Errorf("origin stamp lost on rebuild: got %#v", props)
	}
}

func assertBottleneckFiltering(t *testing.T, projectDir, stage string) {
	t.Helper()
	db := openCachedDB(t, projectDir)
	defer db.Close()

	results, err := graph.FindBottlenecks(db)
	if err != nil {
		t.Fatalf("%s: FindBottlenecks: %v", stage, err)
	}
	counts := map[string]int{}
	for _, r := range results {
		counts[r.ID] = r.BlockCount
	}
	if counts[propsRealBlock] != 1 {
		t.Errorf("%s: asserted blocker missing from bottlenecks: %#v", stage, counts)
	}
	if n, ok := counts[propsPlanBlock]; ok {
		t.Errorf("%s: plan-order blocker leaked into bottlenecks (count %d) — origin stamp was dropped",
			stage, n)
	}
}
