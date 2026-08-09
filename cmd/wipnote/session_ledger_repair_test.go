package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/shakestzd/wipnote/core/sessionledger"
)

func runRepair(t *testing.T, wipnoteDir string, apply bool) string {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := runSessionLedgerRepair(cmd, wipnoteDir, apply); err != nil {
		t.Fatalf("repair: %v", err)
	}
	return out.String()
}

// TestRepairCorrectsAnMtimeDerivedEndEndToEnd is the scenario that forced this
// command to exist, reproduced from real inputs: a row whose end is the archive
// tarball's creation time, weeks after the session actually stopped, corrected
// from the session's own events.
func TestRepairCorrectsAnMtimeDerivedEndEndToEnd(t *testing.T) {
	projectDir := backfillTestProject(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")

	lastEvent := "2026-05-22T03:10:00Z"
	archiveMtime := time.Date(2026, 7, 8, 13, 23, 1, 0, time.UTC) // 47 days later
	writeBackfillArchive(t, wipnoteDir, backfillArchivedSession,
		"2026-05-22T02:33:36.413551295Z", lastEvent, archiveMtime)

	// Seed the damaged row exactly as the pre-provenance backfill wrote it:
	// the mtime as the end, and no record of where that end came from.
	store := sessionledger.NewStore(wipnoteDir)
	if _, err := store.Enrich(backfillArchivedSession, sessionledger.Enrichment{
		StartedAt: time.Date(2026, 5, 22, 2, 33, 36, 413551295, time.UTC),
		EndedAt:   archiveMtime,
	}); err != nil {
		t.Fatalf("seed damaged row: %v", err)
	}

	// Dry-run first: it must report the change and write nothing.
	out := runRepair(t, wipnoteDir, false)
	if !bytes.Contains([]byte(out), []byte(backfillArchivedSession)) {
		t.Errorf("dry-run did not name the damaged row:\n%s", out)
	}
	rec, _, _ := store.Get(backfillArchivedSession)
	if !rec.EndedAt.Equal(archiveMtime) {
		t.Fatalf("the dry-run wrote to the ledger: end is now %v", rec.EndedAt)
	}

	runRepair(t, wipnoteDir, true)

	rec, ok, err := store.Get(backfillArchivedSession)
	if err != nil || !ok {
		t.Fatalf("row missing after repair: ok=%v err=%v", ok, err)
	}
	want, _ := sessionledger.ParseTime(lastEvent)
	if !rec.EndedAt.Equal(want) {
		t.Errorf("end after repair: got %v, want the last event %v", rec.EndedAt, want)
	}
	if rec.EndSource != sessionledger.EndSourceLastActivity {
		t.Errorf("provenance after repair: got %q, want %q", rec.EndSource, sessionledger.EndSourceLastActivity)
	}
	if d := rec.EndedAt.Sub(rec.StartedAt); d > 24*time.Hour {
		t.Errorf("row still spans %v — the 47-day artifact survived repair", d)
	}
}

// TestRepairLeavesALiveCloseAlone is the safety property at the CLI level. A
// session that reported its own end must not be "corrected" by a weaker source
// just because repair was run.
func TestRepairLeavesALiveCloseAlone(t *testing.T) {
	projectDir := backfillTestProject(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")

	// Session HTML exists and would supply an end...
	writeBackfillSessionHTML(t, wipnoteDir, backfillHTMLSession,
		"2026-08-01T09:00:00Z", "2026-08-01T11:00:00Z", false)

	// ...but the session already reported its own close, at a different time.
	store := sessionledger.NewStore(wipnoteDir)
	start := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	if _, err := store.Open(sessionledger.Record{
		SessionID: backfillHTMLSession,
		Harness:   "claude-code",
		StartedAt: start,
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	liveEnd := start.Add(2*time.Hour + 30*time.Minute)
	if err := store.Close(backfillHTMLSession, liveEnd); err != nil {
		t.Fatalf("close: %v", err)
	}

	runRepair(t, wipnoteDir, true)

	rec, _, _ := store.Get(backfillHTMLSession)
	if !rec.EndedAt.Equal(liveEnd) {
		t.Errorf("repair moved a live close from %v to %v; SessionEnd is the most trustworthy "+
			"end there is and no later pass may overwrite it", liveEnd, rec.EndedAt)
	}
	if rec.EndSource != sessionledger.EndSourceLiveClose {
		t.Errorf("provenance downgraded to %q", rec.EndSource)
	}
}

func TestRepairIsIdempotent(t *testing.T) {
	projectDir := backfillTestProject(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	writeBackfillArchive(t, wipnoteDir, backfillArchivedSession,
		"2026-05-22T02:33:36.413551295Z", "2026-05-22T03:10:00Z",
		time.Date(2026, 7, 8, 13, 23, 1, 0, time.UTC))
	runBackfill(t, wipnoteDir, false)

	runRepair(t, wipnoteDir, true)
	store := sessionledger.NewStore(wipnoteDir)
	first, _ := store.ReadAll()

	out := runRepair(t, wipnoteDir, true)
	second, _ := store.ReadAll()

	if len(first) != len(second) {
		t.Fatalf("row count changed: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("a repeated repair changed a row:\n  first:  %+v\n  second: %+v", first[i], second[i])
		}
	}
	if !bytes.Contains([]byte(out), []byte("already backed by the best available source")) {
		t.Errorf("a second repair did not report itself as a no-op:\n%s", out)
	}
}

// TestBackfillNowRecordsProvenance guards the other half: repair can only work
// if the writers say where their ends came from.
func TestBackfillNowRecordsProvenance(t *testing.T) {
	projectDir := backfillTestProject(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	writeBackfillSessionHTML(t, wipnoteDir, backfillHTMLSession,
		"2026-08-01T09:00:00Z", "2026-08-01T11:00:00Z", false)
	writeBackfillArchive(t, wipnoteDir, backfillArchivedSession,
		"2026-05-22T02:33:36.413551295Z", "2026-05-22T03:10:00Z",
		time.Date(2026, 7, 8, 13, 23, 1, 0, time.UTC))

	runBackfill(t, wipnoteDir, false)

	recs, _ := sessionledger.NewStore(wipnoteDir).ReadAll()
	byID := map[string]sessionledger.Record{}
	for _, r := range recs {
		byID[r.SessionID] = r
	}
	if got := byID[backfillHTMLSession].EndSource; got != sessionledger.EndSourceSessionRecord {
		t.Errorf("HTML-sourced end provenance: got %q, want %q", got, sessionledger.EndSourceSessionRecord)
	}
	if got := byID[backfillArchivedSession].EndSource; got != sessionledger.EndSourceLastActivity {
		t.Errorf("archive-sourced end provenance: got %q, want %q", got, sessionledger.EndSourceLastActivity)
	}
	// And the archive row must carry the real last event, not the mtime.
	want, _ := sessionledger.ParseTime("2026-05-22T03:10:00Z")
	if !byID[backfillArchivedSession].EndedAt.Equal(want) {
		t.Errorf("archive-sourced end: got %v, want %v", byID[backfillArchivedSession].EndedAt, want)
	}
}
