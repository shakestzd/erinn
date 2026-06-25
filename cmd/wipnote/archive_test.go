package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
)

// setupArchiveRepo creates a temp git repo with a .wipnote tree, points
// WIPNOTE_PROJECT_DIR at it, and returns (repoRoot, wipnoteDir).
func setupArchiveRepo(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()
	wipnoteDir := filepath.Join(repoRoot, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", repoRoot)

	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", repoRoot,
			"-c", "user.email=test@test.com", "-c", "user.name=Test",
			"-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	// Persist identity to the repo's LOCAL git config. The PRODUCTION archive
	// commit runs `git -C repo commit` WITHOUT the helper's inline `-c user.*`
	// flags, so it relies on ambient config. CI has no global git identity, so
	// without a persisted local identity the archive commit fails (exit 128),
	// leaves .wipnote dirty, and breaks the idempotency / incremental-ledger
	// tests. The inline -c flags above keep the test's own commits working.
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	// Seed an initial commit so the tree has a clean baseline.
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "-A")
	run("commit", "-m", "seed")
	return repoRoot, wipnoteDir
}

// writeDoneFeature writes a done feature file whose data-updated is `age` ago,
// optionally linking part_of a track.
func writeDoneFeature(t *testing.T, wipnoteDir, id, title string, age time.Duration, trackID string) {
	t.Helper()
	updated := time.Now().Add(-age).UTC().Format(time.RFC3339)
	created := time.Now().Add(-age - 24*time.Hour).UTC().Format(time.RFC3339)
	edge := ""
	if trackID != "" {
		edge = fmt.Sprintf(`<nav data-graph-edges><section data-edge-type="part_of"><ul><li><a href="../tracks/%s.html">%s</a></li></ul></section></nav>`, trackID, trackID)
	}
	content := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><title>%s</title></head>
<body>
  <article id="%s" data-type="feature" data-status="done" data-priority="medium" data-track-id="%s" data-created="%s" data-updated="%s" data-created-by-agent="test-agent">
    <header><h1>%s</h1></header>
    %s
    <section data-content><p>Done body.</p></section>
  </article>
</body></html>`, title, id, trackID, created, updated, title, edge)
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", id+".html"), []byte(content), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
}

func commitAll(t *testing.T, repoRoot string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "add fixtures"}} {
		full := append([]string{"-C", repoRoot, "-c", "user.email=test@test.com", "-c", "user.name=Test", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// TestArchive_DryRunDoesNotMutate verifies dry-run reports candidates but leaves
// files and ledger untouched.
func TestArchive_DryRunDoesNotMutate(t *testing.T) {
	_, wipnoteDir := setupArchiveRepo(t)
	writeDoneFeature(t, wipnoteDir, "feat-old00001", "Old feature", 60*24*time.Hour, "")
	featPath := filepath.Join(wipnoteDir, "features", "feat-old00001.html")

	if err := runArchive(false /*apply*/, defaultArchiveAgeDays); err != nil {
		t.Fatalf("runArchive dry-run: %v", err)
	}
	if !fileExists(t, featPath) {
		t.Errorf("dry-run removed the individual file")
	}
	if fileExists(t, graph.ArchiveLedgerPath(wipnoteDir, "features")) {
		t.Errorf("dry-run wrote a ledger")
	}
}

// TestArchive_ApplyRoundTrip is the core requirement: seed a done item → archive
// → reindex → still queryable in the features table AND its lineage edge survives.
func TestArchive_ApplyRoundTrip(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	writeMinimalTrackHTML(t, filepath.Join(wipnoteDir, "tracks"), "trk-host00001.html", "trk-host00001", "Host track")
	writeDoneFeature(t, wipnoteDir, "feat-old00001", "Old feature", 60*24*time.Hour, "trk-host00001")
	commitAll(t, repoRoot)

	featPath := filepath.Join(wipnoteDir, "features", "feat-old00001.html")
	if err := runArchive(true /*apply*/, defaultArchiveAgeDays); err != nil {
		t.Fatalf("runArchive apply: %v", err)
	}

	// Individual file moved out; ledger now holds the row.
	if fileExists(t, featPath) {
		t.Errorf("apply did not remove the individual file")
	}
	ledgerPath := graph.ArchiveLedgerPath(wipnoteDir, "features")
	entries, err := graph.ReadLedger(ledgerPath)
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "feat-old00001" {
		t.Fatalf("ledger should hold feat-old00001, got %+v", entries)
	}

	// Reindex from scratch and assert the archived item is queryable + edge intact.
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	validIDs := map[string]bool{}
	reindexTracks(database, wipnoteDir, repoRoot, validIDs, false)
	reindexFeatureDir(database, wipnoteDir, repoRoot, "features", validIDs, false)
	reindexWorkitemLedgerNodes(database, wipnoteDir, repoRoot, validIDs, false)
	purgeStaleEntries(database, validIDs)
	reindexEdges(database, wipnoteDir, validIDs)
	reindexWorkitemLedgerEdges(database, wipnoteDir, validIDs, false)

	var featCount int
	database.QueryRow(`SELECT COUNT(*) FROM features WHERE id = ?`, "feat-old00001").Scan(&featCount)
	if featCount != 1 {
		t.Errorf("archived feature not queryable after reindex (features count=%d)", featCount)
	}
	var edgeCount int
	database.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE from_node_id = ? AND to_node_id = ?`,
		"feat-old00001", "trk-host00001").Scan(&edgeCount)
	if edgeCount != 1 {
		t.Errorf("lineage edge feat-old00001 -> trk-host00001 lost after archive (count=%d)", edgeCount)
	}

	// show <id> must still resolve the archived item via the ledger fallback.
	node, archErr := resolveArchivedNode(wipnoteDir, "feat-old00001")
	if archErr != nil || node == nil {
		t.Fatalf("resolveArchivedNode: node=%v err=%v", node, archErr)
	}
	if node.Title != "Old feature" {
		t.Errorf("reconstructed node title: got %q", node.Title)
	}

	// CANONICAL-FIRST READ PATH (bypasses SQLite): `find all --title` loads via
	// graph.LoadAll, which must now include the archived item. This is the
	// coverage the team lead flagged — without LoadAll merging the ledger, the
	// archived item would silently vanish from find/analytics/status/etc.
	allNodes, lErr := loadFindNodes(wipnoteDir, "all")
	if lErr != nil {
		t.Fatalf("loadFindNodes all: %v", lErr)
	}
	matched := applyFindFilters(allNodes, findOpts{title: "Old feature"})
	foundInFind := false
	for _, n := range matched {
		if n.ID == "feat-old00001" {
			foundInFind = true
		}
	}
	if !foundInFind {
		t.Errorf("`find all --title` did not return the archived item — canonical-first read path lost it")
	}

	// `find <id>` (by-ID, canonical-first) must also resolve it.
	if rErr := runFindByID(wipnoteDir, "feat-old00001"); rErr != nil {
		t.Errorf("runFindByID for archived item failed: %v", rErr)
	}
}

