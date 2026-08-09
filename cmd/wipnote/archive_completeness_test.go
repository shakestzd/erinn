package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
)

// archiveOneFeature seeds a done feature (optionally edged to a track), commits,
// then archives it (apply). Returns the repo root and wipnote dir with the item
// already compacted into the ledger and the move committed.
func archiveOneFeature(t *testing.T, featID, trackID string) (string, string) {
	t.Helper()
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	if trackID != "" {
		writeMinimalTrackHTML(t, filepath.Join(wipnoteDir, "tracks"), trackID+".html", trackID, "Host track")
	}
	writeDoneFeature(t, wipnoteDir, featID, "Archived feature", 60*24*time.Hour, trackID)
	commitAll(t, repoRoot)
	if err := runArchive(true /*apply*/, defaultArchiveAgeDays); err != nil {
		t.Fatalf("runArchive apply: %v", err)
	}
	return repoRoot, wipnoteDir
}

// F1: `show <id>` must resolve an archived item by exact AND partial ID, even
// though resolveID/ResolvePartialID only scan individual files.
func TestArchive_ShowResolvesArchivedByExactAndPartialID(t *testing.T) {
	_, wipnoteDir := archiveOneFeature(t, "feat-aabbccdd", "")

	// Exact full ID.
	if err := runWiShowWithFormat("feat-aabbccdd", "json"); err != nil {
		t.Errorf("show by exact archived ID failed: %v", err)
	}
	// Unambiguous partial (prefix) ID.
	if err := runWiShowWithFormat("feat-aabb", "json"); err != nil {
		t.Errorf("show by partial archived ID failed: %v", err)
	}
	_ = wipnoteDir
}

