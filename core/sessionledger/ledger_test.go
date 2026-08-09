package sessionledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), ".wipnote"))
}

const (
	sidA = "11112222-3333-4444-5555-666677778888"
	sidB = "99998888-7777-6666-5555-444433332222"
	sidC = "019f424e188c60f444c8eaca668b" // the 28-hex shape
)

func mustOpen(t *testing.T, s *Store, id string, started time.Time) {
	t.Helper()
	written, err := s.Open(Record{SessionID: id, Harness: "claude-code", ProjectDir: ".", StartedAt: started})
	if err != nil {
		t.Fatalf("open %s: %v", id, err)
	}
	if !written {
		t.Fatalf("open %s reported no write on a fresh ledger", id)
	}
}

func TestOpenThenReadRoundTrips(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 9, 10, 30, 0, 0, time.UTC)
	mustOpen(t, s, sidA, start)

	recs, err := s.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	got := recs[0]
	if got.SessionID != sidA || got.Harness != "claude-code" || got.ProjectDir != "." {
		t.Errorf("record round-trip lost fields: %+v", got)
	}
	if !got.StartedAt.Equal(start) {
		t.Errorf("start: got %v, want %v", got.StartedAt, start)
	}
	if !got.IsOpen() {
		t.Errorf("a session that has only started must read back as open, got end %v", got.EndedAt)
	}
}

// TestOpenIsIdempotent pins the property that keeps one session to one row.
// SessionStart fires again on every --resume and --continue of the same id; a
// second append would double-count the session everywhere the ledger is read.
func TestOpenIsIdempotent(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 9, 10, 30, 0, 0, time.UTC)
	mustOpen(t, s, sidA, start)

	written, err := s.Open(Record{SessionID: sidA, Harness: "claude-code", StartedAt: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if written {
		t.Errorf("a second Open for %s wrote another row — a resumed session would appear twice", sidA)
	}
	recs, _ := s.ReadAll()
	if len(recs) != 1 {
		t.Fatalf("got %d rows after a repeated open, want 1", len(recs))
	}
	if !recs[0].StartedAt.Equal(start) {
		t.Errorf("the repeated open moved the start time to %v; the first start is the true one",
			recs[0].StartedAt)
	}
}

func TestCloseStampsEndOnce(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	end := start.Add(90 * time.Minute)
	mustOpen(t, s, sidA, start)

	if err := s.Close(sidA, end); err != nil {
		t.Fatalf("close: %v", err)
	}
	rec, ok, _ := s.Get(sidA)
	if !ok {
		t.Fatal("record vanished after close")
	}
	if rec.IsOpen() || !rec.EndedAt.Equal(end) {
		t.Fatalf("end: got %v (open=%v), want %v", rec.EndedAt, rec.IsOpen(), end)
	}

	// A later reconcile pass must not move an end that was already recorded.
	if err := s.Close(sidA, end.Add(time.Hour)); err != nil {
		t.Fatalf("second close: %v", err)
	}
	rec, _, _ = s.Get(sidA)
	if !rec.EndedAt.Equal(end) {
		t.Errorf("a second close moved the end to %v; the first recorded end is the true one", rec.EndedAt)
	}
}

func TestCloseUnknownSessionReportsErrNoRow(t *testing.T) {
	s := testStore(t)
	if err := s.Close(sidA, time.Now()); err != ErrNoRow {
		t.Errorf("close of an unrecorded session: got %v, want ErrNoRow", err)
	}
}

// TestEnrichCreatesRowForArchivedSession is the backfill contract: a session
// that ran before the ledger existed has no start row, but its archive proves
// it ran. Writing the row is what turns that proof into a resolvable target.
func TestEnrichCreatesRowForArchivedSession(t *testing.T) {
	s := testStore(t)
	end := time.Date(2026, 5, 24, 18, 34, 46, 0, time.UTC)

	changed, err := s.Enrich(sidB, Enrichment{
		EndedAt:     end,
		ArchivePath: ".wipnote/archive/2026-05/" + sidB + ".tar.gz",
	})
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if !changed {
		t.Fatal("enrich of an unrecorded session reported no change")
	}

	rec, ok, _ := s.Get(sidB)
	if !ok {
		t.Fatal("enrich did not create the row")
	}
	if !rec.EndedAt.Equal(end) {
		t.Errorf("end: got %v, want %v", rec.EndedAt, end)
	}
	// With no start evidence the row falls back to the end time rather than
	// inventing one — a fabricated start would misreport the interval to every
	// consumer that joins on it.
	if !rec.StartedAt.Equal(end) {
		t.Errorf("start with no start evidence: got %v, want the end time %v", rec.StartedAt, end)
	}
	if rec.ArchivePath == "" {
		t.Error("archive path was not recorded")
	}
}

func TestEnrichNeverDowngradesAKnownField(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	mustOpen(t, s, sidA, start)
	if err := s.Close(sidA, end); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A later archive pass knows only the tarball mtime, which is LATER than the
	// end SessionEnd recorded. It must not move the end.
	if _, err := s.Enrich(sidA, Enrichment{
		Harness:     "codex",
		EndedAt:     end.Add(6 * time.Hour),
		ArchivePath: "a/b.tar.gz",
	}); err != nil {
		t.Fatalf("enrich: %v", err)
	}

	rec, _, _ := s.Get(sidA)
	if !rec.EndedAt.Equal(end) {
		t.Errorf("end moved to %v; the archive mtime must not override the recorded end %v",
			rec.EndedAt, end)
	}
	if rec.Harness != "claude-code" {
		t.Errorf("harness overwritten to %q; enrichment fills gaps, it does not correct", rec.Harness)
	}
	if rec.ArchivePath != "a/b.tar.gz" {
		t.Errorf("archive path (a genuine gap) was not filled: %q", rec.ArchivePath)
	}
}

func TestEnrichIsIdempotent(t *testing.T) {
	s := testStore(t)
	e := Enrichment{EndedAt: time.Date(2026, 5, 24, 18, 0, 0, 0, time.UTC), ArchivePath: "x.tar.gz"}
	if _, err := s.Enrich(sidB, e); err != nil {
		t.Fatalf("first enrich: %v", err)
	}
	changed, err := s.Enrich(sidB, e)
	if err != nil {
		t.Fatalf("second enrich: %v", err)
	}
	if changed {
		t.Error("re-running the same enrichment rewrote the ledger; backfill must be safe to repeat")
	}
}

// TestOpenRefusesNonSessionShapedID is the boundary that keeps the ledger from
// becoming a way to make an arbitrary token a valid edge target. It uses the
// same predicate the target-validity gate applies.
func TestOpenRefusesNonSessionShapedID(t *testing.T) {
	s := testStore(t)
	for _, id := range []string{"feat-deadbeef", "c4efb206", "", "not-an-id"} {
		if _, err := s.Open(Record{SessionID: id, StartedAt: time.Now()}); err == nil {
			t.Errorf("Open accepted %q — the gate would then treat it as a live session target", id)
		}
	}
}

func TestOpenAcceptsBothSessionIDShapes(t *testing.T) {
	s := testStore(t)
	now := time.Now().UTC()
	mustOpen(t, s, sidA, now)
	mustOpen(t, s, sidC, now)
	recs, _ := s.ReadAll()
	if len(recs) != 2 {
		t.Fatalf("got %d rows, want both the dashed UUID and the 28-hex shape", len(recs))
	}
}

// TestTornWriteIsRepairedWithoutSwallowingTheNextRow is why appendRowLocked
// does not simply seek to the end. A crash mid-append leaves a tail that is
// neither a row nor the footer; appending after it merges the new row INTO the
// fragment and goquery parses the pair as one garbage element, losing both.
func TestTornWriteIsRepairedWithoutSwallowingTheNextRow(t *testing.T) {
	s := testStore(t)
	mustOpen(t, s, sidA, time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC))

	// Simulate the crash: truncate the footer and half of a second row away.
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	torn := strings.TrimSuffix(string(data), ledgerFooter) + `<tr data-session-i`
	if err := os.WriteFile(s.Path(), []byte(torn), 0o644); err != nil {
		t.Fatalf("write torn ledger: %v", err)
	}

	mustOpen(t, s, sidB, time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC))

	recs, err := s.ReadAll()
	if err != nil {
		t.Fatalf("read after repair: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d rows after a torn write, want 2 (the survivor plus the new one): %+v", len(recs), recs)
	}
	if recs[0].SessionID != sidA || recs[1].SessionID != sidB {
		t.Errorf("rows after repair: got %s, %s", recs[0].SessionID, recs[1].SessionID)
	}
}

