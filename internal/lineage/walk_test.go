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
