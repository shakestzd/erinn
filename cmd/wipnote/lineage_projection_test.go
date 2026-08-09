package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// This file covers the DISPATCHER path for canonical-kind lineage queries —
// runLineageProjection, the code a real `wipnote lineage feat-XXX` invocation
// actually runs (see newLineageCmd's isCanonicalLineageKind branch). Every
// existing test in lineage_test.go instead seeds a raw SQL graph_edges table
// and calls runLineage(db, ...) directly, which canonical-kind lineage no
// longer executes in production. Exercising only the legacy SQL walker is
// exactly how the projection's missing tombstone classification and edge
// dedup (core/projection/snapshot.go) stayed hidden (feat-fc3cc9e0, defects 1
// and 2). These tests seed a real .wipnote canonical project via
// workitem.Open — the same artifact `wipnote lineage` reads in production —
// and dispatch through runLineageProjection.

// TestLineageProjectionRendersTombstonedSession is the dispatcher-level
// regression for defect 1. Before core/projection/snapshot.go derived the
// tombstone classification (graph.ClassifyEdgeTarget), a real `wipnote
// lineage feat-XXX` invocation on a feature with an implemented_in edge to a
// pruned session rendered a blank, unmarked neighbour instead of the
// "(session pruned)" caveat core/graph/tombstone.go exists to preserve
// (bug-10e166d8).
func TestLineageProjectionRendersTombstonedSession(t *testing.T) {
	wipnoteDir := newLineageProjectionWipnoteDir(t)
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	feature, err := p.Features.Create("Implemented Somewhere", workitem.FeatWithStatus("done"))
	if err != nil {
		t.Fatal(err)
	}
	prunedSession := "sess-33333333-3333-3333-3333-333333333333"
	if _, err := p.Features.AddEdge(feature.ID, models.Edge{
		TargetID:     prunedSession,
		Relationship: models.RelImplementedIn,
	}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runLineageProjection(&buf, wipnoteDir, feature.ID, lineageOpts{depth: 5}); err != nil {
		t.Fatalf("runLineageProjection: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, prunedSession) {
		t.Fatalf("expected pruned session %s to appear in lineage output, got:\n%s", prunedSession, out)
	}
	if !strings.Contains(out, "(session pruned)") {
		t.Errorf("expected the tombstone caveat '(session pruned)' in real `wipnote lineage` output, got:\n%s", out)
	}
}

// Note on defect 2 (edge dedup): a `wipnote lineage` BFS walk (see
// internal/lineage.ProjectionWalk) already collapses a repeated declaration
// to one hop via its own visited-ID set, independent of whether the
// projection itself deduplicated the underlying edge — so lineage tree
// output cannot distinguish a fixed Snapshot from a broken one. The
// dispatcher-level regression for defect 2 lives in
// api_graph_filter_test.go instead (TestGraphAPI_EdgeBadgeMatchesActualEdgeCount),
// exercised through /api/graph — the surface defect 2 named directly
// (api_graph.go's node edge-count badge computed from snap.Out/snap.In).

func newLineageProjectionWipnoteDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".wipnote")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