func TestReadAllOfMissingLedgerIsEmptyNotAnError(t *testing.T) {
	s := testStore(t)
	recs, err := s.ReadAll()
	if err != nil {
		t.Fatalf("reading a ledger that does not exist yet: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("got %d records from a nonexistent ledger", len(recs))
	}
}

// TestFieldsCannotBreakTheOneRowOneLineInvariant guards the property the
// torn-write repair depends on: the repair truncates to the last newline and
// calls that a row boundary, which is only true if no field can contain one.
func TestFieldsCannotBreakTheOneRowOneLineInvariant(t *testing.T) {
	s := testStore(t)
	if _, err := s.Open(Record{
		SessionID:  sidA,
		Harness:    "claude\ncode",
		ProjectDir: "a\r\nb",
		StartedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := strings.TrimPrefix(strings.TrimSuffix(string(data), ledgerFooter), ledgerHeader)
	if strings.Count(body, "\n") != 1 {
		t.Errorf("one record produced %d lines; a newline in a field can swallow the rows after it",
			strings.Count(body, "\n"))
	}
}

// TestLabelIsNeverBlank pins the renderability contract at its source. The
// projection writes Label() into sessions.title, and a blank there is exactly
// the failure the ledger was built to avoid: a node that resolves as live,
// carries no tombstone marker, and renders as nothing.
func TestLabelIsNeverBlank(t *testing.T) {
	cases := []Record{
		{SessionID: sidA, Harness: "claude-code", StartedAt: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)},
		{SessionID: sidA, StartedAt: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)},
		{SessionID: sidA, EndedAt: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)},
		{SessionID: sidA},
	}
	for _, r := range cases {
		label := strings.TrimSpace(r.Label())
		if label == "" {
			t.Errorf("Label() is blank for %+v", r)
		}
		if label == r.SessionID {
			t.Errorf("Label() is just the raw id for %+v — nothing a reader can read", r)
		}
	}
}

func TestOnCommitFiresWithARepoRelativePath(t *testing.T) {
	s := testStore(t)
	var gotRel string
	OnCommit = func(_, relPath, _ string) { gotRel = relPath }
	t.Cleanup(func() { OnCommit = nil })

	mustOpen(t, s, sidA, time.Now().UTC())

	want := ".wipnote/" + FileName
	if gotRel != want {
		t.Errorf("commit path: got %q, want %q — an absolute host path must never reach a commit message",
			gotRel, want)
	}
}
