package sessionledger

import (
	"testing"
	"time"
)

var (
	repairStart = time.Date(2026, 5, 22, 2, 33, 36, 0, time.UTC)
	repairTrue  = time.Date(2026, 5, 22, 4, 53, 25, 0, time.UTC) // last real activity
	repairMtime = time.Date(2026, 7, 8, 13, 23, 1, 0, time.UTC)  // when retention archived it
)

// seedWithEnd puts a row in the ledger carrying a specific end and provenance,
// bypassing the lifecycle so a test can start from any recorded state.
func seedWithEnd(t *testing.T, s *Store, id string, end time.Time, src EndSource) {
	t.Helper()
	if _, err := s.Enrich(id, Enrichment{
		StartedAt: repairStart,
		EndedAt:   end,
		EndSource: src,
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// TestEndSourceRankingIsTotalAndUnknownIsLowest pins the ordering the whole
// repair rule rests on. Unknown must rank lowest: a row that cannot say where
// its end came from is exactly the row repair should re-derive.
func TestEndSourceRankingIsTotalAndUnknownIsLowest(t *testing.T) {
	ordered := []EndSource{
		EndSourceUnknown,
		EndSourceArchiveMtime,
		EndSourceLastActivity,
		EndSourceSessionRecord,
		EndSourceLiveClose,
	}
	for i := 1; i < len(ordered); i++ {
		if !ordered[i].OutranksRecorded(ordered[i-1]) {
			t.Errorf("%q must outrank %q", ordered[i], ordered[i-1])
		}
		if ordered[i-1].OutranksRecorded(ordered[i]) {
			t.Errorf("%q must NOT outrank %q", ordered[i-1], ordered[i])
		}
	}
	// An unrecognised spelling must rank lowest, so a hand-edited or future
	// value can never silently outrank a known source.
	if EndSource("something-invented").OutranksRecorded(EndSourceArchiveMtime) {
		t.Error("an unrecognised source outranked archive-mtime")
	}
}

// TestCorrectReplacesAnArchiveMtimeEnd is the defect this whole path exists
// for: a row whose end is the tarball's creation time, corrected to the real
// last activity.
func TestCorrectReplacesAnArchiveMtimeEnd(t *testing.T) {
	s := testStore(t)
	seedWithEnd(t, s, sidA, repairMtime, EndSourceArchiveMtime)

	c, err := s.Correct(sidA, repairTrue, EndSourceLastActivity, false)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if !c.Applied {
		t.Fatalf("correction was not applied: %s", c.Reason)
	}
	rec, _, _ := s.Get(sidA)
	if !rec.EndedAt.Equal(repairTrue) {
		t.Errorf("end: got %v, want %v", rec.EndedAt, repairTrue)
	}
	if rec.EndSource != EndSourceLastActivity {
		t.Errorf("provenance not updated: got %q", rec.EndSource)
	}
	// The 47-day artifact is gone.
	if d := rec.EndedAt.Sub(rec.StartedAt); d > 24*time.Hour {
		t.Errorf("span is still %v — the mtime-derived end survived", d)
	}
}

// TestCorrectRefusesToMoveALiveClose is the safety property. SessionEnd's stamp
// is the most trustworthy end there is, and no later pass may overwrite it —
// including one whose value happens to look more precise.
func TestCorrectRefusesToMoveALiveClose(t *testing.T) {
	s := testStore(t)
	liveEnd := repairStart.Add(90 * time.Minute)
	seedWithEnd(t, s, sidA, liveEnd, EndSourceLiveClose)

	c, err := s.Correct(sidA, repairTrue, EndSourceLastActivity, false)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if c.Applied {
		t.Error("a live-close end was overwritten by a weaker source")
	}
	rec, _, _ := s.Get(sidA)
	if !rec.EndedAt.Equal(liveEnd) {
		t.Errorf("end moved to %v, want the live close %v", rec.EndedAt, liveEnd)
	}
	if c.Reason == "" {
		t.Error("a refused correction must say why it was refused")
	}
}

// TestCorrectReDerivesAnUnattributedEnd covers every row written before
// provenance existed — including the ones this feature's own first backfill
// produced.
func TestCorrectReDerivesAnUnattributedEnd(t *testing.T) {
	s := testStore(t)
	seedWithEnd(t, s, sidA, repairMtime, EndSourceUnknown)

	c, err := s.Correct(sidA, repairTrue, EndSourceLastActivity, false)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if !c.Applied {
		t.Fatalf("an unattributed end was not re-derived: %s", c.Reason)
	}
	rec, _, _ := s.Get(sidA)
	if !rec.EndedAt.Equal(repairTrue) {
		t.Errorf("end: got %v, want %v", rec.EndedAt, repairTrue)
	}
}

func TestCorrectIsIdempotent(t *testing.T) {
	s := testStore(t)
	seedWithEnd(t, s, sidA, repairMtime, EndSourceArchiveMtime)

	if _, err := s.Correct(sidA, repairTrue, EndSourceLastActivity, false); err != nil {
		t.Fatalf("first correct: %v", err)
	}
	c, err := s.Correct(sidA, repairTrue, EndSourceLastActivity, false)
	if err != nil {
		t.Fatalf("second correct: %v", err)
	}
	if c.Applied {
		t.Error("re-running the same correction rewrote the ledger")
	}
}

// TestCorrectDryRunWritesNothing is what makes repair's report-first default
// meaningful rather than decorative.
func TestCorrectDryRunWritesNothing(t *testing.T) {
	s := testStore(t)
	seedWithEnd(t, s, sidA, repairMtime, EndSourceArchiveMtime)

	c, err := s.Correct(sidA, repairTrue, EndSourceLastActivity, true)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if c.Applied {
		t.Error("a dry-run reported itself as applied")
	}
	if !c.Changed() {
		t.Error("a dry-run must still report the change it would make")
	}
	rec, _, _ := s.Get(sidA)
	if !rec.EndedAt.Equal(repairMtime) {
		t.Errorf("the dry-run wrote to the ledger: end is now %v", rec.EndedAt)
	}
}

func TestCorrectRefusesAnEndBeforeTheStart(t *testing.T) {
	s := testStore(t)
	seedWithEnd(t, s, sidA, repairMtime, EndSourceArchiveMtime)

	c, err := s.Correct(sidA, repairStart.Add(-time.Hour), EndSourceLiveClose, false)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if c.Applied {
		t.Error("recorded an end that precedes the start, which is corrupt whatever its provenance")
	}
}

func TestCorrectOfUnknownSessionReportsErrNoRow(t *testing.T) {
	s := testStore(t)
	if _, err := s.Correct(sidA, repairTrue, EndSourceLiveClose, false); err != ErrNoRow {
		t.Errorf("got %v, want ErrNoRow", err)
	}
}

// TestCloseRecordsLiveProvenance ties the lifecycle to the trust ordering: the
// end SessionEnd writes must be marked as the strongest source, or repair would
// happily overwrite real closes.
func TestCloseRecordsLiveProvenance(t *testing.T) {
	s := testStore(t)
	mustOpen(t, s, sidA, repairStart)
	if err := s.Close(sidA, repairStart.Add(time.Hour)); err != nil {
		t.Fatalf("close: %v", err)
	}
	rec, _, _ := s.Get(sidA)
	if rec.EndSource != EndSourceLiveClose {
		t.Errorf("Close recorded provenance %q, want %q — repair would overwrite real closes",
			rec.EndSource, EndSourceLiveClose)
	}
}

// TestProvenanceRoundTripsAndOldRowsStillParse guards the format change. Rows
// written before data-end-source existed must keep parsing, as unattributed.
func TestProvenanceRoundTripsAndOldRowsStillParse(t *testing.T) {
	s := testStore(t)
	seedWithEnd(t, s, sidA, repairTrue, EndSourceSessionRecord)
	recs, err := s.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != 1 || recs[0].EndSource != EndSourceSessionRecord {
		t.Fatalf("provenance did not round-trip: %+v", recs)
	}

	// A row with no data-end-source attribute — the pre-provenance format.
	legacy := `<!DOCTYPE html><html><body><table data-session-ledger="true"><tbody>` +
		`<tr id="` + sidB + `" data-session-id="` + sidB + `" data-start="` +
		FormatTime(repairStart) + `" data-end="` + FormatTime(repairMtime) + `"></tr>` +
		`</tbody></table></body></html>`
	parsed, err := parseLedgerString(legacy)
	if err != nil {
		t.Fatalf("parse legacy row: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("legacy row did not parse: got %d records", len(parsed))
	}
	if parsed[0].EndSource != EndSourceUnknown {
		t.Errorf("legacy row provenance: got %q, want unattributed", parsed[0].EndSource)
	}
	if !parsed[0].EndedAt.Equal(repairMtime) {
		t.Errorf("legacy row lost its end: %v", parsed[0].EndedAt)
	}
}
