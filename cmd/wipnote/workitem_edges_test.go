package main

import (
	"os"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// TestAutoImplementedInEdge_NoStoredMirror is the regression test for
// bug-216c02c4. autoImplementedInEdge used to also write an independent
// session→item "implements" row straight to SQLite via a bare InsertEdge
// call, with no HTML backing anywhere — sessions have no Collection-backed
// AddEdge path, so that row could never be reconstructed by a reindex.
// Production observed exactly this: session|implements went 43 → 0 after a
// full reindex of otherwise-unchanged HTML.
//
// autoImplementedInEdge must now write ONLY the canonical item→session
// implemented_in edge (HTML + SQLite dual-write). "Session implements item"
// is derived on demand by SessionImplements, reversing implemented_in
// instead of reading a stored mirror — so there is nothing for a reindex to
// lose.
func TestAutoImplementedInEdge_NoStoredMirror(t *testing.T) {
	wipnoteDir := t.TempDir()
	featDir := filepath.Join(wipnoteDir, "features")
	if err := os.MkdirAll(featDir, 0o755); err != nil {
		t.Fatal(err)
	}

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	featID := "feat-mirror01"
	sessionID := "sess-mirror-test-0001"
	writeMinimalFeatureHTML(t, featDir, featID+".html", featID, "Mirror Test Feature")

	base := &workitem.Base{ProjectDir: wipnoteDir, Agent: "test-agent", DB: database}
	col := workitem.NewFeatureCollection(base).Collection

	autoImplementedInEdge(col, featID, sessionID)

	// 1. The canonical edge is present in HTML.
	node, err := htmlparse.ParseFile(filepath.Join(featDir, featID+".html"))
	if err != nil {
		t.Fatalf("parse feature: %v", err)
	}
	implEdges := node.Edges[string(models.RelImplementedIn)]
	if len(implEdges) != 1 || implEdges[0].TargetID != sessionID {
		t.Fatalf("implemented_in edge missing/wrong in HTML: %v", node.Edges)
	}

	// 2. The canonical edge is also present in SQLite (dual-write).
	var fwdCount int
	database.QueryRow(
		`SELECT COUNT(*) FROM graph_edges WHERE from_node_id = ? AND to_node_id = ? AND relationship_type = ?`,
		featID, sessionID, string(models.RelImplementedIn),
	).Scan(&fwdCount)
	if fwdCount != 1 {
		t.Errorf("expected 1 implemented_in row in graph_edges, got %d", fwdCount)
	}

	// 3. No independent reverse "implements" row was written — that row is
	// exactly what used to be silently wiped on reindex.
	var revCount int
	database.QueryRow(
		`SELECT COUNT(*) FROM graph_edges WHERE relationship_type = ?`,
		string(models.RelImplements),
	).Scan(&revCount)
	if revCount != 0 {
		t.Errorf("expected no stored implements mirror row, got %d", revCount)
	}

	// 4. The fact is still recoverable: SessionImplements derives it by
	// reversing the canonical implemented_in edge.
	got := SessionImplements(database, sessionID)
	if len(got) != 1 || got[0] != featID {
		t.Errorf("SessionImplements(%q) = %v, want [%q]", sessionID, got, featID)
	}
}

// TestSessionImplements_SurvivesReindex proves the derivation is reindex-safe:
// wipe graph_edges (simulating a full rebuild), reindex ONLY the feature's own
// HTML (where implemented_in canonically lives), and confirm SessionImplements
// still resolves the session's implemented item — with no session-side data
// involved at all. This is the exact scenario that lost 43 implements edges
// in production; the derived edge cannot be lost the same way because there
// is no independent row for a reindex to fail to reconstruct.
func TestSessionImplements_SurvivesReindex(t *testing.T) {
	wipnoteDir := t.TempDir()
	featDir := filepath.Join(wipnoteDir, "features")
	if err := os.MkdirAll(featDir, 0o755); err != nil {
		t.Fatal(err)
	}

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	featID := "feat-reidx0001"
	sessionID := "sess-reindex-test-0001"
	writeMinimalFeatureHTML(t, featDir, featID+".html", featID, "Reindex Test Feature")

	base := &workitem.Base{ProjectDir: wipnoteDir, Agent: "test-agent", DB: database}
	col := workitem.NewFeatureCollection(base).Collection
	autoImplementedInEdge(col, featID, sessionID)

	// Simulate a full reindex clearing every derived row.
	if _, err := database.Exec(`DELETE FROM graph_edges`); err != nil {
		t.Fatalf("clear graph_edges: %v", err)
	}
	if got := SessionImplements(database, sessionID); len(got) != 0 {
		t.Fatalf("expected empty derivation right after wipe, got %v", got)
	}

	// Rebuild graph_edges from HTML exactly as `wipnote reindex` does for
	// features — this is the read path that previously had nothing to
	// recover the reverse "implements" edge from.
	validIDs := map[string]bool{featID: true, sessionID: true}
	reindexEdges(database, wipnoteDir, validIDs)

	got := SessionImplements(database, sessionID)
	if len(got) != 1 || got[0] != featID {
		t.Errorf("after reindex, SessionImplements(%q) = %v, want [%q]", sessionID, got, featID)
	}
}