// TestArchive_Idempotent verifies a second apply archives nothing new and the
// ledger is unchanged.
func TestArchive_Idempotent(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	writeDoneFeature(t, wipnoteDir, "feat-old00001", "Old feature", 60*24*time.Hour, "")
	commitAll(t, repoRoot)

	if err := runArchive(true, defaultArchiveAgeDays); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	ledgerPath := graph.ArchiveLedgerPath(wipnoteDir, "features")
	first, _ := os.ReadFile(ledgerPath)

	// Tree is clean again after the archive's own commit; second apply is a no-op.
	if err := runArchive(true, defaultArchiveAgeDays); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	second, _ := os.ReadFile(ledgerPath)
	if string(first) != string(second) {
		t.Errorf("ledger changed on idempotent re-run")
	}
}

// TestArchive_RefusesDirtyTree verifies apply refuses when the working tree has
// uncommitted changes.
func TestArchive_RefusesDirtyTree(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	writeDoneFeature(t, wipnoteDir, "feat-old00001", "Old feature", 60*24*time.Hour, "")
	// Intentionally do NOT commit — the new file makes the tree dirty.
	_ = repoRoot

	err := runArchive(true, defaultArchiveAgeDays)
	if err == nil {
		t.Fatalf("expected dirty-tree refusal, got nil")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("expected uncommitted-changes error, got %v", err)
	}
	// File must be untouched.
	if !fileExists(t, filepath.Join(wipnoteDir, "features", "feat-old00001.html")) {
		t.Errorf("dirty-tree refusal still removed the file")
	}
}

// TestArchive_SkipsRecentAndActive verifies recently-completed and non-done
// items are NOT archived.
func TestArchive_SkipsRecentAndActive(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	writeDoneFeature(t, wipnoteDir, "feat-recent001", "Recent done", 2*24*time.Hour, "")                               // too new
	writeMinimalFeatureHTML(t, filepath.Join(wipnoteDir, "features"), "feat-todo00001.html", "feat-todo00001", "Todo") // not done
	writeDoneFeature(t, wipnoteDir, "feat-old00001", "Old done", 60*24*time.Hour, "")                                  // eligible
	commitAll(t, repoRoot)

	if err := runArchive(true, defaultArchiveAgeDays); err != nil {
		t.Fatalf("apply: %v", err)
	}
	entries, err := graph.ReadLedger(graph.ArchiveLedgerPath(wipnoteDir, "features"))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "feat-old00001" {
		t.Fatalf("only feat-old00001 should be archived, got %+v", entries)
	}
	if !fileExists(t, filepath.Join(wipnoteDir, "features", "feat-recent001.html")) {
		t.Errorf("recently-completed item was wrongly archived")
	}
	if !fileExists(t, filepath.Join(wipnoteDir, "features", "feat-todo00001.html")) {
		t.Errorf("non-done item was wrongly archived")
	}
}
