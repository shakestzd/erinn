package lineage

import (
	"database/sql"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

func TestBFSWalk_Bidirectional(t *testing.T) {
	db, err := dbpkg.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// feat -> plan (part_of: feat is part of plan); feat -> bug (relates_to).
	mustEdge(t, db, "e1", "feat-1", "feature", "plan-1", "plan", "part_of")
	mustEdge(t, db, "e2", "feat-1", "feature", "bug-1", "bug", "relates_to")

	fwd, err := ForwardWalk(db, "feat-1", AllRels, 5)
	if err != nil {
		t.Fatalf("ForwardWalk: %v", err)
	}
	if len(fwd) != 2 {
		t.Fatalf("forward = %d nodes, want 2: %+v", len(fwd), fwd)
	}
	for _, n := range fwd {
		if n.Parent != "feat-1" || n.Depth != 1 {
			t.Errorf("node %+v: want parent feat-1 depth 1", n)
		}
	}

	back, err := BackwardWalk(db, "plan-1", AllRels, 5)
	if err != nil {
		t.Fatalf("BackwardWalk: %v", err)
	}
	if len(back) != 1 || back[0].ID != "feat-1" {
		t.Fatalf("backward = %+v, want [feat-1]", back)
	}
}

func mustEdge(t *testing.T, db *sql.DB, id, fromID, fromType, toID, toType, rel string) {
	t.Helper()
	if err := dbpkg.InsertEdge(db, id, fromID, fromType, toID, toType, rel, nil); err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}
}

// TestBFSWalk_CarriesMetadata is the regression for bug-b4458e51: the BFS walk
// used to select only (to_node_id, to_node_type, relationship_type), silently
// discarding graph_edges.metadata — the only place a dedup heuristic's
// similarity_score or a mechanical writer's origin tag lives. This asserts
// both directions carry metadata through, and that an edge with no metadata
// (the common, hand-asserted case) comes back with a nil map rather than an
// empty non-nil one or an error.
func TestBFSWalk_CarriesMetadata(t *testing.T) {
	db, err := dbpkg.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mustEdgeWithMeta(t, db, "e-dup", "feat-1", "feature", "feat-2", "feature", "relates_to",
		map[string]string{"tag": "needs-triage-dup", "similarity_score": "0.820"})
	mustEdgeWithMeta(t, db, "e-origin", "feat-1", "feature", "feat-3", "feature", "blocked_by",
		map[string]string{"origin": "plan_slice_deps"})
	mustEdge(t, db, "e-plain", "feat-1", "feature", "feat-4", "feature", "implements")

	fwd, err := ForwardWalk(db, "feat-1", AllRels, 5)
	if err != nil {
		t.Fatalf("ForwardWalk: %v", err)
	}
	byID := make(map[string]Node, len(fwd))
	for _, n := range fwd {
		byID[n.ID] = n
	}

	dup, ok := byID["feat-2"]
	if !ok {
		t.Fatalf("expected feat-2 in forward walk: %+v", fwd)
	}
	if dup.Metadata["tag"] != "needs-triage-dup" || dup.Metadata["similarity_score"] != "0.820" {
		t.Errorf("feat-2 metadata = %+v, want tag/similarity_score carried through", dup.Metadata)
	}

	origin, ok := byID["feat-3"]
	if !ok {
		t.Fatalf("expected feat-3 in forward walk: %+v", fwd)
	}
	if origin.Metadata["origin"] != "plan_slice_deps" {
		t.Errorf("feat-3 metadata = %+v, want origin=plan_slice_deps", origin.Metadata)
	}

	plain, ok := byID["feat-4"]
	if !ok {
		t.Fatalf("expected feat-4 in forward walk: %+v", fwd)
	}
	if plain.Metadata != nil {
		t.Errorf("feat-4 metadata = %+v, want nil (asserted edge carries no metadata)", plain.Metadata)
	}

	// Backward walk must carry the same metadata for the reverse direction.
	back, err := BackwardWalk(db, "feat-2", AllRels, 5)
	if err != nil {
		t.Fatalf("BackwardWalk: %v", err)
	}
	if len(back) != 1 || back[0].Metadata["tag"] != "needs-triage-dup" {
		t.Fatalf("backward walk from feat-2 = %+v, want tag=needs-triage-dup", back)
	}
}

func mustEdgeWithMeta(t *testing.T, db *sql.DB, id, fromID, fromType, toID, toType, rel string, meta map[string]string) {
	t.Helper()
	if err := dbpkg.InsertEdge(db, id, fromID, fromType, toID, toType, rel, meta); err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}
}
