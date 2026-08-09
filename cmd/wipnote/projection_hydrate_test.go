package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupColdCloneFixture creates a temp project with .wipnote/*.html containing
// known lineage edges but NO SQLite index (cold-clone scenario). It returns the
// .wipnote dir path and configures WIPNOTE_DB_PATH + git env vars so that the
// reindex helpers don't walk the real repo.
func setupColdCloneFixture(t *testing.T) (wipnoteDir string) {
	t.Helper()

	projectDir := t.TempDir()
	wipnoteDir = filepath.Join(projectDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Write a feature HTML with a lineage edge to a bug using the canonical
	// nav[data-graph-edges] format that htmlparse.ParseFile expects.
	featID := "feat-cold-0001"
	bugID := "bug-cold-0001"
	featHTML := fmt.Sprintf(`<!DOCTYPE html>
<html><body>
<article id="%s" data-type="feature" data-status="todo" data-priority="medium">
  <h1>Cold Clone Feature</h1>
  <nav data-graph-edges>
    <section data-edge-type="implements">
      <h3>Implements:</h3>
      <ul>
        <li><a href="%s.html" data-relationship="implements">Cold Clone Bug</a></li>
      </ul>
    </section>
  </nav>
</article>
</body></html>`, featID, bugID)
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", featID+".html"), []byte(featHTML), 0o644); err != nil {
		t.Fatalf("write feature html: %v", err)
	}

	bugHTML := fmt.Sprintf(`<!DOCTYPE html>
<html><body>
<article id="%s" data-type="bug" data-status="todo" data-priority="medium">
  <h1>Cold Clone Bug</h1>
</article>
</body></html>`, bugID)
	if err := os.WriteFile(filepath.Join(wipnoteDir, "bugs", bugID+".html"), []byte(bugHTML), 0o644); err != nil {
		t.Fatalf("write bug html: %v", err)
	}

	// Pin DB to a temp file (not yet created = cold-clone condition).
	dbPath := filepath.Join(t.TempDir(), "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	// Isolate from the real wipnote repo so git helpers fail fast.
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(projectDir))

	return wipnoteDir
}

// edgeCount returns the number of rows in graph_edges for the given db.
func edgeCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM graph_edges`).Scan(&n); err != nil {
		t.Fatalf("count graph_edges: %v", err)
	}
	return n
}

// featureCount returns the number of rows in features for the given db.
func featureCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM features`).Scan(&n); err != nil {
		t.Fatalf("count features: %v", err)
	}
	return n
}

// TestLazyReindex_ColdClone_LineageQueryReturnsEdges is the TDD regression for
// bug-4b07fd94. With a fresh clone (HTML has edges but no index), the lineage
// command must build the projection and return results instead of "no related
// nodes".
//
// The bug was fixed by rebuilding before the read. That rebuild is now
// unconditional — openReadOnlyDB goes through openDB, which hydrates the
// ephemeral projection from canonical HTML every time (feat-fc3cc9e0) — so the
// cold-clone case this guards is the only case there is. Its companion,
// TestLazyReindex_WarmIndex_NoRebuild, was deleted with the warmth check it
// drove: it asserted that a pre-populated persistent DB skipped the rebuild,
// and once no DB persists between processes it could only ever pass
// vacuously.
func TestLazyReindex_ColdClone_LineageQueryReturnsEdges(t *testing.T) {
	wipnoteDir := setupColdCloneFixture(t)

	// openReadOnlyDB must lazily populate the index for cold-clone scenario.
	db, err := openReadOnlyDB(wipnoteDir)
	if err != nil {
		t.Fatalf("openReadOnlyDB: %v", err)
	}
	defer db.Close()

	// After opening, the index must contain the feature row written to HTML.
	if n := featureCount(t, db); n < 1 {
		t.Errorf("features count after lazy reindex: got %d, want >= 1 (cold-clone HTML not indexed)", n)
	}

	// The index must also contain the lineage edges from the HTML.
	if n := edgeCount(t, db); n < 1 {
		t.Errorf("graph_edges count after lazy reindex: got %d, want >= 1 (missing/empty index)", n)
	}

	// And the lineage query must surface the edges present in the HTML.
	var buf bytes.Buffer
	opts := lineageOpts{depth: 5}
	if err := runLineage(&buf, db, "feat-cold-0001", opts); err != nil {
		t.Fatalf("runLineage: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "(no related nodes") {
		t.Errorf("cold-clone lineage returned 'no related nodes' — lazy reindex not triggered\noutput:\n%s", out)
	}
	if !strings.Contains(out, "bug-cold-0001") {
		t.Errorf("cold-clone lineage output missing 'bug-cold-0001'\noutput:\n%s", out)
	}
}