// F2: scoped `find features <q>` must include archived items, but `feature list`
// (runWiList) must STILL EXCLUDE them (intentional curated-active view).
func TestArchive_FindScopedIncludesButListExcludes(t *testing.T) {
	_, wipnoteDir := archiveOneFeature(t, "feat-aabbccdd", "")

	// find features <q> (scoped) → archive-aware via loadFindNodes.
	nodes, err := loadFindNodes(wipnoteDir, "features")
	if err != nil {
		t.Fatalf("loadFindNodes features: %v", err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == "feat-aabbccdd" {
			found = true
		}
	}
	if !found {
		t.Errorf("scoped `find features` did NOT include the archived item")
	}

	// feature list (runWiList) → graph.LoadDir only, must EXCLUDE archived.
	listNodes, err := graph.LoadDir(filepath.Join(wipnoteDir, "features"))
	if err != nil {
		t.Fatalf("LoadDir features: %v", err)
	}
	for _, n := range listNodes {
		if n.ID == "feat-aabbccdd" {
			t.Errorf("`feature list` (LoadDir) wrongly included the archived item — curated-active view broken")
		}
	}
}

// F3 (TestArchive_IncrementalDetectsLedgerChange) is gone. It asserted that an
// incremental reindex window containing an archive-ledger change fell back to
// the full, ledger-aware path — archiveLedgerChangedSince was that gate. The
// incremental path and its gate were both deleted with the persistent index
// they served (feat-fc3cc9e0); every rebuild is now full and ledger-aware by
// construction, which is what F4 below checks directly.

// F4: the projection rebuild must include archived items in the features table
// and preserve their lineage edges. openDB hydrates from canonical artifacts,
// which is the only rebuild there is now (feat-fc3cc9e0) — it replaced
// runFullSyncReindex, whose staleness check had no meaning once the projection
// stopped surviving between processes.
func TestArchive_LazyColdRebuildIncludesArchived(t *testing.T) {
	_, wipnoteDir := archiveOneFeature(t, "feat-aabbccdd", "trk-hostaaaa")

	database, err := openDB(wipnoteDir)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer database.Close()

	var featCount int
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-aabbccdd").Scan(&featCount)
	if featCount != 1 {
		t.Errorf("rebuild did not index the archived feature (count=%d)", featCount)
	}
	var edgeCount int
	database.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE from_node_id = ? AND to_node_id = ?`,
		"feat-aabbccdd", "trk-hostaaaa").Scan(&edgeCount)
	if edgeCount != 1 {
		t.Errorf("rebuild lost archived item's lineage edge (count=%d)", edgeCount)
	}
}

// F5: resolveNodeByUnionID must detect when a prefix matches BOTH a live and
// an archived item, returning ambiguity error instead of silently picking live.
func TestArchive_UnionIDReturnsAmbiguityForCrossSourcMatch(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)

	// Create and archive one feature.
	writeDoneFeature(t, wipnoteDir, "feat-aabbccdd", "Archived feature", 60*24*time.Hour, "")
	commitAll(t, repoRoot)
	if err := runArchive(true, defaultArchiveAgeDays); err != nil {
		t.Fatalf("runArchive apply: %v", err)
	}

	// Create a new live feature with the same prefix.
	writeMinimalFeatureHTML(t, filepath.Join(wipnoteDir, "features"), "feat-aabbeeee.html",
		"feat-aabbeeee", "Live feature with matching prefix")
	commitAll(t, repoRoot)

	// Now resolveNodeByUnionID with prefix "feat-aabb" should fail with ambiguity error.
	_, err := resolveNodeByUnionID(wipnoteDir, "feat-aabb")
	if err == nil {
		t.Errorf("resolveNodeByUnionID(feat-aabb) should fail with ambiguity, but succeeded")
	}
	if !strings.Contains(err.Error(), "ambiguous ID") {
		t.Errorf("resolveNodeByUnionID(feat-aabb) error should mention ambiguity; got: %v", err)
	}

	// Exact archived ID should still resolve.
	node, err := resolveNodeByUnionID(wipnoteDir, "feat-aabbccdd")
	if err != nil {
		t.Errorf("resolveNodeByUnionID(feat-aabbccdd) exact archived ID failed: %v", err)
	}
	if node != nil && node.ID != "feat-aabbccdd" {
		t.Errorf("resolved wrong node; want feat-aabbccdd, got %s", node.ID)
	}

	// Exact live ID should resolve.
	node, err = resolveNodeByUnionID(wipnoteDir, "feat-aabbeeee")
	if err != nil {
		t.Errorf("resolveNodeByUnionID(feat-aabbeeee) exact live ID failed: %v", err)
	}
	if node != nil && node.ID != "feat-aabbeeee" {
		t.Errorf("resolved wrong node; want feat-aabbeeee, got %s", node.ID)
	}

	// Unambiguous partial of the live feature should resolve.
	node, err = resolveNodeByUnionID(wipnoteDir, "feat-aabbee")
	if err != nil {
		t.Errorf("resolveNodeByUnionID(feat-aabbee) unambiguous partial failed: %v", err)
	}
	if node != nil && node.ID != "feat-aabbeeee" {
		t.Errorf("resolved wrong node; want feat-aabbeeee, got %s", node.ID)
	}
}

// F6: regression for roborev #538 — exact-match precedence must not be lost.
// An exact ID that is also a prefix of another ID must resolve to the exact item.
func TestArchive_UnionIDExactMatchPrecedence(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)

	// Create an archived item with a "short" exact ID.
	writeDoneFeature(t, wipnoteDir, "feat-aaaa", "Archived short ID", 60*24*time.Hour, "")
	commitAll(t, repoRoot)
	if err := runArchive(true, defaultArchiveAgeDays); err != nil {
		t.Fatalf("runArchive apply: %v", err)
	}

	// Create a live item whose ID starts with the archived ID's prefix.
	writeMinimalFeatureHTML(t, filepath.Join(wipnoteDir, "features"), "feat-aaaabbbb.html",
		"feat-aaaabbbb", "Live feature with longer ID")
	commitAll(t, repoRoot)

	// Exact match on the archived short ID should resolve to it, not error on ambiguity.
	node, err := resolveNodeByUnionID(wipnoteDir, "feat-aaaa")
	if err != nil {
		t.Errorf("resolveNodeByUnionID(feat-aaaa) exact archived ID should resolve, got: %v", err)
	}
	if node != nil && node.ID != "feat-aaaa" {
		t.Errorf("resolved wrong node; want feat-aaaa, got %s", node.ID)
	}

	// Exact match on the live long ID should also work.
	node, err = resolveNodeByUnionID(wipnoteDir, "feat-aaaabbbb")
	if err != nil {
		t.Errorf("resolveNodeByUnionID(feat-aaaabbbb) exact live ID should resolve, got: %v", err)
	}
	if node != nil && node.ID != "feat-aaaabbbb" {
		t.Errorf("resolved wrong node; want feat-aaaabbbb, got %s", node.ID)
	}

	// Prefix "feat-aaa" matches both, should be ambiguous.
	_, err = resolveNodeByUnionID(wipnoteDir, "feat-aaa")
	if err == nil {
		t.Errorf("resolveNodeByUnionID(feat-aaa) should fail with ambiguity, but succeeded")
	}
	if !strings.Contains(err.Error(), "ambiguous ID") {
		t.Errorf("resolveNodeByUnionID(feat-aaa) error should mention ambiguity; got: %v", err)
	}
}

// F7: regression for roborev #538 — error handling for scan failures.
// Corrupt/unreadable collection dirs must surface errors, not silently become no-match.
func TestArchive_UnionIDPropagatesScanErrors(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)

	// Create a live feature in the features directory.
	writeMinimalFeatureHTML(t, filepath.Join(wipnoteDir, "features"), "feat-abcd1234.html",
		"feat-abcd1234", "A feature")
	commitAll(t, repoRoot)

	// Make the bugs directory unreadable to trigger an error during prefix scan.
	bugsDir := filepath.Join(wipnoteDir, "bugs")
	os.MkdirAll(bugsDir, 0o755)
	os.Chmod(bugsDir, 0o000)
	defer os.Chmod(bugsDir, 0o755)

	// Exact match on an existing feature should still work (doesn't scan bugs).
	node, err := resolveNodeByUnionID(wipnoteDir, "feat-abcd1234")
	if err != nil {
		t.Errorf("resolveNodeByUnionID exact match should work: %v", err)
	}
	if node == nil || node.ID != "feat-abcd1234" {
		t.Errorf("expected exact match, got: %+v", node)
	}

	// Prefix match on a non-existent partial ID should fail when scanning,
	// exposing the permission error.
	_, err = resolveNodeByUnionID(wipnoteDir, "feat-xyz")
	if err == nil {
		t.Errorf("resolveNodeByUnionID should propagate scan error, but succeeded")
	}
	if !strings.Contains(err.Error(), "scan bugs") {
		t.Errorf("resolveNodeByUnionID error should mention scan failure; got: %v", err)
	}
}
