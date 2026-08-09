package gateledger_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/gateledger"
)

func newStore(t *testing.T) *gateledger.Store {
	t.Helper()
	return gateledger.NewStore(filepath.Join(t.TempDir(), ".wipnote"))
}

func passing(session, workItem string, at time.Time) gateledger.Record {
	return gateledger.Record{
		SessionID:   session,
		WorkItemID:  workItem,
		Harness:     "claude-code",
		ProjectType: "go",
		GateCommand: "go build ./... && go vet ./... && go test -short ./...",
		Status:      gateledger.StatusPass,
		CheckedAt:   at,
		Source:      "check",
	}
}

// TestAppendRoundTrip is the format contract: everything written comes back.
//
// The two fields that carry the most risk are asserted explicitly. CheckedAt
// must survive at NANOSECOND precision, because the signature payload formats it
// with RFC3339Nano — a lossy on-disk format would silently invalidate every
// record's checksum. And the allowlist hit DETAIL must round-trip, not just its
// count: the detail is the reason a failing gate command was forgiven.
func TestAppendRoundTrip(t *testing.T) {
	store := newStore(t)
	at := time.Date(2026, 8, 9, 12, 34, 56, 123456789, time.UTC)

	in := passing("sess-round-trip", "feat-round-trip", at)
	in.OutputSummary = "go test allowlisted"
	in.ProfileSignature = "sha256:abc123"
	in.GuardsRunJSON = `["build","vet","test"]`
	in.AllowlistHitsJSON = `[{"id":"listener-socket-sandbox","command":"go test","justification":"sandbox forbids listener binds"}]`
	in.AllowlistHitCount = 1

	written, err := store.Append(in)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !strings.HasPrefix(written.ID, "gr-") {
		t.Fatalf("minted id = %q, want a gr- prefix", written.ID)
	}

	records, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("read %d records, want 1", len(records))
	}
	got := records[0]

	if !got.CheckedAt.Equal(at) {
		t.Fatalf("checked-at = %s, want %s (nanosecond precision must survive)", got.CheckedAt, at)
	}
	if got.AllowlistHitCount != 1 {
		t.Fatalf("allowlist hit count = %d, want 1", got.AllowlistHitCount)
	}
	if !strings.Contains(got.AllowlistHitsJSON, "listener-socket-sandbox") {
		t.Fatalf("allowlist detail lost: %q", got.AllowlistHitsJSON)
	}
	if !strings.Contains(got.AllowlistHitsJSON, "sandbox forbids listener binds") {
		t.Fatalf("allowlist justification lost: %q", got.AllowlistHitsJSON)
	}
	if got.GateCommand != in.GateCommand {
		t.Fatalf("gate command = %q, want %q", got.GateCommand, in.GateCommand)
	}
	if got.ProfileSignature != in.ProfileSignature {
		t.Fatalf("profile signature = %q, want %q", got.ProfileSignature, in.ProfileSignature)
	}
	if got.GuardsRunJSON != in.GuardsRunJSON {
		t.Fatalf("guards run = %q, want %q", got.GuardsRunJSON, in.GuardsRunJSON)
	}
	if got.OutputSummary != in.OutputSummary {
		t.Fatalf("output summary = %q, want %q", got.OutputSummary, in.OutputSummary)
	}
	if !got.SignatureValid() {
		t.Fatal("signature did not re-verify after the round trip")
	}
}

// TestAppendIsAlwaysANewRecord pins the property that makes this the simplest of
// the three ledgers: a gate run is immutable, so two runs of the same commands in
// the same session are two rows, never a revision of one.
func TestAppendIsAlwaysANewRecord(t *testing.T) {
	store := newStore(t)
	at := time.Now().UTC()

	first, err := store.Append(passing("sess-twice", "feat-twice", at))
	if err != nil {
		t.Fatalf("first Append: %v", err)
	}
	second, err := store.Append(passing("sess-twice", "feat-twice", at.Add(time.Second)))
	if err != nil {
		t.Fatalf("second Append: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("two gate runs shared a record id")
	}

	records, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("read %d records, want 2", len(records))
	}
}

