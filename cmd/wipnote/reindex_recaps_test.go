package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/recap"
	"github.com/shakestzd/wipnote/recap/recaptmpl"
)

// writeRecapArtifact renders a recap HTML artifact for the given data into
// <wipnoteDir>/recaps/<recapID>.html and returns the path.
func writeRecapArtifact(t *testing.T, wipnoteDir, recapID string, data recap.RecapData) string {
	t.Helper()
	recapsDir := filepath.Join(wipnoteDir, "recaps")
	if err := os.MkdirAll(recapsDir, 0o755); err != nil {
		t.Fatalf("mkdir recaps: %v", err)
	}
	page := recaptmpl.Build(data)
	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("render recap: %v", err)
	}
	path := filepath.Join(recapsDir, recapID+".html")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write recap: %v", err)
	}
	return path
}

func TestReindexRecaps(t *testing.T) {
	dir := t.TempDir()

	// Work-item recap (grounded).
	writeRecapArtifact(t, dir, "recap-feat-deadbeef", recap.RecapData{
		Outcome: "Add recap kind",
		Provenance: recap.Provenance{
			Kind:     recap.InputWorkItem,
			Input:    "feat-deadbeef",
			GitRange: "main..HEAD",
			Grounded: true,
		},
	})
	// Range recap (ungrounded).
	writeRecapArtifact(t, dir, "recap-r-0123456789ab", recap.RecapData{
		Outcome: "Changes in main..HEAD",
		Provenance: recap.Provenance{
			Kind:     recap.InputRange,
			Input:    "main..HEAD",
			GitRange: "main..HEAD",
			Grounded: false,
		},
	})

	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	total, upserted, errs := reindexRecaps(db, dir, dir, true)
	if total != 2 || upserted != 2 || errs != 0 {
		t.Fatalf("got (total=%d, upserted=%d, errs=%d), want (2,2,0)", total, upserted, errs)
	}

	// Work-item recap row.
	row, err := dbpkg.GetRecap(db, "recap-feat-deadbeef")
	if err != nil {
		t.Fatalf("get recap: %v", err)
	}
	if row == nil {
		t.Fatal("recap-feat-deadbeef missing from index")
	}
	if row.Kind != string(recap.InputWorkItem) {
		t.Errorf("kind = %q, want %q", row.Kind, recap.InputWorkItem)
	}
	if !row.Grounded {
		t.Error("expected grounded = true")
	}
	if row.WorkItemID != "feat-deadbeef" {
		t.Errorf("work_item_id = %q, want feat-deadbeef", row.WorkItemID)
	}
	if row.Outcome != "Add recap kind" {
		t.Errorf("outcome = %q", row.Outcome)
	}

	// Range recap has no work item.
	rangeRow, err := dbpkg.GetRecap(db, "recap-r-0123456789ab")
	if err != nil {
		t.Fatalf("get range recap: %v", err)
	}
	if rangeRow == nil {
		t.Fatal("range recap missing")
	}
	if rangeRow.WorkItemID != "" {
		t.Errorf("range recap work_item_id = %q, want empty", rangeRow.WorkItemID)
	}
	if rangeRow.Grounded {
		t.Error("range recap should be ungrounded")
	}

	// Listing returns both.
	all, err := dbpkg.ListRecaps(db)
	if err != nil {
		t.Fatalf("list recaps: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("list returned %d recaps, want 2", len(all))
	}
}

func TestReindexRecaps_PurgesStale(t *testing.T) {
	dir := t.TempDir()
	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Pre-seed a row that has no backing file on disk.
	if err := dbpkg.UpsertRecap(db, &dbpkg.RecapRow{ID: "recap-feat-ghost", Kind: "work-item"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Reindex with no recaps dir present should purge it.
	reindexRecaps(db, dir, dir, false)

	if row, _ := dbpkg.GetRecap(db, "recap-feat-ghost"); row != nil {
		t.Error("stale recap row was not purged")
	}
}

func TestInferNodeType_Recap(t *testing.T) {
	cases := map[string]string{
		"recap-feat-deadbeef":  "recap",
		"recap-r-0123456789ab": "recap",
		"recap-s-abc123":       "recap",
		"feat-deadbeef":        "feature",
		"sess-abc":             "session",
	}
	for id, want := range cases {
		if got := inferNodeTypeFromID(id); got != want {
			t.Errorf("inferNodeTypeFromID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestIsRecapHTMLPath(t *testing.T) {
	root := t.TempDir()
	wipnoteDir := filepath.Join(root, ".wipnote")
	if !isRecapHTMLPath(filepath.Join(wipnoteDir, "recaps", "recap-feat-x.html"), wipnoteDir) {
		t.Fatal("recap html path was not recognized")
	}
	if isRecapHTMLPath(filepath.Join(wipnoteDir, "features", "feat-x.html"), wipnoteDir) {
		t.Fatal("feature html path must not be treated as recap")
	}
	if isRecapHTMLPath(filepath.Join(root, "other", "recap-feat-x.html"), wipnoteDir) {
		t.Fatal("outside path must not be treated as recap")
	}
}
