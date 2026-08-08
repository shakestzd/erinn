package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestStartRecapsReindexLoop_PopulatesTableAtStartup verifies bug-95d2d493 fix:
// startRecapsReindexLoop triggers reindexRecaps at startup, so the recaps SQLite
// table is populated immediately after a container/DB restart without requiring a
// manual `wipnote reindex` call.
func TestStartRecapsReindexLoop_PopulatesTableAtStartup(t *testing.T) {
	dir := t.TempDir()
	writeRecapArtifact(t, dir, "recap-feat-startloop", recap.RecapData{
		Outcome: "loop startup test",
		Provenance: recap.Provenance{
			Kind:     recap.InputWorkItem,
			Input:    "feat-startloop",
			GitRange: "main..HEAD",
			Grounded: true,
		},
	})

	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Pin the pool to ONE permanent connection. Every new connection to
	// ":memory:" gets its own private, empty database, so without this the drain
	// goroutine's INSERT and this goroutine's polling SELECT can land on
	// different databases and the row is never observed — the test then burns
	// its whole 10s deadline and fails. That made it flaky at roughly 1-in-4
	// independent of any production change. Same remedy, and same reasoning, as
	// core/db/dbtest.OpenForTest, which documents this trap in detail.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run with a very long tick so only the startup run fires in this test.
	// startDrainLoop calls fn once before the first tick.
	done := make(chan struct{})
	go func() {
		startRecapsReindexLoop(ctx, db, dir)
		close(done)
	}()

	// Wait for startup reindex to land. startRecapsReindexLoop hands off to
	// startDrainLoop, which itself spawns the goroutine that actually runs
	// reindexRecaps — two goroutine hops from here. 2s was tight enough to
	// flake under the full test suite's parallel load (hundreds of tests,
	// many spawning git subprocesses, contending for OS threads/CPU); 10s
	// keeps this a bounded poll — it still returns as soon as the row
	// lands — while tolerating scheduler contention instead of assuming a
	// lightly-loaded machine.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := dbpkg.GetRecap(db, "recap-feat-startloop")
		if row != nil {
			return // pass
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("startRecapsReindexLoop did not populate the recaps table at startup")
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