// TestLatestForSession_PicksTheNewest also covers the negative: a record from
// another session must not satisfy a session-scoped lookup.
func TestLatestForSession_PicksTheNewest(t *testing.T) {
	store := newStore(t)
	base := time.Now().UTC().Add(-time.Hour)

	if _, err := store.Append(passing("sess-a", "feat-x", base)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	newest := passing("sess-a", "feat-x", base.Add(30*time.Minute))
	newest.Status = gateledger.StatusFail
	newest.OutputSummary = "go vet failed"
	if _, err := store.Append(newest); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Append(passing("sess-b", "feat-x", base.Add(45*time.Minute))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := store.LatestForSession("sess-a")
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if got == nil {
		t.Fatal("expected a record for sess-a")
	}
	if got.OutputSummary != "go vet failed" {
		t.Fatalf("latest = %q, want the newer failing run", got.OutputSummary)
	}

	missing, err := store.LatestForSession("sess-never-ran")
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for an unknown session, got %+v", missing)
	}
}

func TestLatestPassingForWorkItem(t *testing.T) {
	store := newStore(t)
	now := time.Now().UTC()

	failed := passing("sess-p", "feat-p", now.Add(-time.Minute))
	failed.Status = gateledger.StatusFail
	if _, err := store.Append(failed); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Append(passing("sess-q", "feat-p", now.Add(-10*time.Minute))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Cross-session: the passing run came from sess-q, the newest run overall
	// failed, and the lookup must still find the pass.
	got, err := store.LatestPassingForWorkItem("feat-p", time.Hour)
	if err != nil {
		t.Fatalf("LatestPassingForWorkItem: %v", err)
	}
	if got == nil || got.SessionID != "sess-q" {
		t.Fatalf("got %+v, want the passing run from sess-q", got)
	}

	// Recency filter: the same record must fall outside a tighter window.
	stale, err := store.LatestPassingForWorkItem("feat-p", time.Minute)
	if err != nil {
		t.Fatalf("LatestPassingForWorkItem: %v", err)
	}
	if stale != nil {
		t.Fatalf("expected nil outside the window, got %+v", stale)
	}
}

// TestNewlineInFieldCannotSwallowLaterRows is the non-vacuous version of the
// one-row-one-line invariant: a gate command or an operator-written allowlist
// justification carrying a newline must be collapsed, because a real newline
// would make the torn-write repair truncate to the middle of a row and drop
// every record after it.
func TestNewlineInFieldCannotSwallowLaterRows(t *testing.T) {
	store := newStore(t)
	now := time.Now().UTC()

	hostile := passing("sess-hostile", "feat-hostile", now)
	hostile.GateCommand = "go build ./...\n<tr data-record-id=\"gr-injected\">"
	hostile.OutputSummary = "line one\r\nline two\ttabbed"
	hostile.AllowlistHitsJSON = "[{\"justification\":\"multi\nline\"}]"
	if _, err := store.Append(hostile); err != nil {
		t.Fatalf("Append hostile: %v", err)
	}
	if _, err := store.Append(passing("sess-after", "feat-after", now.Add(time.Second))); err != nil {
		t.Fatalf("Append after: %v", err)
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Count(line, "<tr ") > 1 {
			t.Fatalf("more than one row on a line: %q", line)
		}
	}

	records, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("read %d records, want 2 — the hostile row swallowed the one after it", len(records))
	}
	for _, r := range records {
		if r.ID == "gr-injected" {
			t.Fatal("an escaped <tr> in a field was parsed as a record")
		}
		if strings.ContainsAny(r.GateCommand+r.OutputSummary+r.AllowlistHitsJSON, "\n\r\t") {
			t.Fatalf("record %s retained a line-breaking character", r.ID)
		}
	}
}

// TestTornTailIsRepaired proves the append path recovers from a crash mid-write
// rather than merging the new row into the corrupt fragment.
func TestTornTailIsRepaired(t *testing.T) {
	store := newStore(t)
	now := time.Now().UTC()

	if _, err := store.Append(passing("sess-intact", "feat-intact", now)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Simulate a crash: the footer and part of a second row were never fsynced.
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	torn := strings.TrimSuffix(string(raw), "</tbody>\n</table>\n</main>\n</body></html>\n")
	torn += `<tr data-record-id="gr-tor`
	if err := os.WriteFile(store.Path(), []byte(torn), 0o644); err != nil {
		t.Fatalf("write torn ledger: %v", err)
	}

	if _, err := store.Append(passing("sess-after-tear", "feat-after-tear", now.Add(time.Second))); err != nil {
		t.Fatalf("Append after tear: %v", err)
	}

	records, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("read %d records, want 2 (the intact row plus the new one)", len(records))
	}
	if records[0].SessionID != "sess-intact" || records[1].SessionID != "sess-after-tear" {
		t.Fatalf("unexpected records after repair: %+v", records)
	}
}

func TestValidateRejectsUnusableRecords(t *testing.T) {
	now := time.Now().UTC()
	cases := map[string]gateledger.Record{
		"no id":      {SessionID: "s", Status: gateledger.StatusPass, CheckedAt: now},
		"no session": {ID: "gr-1", Status: gateledger.StatusPass, CheckedAt: now},
		"bad status": {ID: "gr-1", SessionID: "s", Status: "maybe", CheckedAt: now},
		"no time":    {ID: "gr-1", SessionID: "s", Status: gateledger.StatusPass},
	}
	for name, rec := range cases {
		if err := rec.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

// TestAppendRejectsRecordWithNoSession keeps an unusable record out of the
// canonical file rather than letting it be skipped silently on read.
func TestAppendRejectsRecordWithNoSession(t *testing.T) {
	store := newStore(t)
	if _, err := store.Append(gateledger.Record{Status: gateledger.StatusPass, CheckedAt: time.Now().UTC()}); err == nil {
		t.Fatal("expected Append to reject a record with no session")
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Fatal("a rejected append created the ledger file")
	}
}

// TestSignatureCoversDecisionFields pins what the checksum is for: a hand-edited
// row cannot flip a fail into a pass and still verify.
func TestSignatureCoversDecisionFields(t *testing.T) {
	rec := passing("sess-sig", "feat-sig", time.Now().UTC())
	rec.ID = "gr-sig"
	rec.EnsureSignature()
	if !rec.SignatureValid() {
		t.Fatal("freshly stamped record did not verify")
	}

	forged := rec
	forged.Status = gateledger.StatusFail
	if forged.SignatureValid() {
		t.Fatal("a flipped status still verified")
	}

	relabelled := rec
	relabelled.AllowlistHitsJSON = `[{"id":"invented"}]`
	if relabelled.SignatureValid() {
		t.Fatal("rewritten allowlist detail still verified")
	}

	// Provenance is deliberately outside the signature: re-approving a guard
	// profile must not retroactively invalidate a record that already ran.
	reprovenanced := rec
	reprovenanced.ProfileSignature = "sha256:different"
	if !reprovenanced.SignatureValid() {
		t.Fatal("provenance change invalidated the signature; it must not be covered")
	}
}

func TestSignaturesSet(t *testing.T) {
	store := newStore(t)
	now := time.Now().UTC()

	written, err := store.Append(passing("sess-sigset", "feat-sigset", now))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	sigs, err := store.Signatures()
	if err != nil {
		t.Fatalf("Signatures: %v", err)
	}
	if !sigs[written.Signature] {
		t.Fatalf("signature %q missing from the set", written.Signature)
	}
	if sigs["not-a-signature"] {
		t.Fatal("unknown signature reported present")
	}
}

func TestReadAllOnMissingLedgerIsEmpty(t *testing.T) {
	records, err := newStore(t).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll on a missing ledger: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("read %d records from a missing ledger", len(records))
	}
}

func TestOnCommitReceivesRepoRelativePath(t *testing.T) {
	dir := t.TempDir()
	store := gateledger.NewStore(filepath.Join(dir, ".wipnote"))

	var gotRel, gotAction string
	gateledger.OnCommit = func(_, relPath, action string) {
		gotRel, gotAction = relPath, action
	}
	t.Cleanup(func() { gateledger.OnCommit = nil })

	if _, err := store.Append(passing("sess-commit", "feat-commit", time.Now().UTC())); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if gotRel != ".wipnote/"+gateledger.FileName {
		t.Fatalf("commit path = %q, want the repo-relative ledger path", gotRel)
	}
	if gotAction == "" {
		t.Fatal("commit action was empty")
	}
	if filepath.IsAbs(gotRel) {
		t.Fatalf("commit path %q is absolute; host paths must never reach a commit message", gotRel)
	}
}
