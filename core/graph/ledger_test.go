package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sampleNodeHTML is a representative work-item file with edges, steps, and
// HTML-special characters in the body, to prove the ledger preserves it byte-
// for-byte through escape/unescape.
const sampleNodeHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Sample &amp; Special</title></head>
<body>
  <article id="feat-abc12345"
           data-type="feature"
           data-status="done"
           data-priority="high"
           data-track-id="trk-99999999"
           data-created="2026-01-01T10:00:00Z"
           data-updated="2026-02-01T10:00:00Z"
           data-created-by-agent="claude-code">
    <header><h1>Sample &lt;feature&gt; with &amp; chars</h1></header>
    <nav data-graph-edges>
      <section data-edge-type="part_of"><ul><li><a href="../tracks/trk-99999999.html">trk-99999999</a></li></ul></section>
    </nav>
    <section data-content><p>Body with &lt;tags&gt; &amp; an ampersand.</p></section>
  </article>
</body>
</html>`

func TestLedger_RoundTripPreservesHTMLAndFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "features.html")

	in := &LedgerEntry{
		ID:         "feat-abc12345",
		Type:       "feature",
		Title:      "Sample <feature> with & chars",
		Status:     "done",
		Priority:   "high",
		TrackID:    "trk-99999999",
		CreatedBy:  "claude-code",
		CreatedAt:  time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
		ArchivedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		HTML:       sampleNodeHTML,
	}

	if err := WriteLedger(path, []*LedgerEntry{in}); err != nil {
		t.Fatalf("WriteLedger: %v", err)
	}

	got, err := ReadLedger(path)
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	out := got[0]

	if out.HTML != sampleNodeHTML {
		t.Errorf("HTML payload not preserved verbatim.\n got: %q\nwant: %q", out.HTML, sampleNodeHTML)
	}
	if out.ID != in.ID || out.Type != in.Type || out.Status != in.Status || out.Priority != in.Priority {
		t.Errorf("scalar mismatch: got %+v", out)
	}
	if out.Title != in.Title {
		t.Errorf("title mismatch: got %q want %q", out.Title, in.Title)
	}
	if out.TrackID != in.TrackID || out.CreatedBy != in.CreatedBy {
		t.Errorf("track/createdBy mismatch: got track=%q by=%q", out.TrackID, out.CreatedBy)
	}
	if !out.CreatedAt.Equal(in.CreatedAt) || !out.UpdatedAt.Equal(in.UpdatedAt) || !out.ArchivedAt.Equal(in.ArchivedAt) {
		t.Errorf("timestamp mismatch: created=%v updated=%v archived=%v", out.CreatedAt, out.UpdatedAt, out.ArchivedAt)
	}
}

func TestLedger_NodeReconstructsExactly(t *testing.T) {
	e := &LedgerEntry{ID: "feat-abc12345", HTML: sampleNodeHTML}
	node, err := e.Node()
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if node.ID != "feat-abc12345" {
		t.Errorf("node ID: got %q", node.ID)
	}
	if string(node.Status) != "done" {
		t.Errorf("node status: got %q want done", node.Status)
	}
	if node.TrackID != "trk-99999999" {
		t.Errorf("node trackID: got %q", node.TrackID)
	}
	// Edge to the track must survive so lineage can traverse it.
	found := false
	for _, edges := range node.Edges {
		for _, ed := range edges {
			if ed.TargetID == "trk-99999999" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected part_of edge to trk-99999999, edges=%+v", node.Edges)
	}
}

func TestLedger_MissingFileIsNotExist(t *testing.T) {
	_, err := ReadLedger(filepath.Join(t.TempDir(), "nope.html"))
	if !os.IsNotExist(err) {
		t.Fatalf("expected os.IsNotExist error, got %v", err)
	}
}

func TestLedger_StableSortAndMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "features.html")

	mk := func(id string) *LedgerEntry {
		return &LedgerEntry{ID: id, Type: "feature", Status: "done", HTML: strings.Replace(sampleNodeHTML, "feat-abc12345", id, 1)}
	}
	if err := WriteLedger(path, []*LedgerEntry{mk("feat-bbb"), mk("feat-aaa")}); err != nil {
		t.Fatalf("WriteLedger: %v", err)
	}
	got, err := ReadLedger(path)
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(got) != 2 || got[0].ID != "feat-aaa" || got[1].ID != "feat-bbb" {
		t.Fatalf("expected sorted [feat-aaa feat-bbb], got %v", []string{got[0].ID, got[1].ID})
	}
}

// TestLoadAll_IncludesArchivedNodes is the central guarantee that archived items
// stay visible to every canonical-first reader (find/analytics/status/etc.):
// LoadAll must merge ledger rows with the individual files.
func TestLoadAll_IncludesArchivedNodes(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	// One live file.
	live := strings.Replace(sampleNodeHTML, "feat-abc12345", "feat-live00001", 1)
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", "feat-live00001.html"), []byte(live), 0o644); err != nil {
		t.Fatalf("write live: %v", err)
	}
	// One archived item in the ledger.
	arch := strings.Replace(sampleNodeHTML, "feat-abc12345", "feat-arch00001", 1)
	if err := WriteLedger(ArchiveLedgerPath(wipnoteDir, "features"),
		[]*LedgerEntry{{ID: "feat-arch00001", Type: "feature", Status: "done", HTML: arch}}); err != nil {
		t.Fatalf("WriteLedger: %v", err)
	}

	nodes, err := LoadAll(wipnoteDir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.ID] = true
	}
	if !ids["feat-live00001"] {
		t.Errorf("LoadAll missing the live file node")
	}
	if !ids["feat-arch00001"] {
		t.Errorf("LoadAll missing the ARCHIVED node — canonical-first readers would not see it")
	}
}

// TestLoadAll_LiveFileWinsOverStaleLedgerRow verifies de-dup: if an ID exists
// both as a live file and a (stale) ledger row, the live file is kept once.
func TestLoadAll_LiveFileWinsOverStaleLedgerRow(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	html := strings.Replace(sampleNodeHTML, "feat-abc12345", "feat-dup000001", 1)
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", "feat-dup000001.html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := WriteLedger(ArchiveLedgerPath(wipnoteDir, "features"),
		[]*LedgerEntry{{ID: "feat-dup000001", Type: "feature", Status: "done", HTML: html}}); err != nil {
		t.Fatalf("WriteLedger: %v", err)
	}

	nodes, err := LoadAll(wipnoteDir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	count := 0
	for _, n := range nodes {
		if n.ID == "feat-dup000001" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected feat-dup000001 exactly once (live wins), got %d", count)
	}
}
