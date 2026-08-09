package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/gateledger"
	"github.com/shakestzd/wipnote/internal/commitqueue"
)

func purgeGateRecords(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`DELETE FROM gate_records`); err != nil {
		t.Fatalf("purge gate_records: %v", err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM gate_records`).Scan(&n); err != nil {
		t.Fatalf("count gate_records: %v", err)
	}
	if n != 0 {
		t.Fatalf("gate_records still holds %d rows after the purge", n)
	}
}

func countGateRecords(t *testing.T, database *sql.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM gate_records`).Scan(&n); err != nil {
		t.Fatalf("count gate_records: %v", err)
	}
	return n
}

// TestGateRecord_SurvivesIndexPurge is the acceptance test for bug-550c1cd8: a
// gate run must outlive the derived index, which lives in the OS cache directory
// and may be purged at any time.
//
// The assertion is deliberately two-sided. It is not enough that the ledger has
// the record — the index must be verifiably EMPTY at the same moment, otherwise
// the test could pass on a stale row and prove nothing.
func TestGateRecord_SurvivesIndexPurge(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}
	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	if _, err := runSessionGate(projectRoot, "sess-purge", "feat-purge", "check", "quality", os.Stdout, os.Stderr); err != nil {
		t.Fatalf("runSessionGate: %v", err)
	}
	purgeGateRecords(t, database)

	indexed, err := dbpkg.LatestGateRecordForSession(database, "sess-purge")
	if err != nil {
		t.Fatalf("LatestGateRecordForSession: %v", err)
	}
	if indexed != nil {
		t.Fatal("index still holds a record after the purge — the test setup did not simulate a cache wipe")
	}

	canonical, err := gateledger.StoreForProject(projectRoot).LatestForSession("sess-purge")
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if canonical == nil {
		t.Fatal("the gate run did not survive the index purge")
	}
	if !canonical.Passed() || !canonical.SignatureValid() {
		t.Fatalf("surviving record is unusable: status=%q signatureValid=%v", canonical.Status, canonical.SignatureValid())
	}
	if canonical.WorkItemID != "feat-purge" {
		t.Fatalf("work item = %q, want feat-purge", canonical.WorkItemID)
	}
}

// TestCompletionGate_VerdictTracksLedgerNotIndex is the whole feature in one
// test, and it is non-vacuous in both directions:
//
//   - Emptying gate_records must NOT change whether a completion is permitted.
//     If it did, gate decisions would still be a function of a purgeable cache.
//   - Removing the LEDGER while gate_records is full must REFUSE the completion.
//     Without this half the first assertion could pass with the gate still
//     reading the index, since both sources agreed.
func TestCompletionGate_VerdictTracksLedgerNotIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}
	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	if _, err := runSessionGate(projectRoot, "sess-verdict", "feat-verdict", "check", "quality", os.Stdout, os.Stderr); err != nil {
		t.Fatalf("runSessionGate: %v", err)
	}
	ledgerPath := gateledger.StoreForProject(projectRoot).Path()
	ledgerBytes, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}

	// Half one: index emptied, ledger intact — the completion must be permitted.
	purgeGateRecords(t, database)
	if err := validateCompletionGateRecord(projectRoot, "sess-verdict", "feat-verdict"); err != nil {
		t.Fatalf("completion refused with an empty index; the verdict is still coupled to the cache: %v", err)
	}

	// Half two: ledger removed, index full — the completion must be refused.
	// The re-check the gate runs at the end would rewrite the ledger, so the
	// index is reloaded from the surviving rows first and the ledger removed
	// immediately before the call.
	if countGateRecords(t, database) == 0 {
		t.Fatal("expected the re-check above to have repopulated the index")
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatalf("remove ledger: %v", err)
	}
	err = validateCompletionGateRecord(projectRoot, "sess-never-gated", "feat-verdict")
	if err == nil {
		t.Fatal("completion permitted with no canonical record; the gate is still reading the index")
	}
	if !strings.Contains(err.Error(), "wipnote check --gate") {
		t.Fatalf("expected the remediation command in the refusal, got: %v", err)
	}

	// Restoring the canonical file restores the verdict — proving the refusal
	// above came from the ledger's absence and not from some unrelated failure.
	if err := os.WriteFile(ledgerPath, ledgerBytes, 0o644); err != nil {
		t.Fatalf("restore ledger: %v", err)
	}
	if err := validateCompletionGateRecord(projectRoot, "sess-verdict", "feat-verdict"); err != nil {
		t.Fatalf("completion refused after restoring the ledger: %v", err)
	}
}

// TestCompletionGate_SessionScopedLookupIsCanonical isolates the FIRST of the
// gate's two lookups.
//
// It exists because a partial regression is otherwise invisible: pointing only
// the session-scoped lookup back at the index still passes
// TestCompletionGate_VerdictTracksLedgerNotIndex, because the cross-session
// fallback — still canonical — rescues the same record. Verified by breaking
// exactly that one lookup and watching the other test stay green.
//
// The isolation trick is the work item. The ledger holds a passing run for
// (sess-scoped, feat-scoped-a); the completion asks about feat-scoped-b, which
// has no passing record at all, so the fallback CANNOT rescue and only the
// session-scoped lookup can permit it.
func TestCompletionGate_SessionScopedLookupIsCanonical(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}
	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	if _, err := runSessionGate(projectRoot, "sess-scoped", "feat-scoped-a", "check", "quality", os.Stdout, os.Stderr); err != nil {
		t.Fatalf("runSessionGate: %v", err)
	}
	purgeGateRecords(t, database)

	fallback, err := gateledger.StoreForProject(projectRoot).LatestPassingForWorkItem("feat-scoped-b", 6*time.Hour)
	if err != nil {
		t.Fatalf("LatestPassingForWorkItem: %v", err)
	}
	if fallback != nil {
		t.Fatal("feat-scoped-b has a passing record; the fallback could rescue this and the test would prove nothing")
	}

	if err := validateCompletionGateRecord(projectRoot, "sess-scoped", "feat-scoped-b"); err != nil {
		t.Fatalf("session-scoped lookup did not resolve from the ledger: %v", err)
	}
}

// TestGateRecord_WriteThenReadBeforeCommitQueueFlush covers the sequencing worry:
// `wipnote check --gate` writes a record and a later `wipnote feature complete`
// — a DIFFERENT process — reads it. Only the git commit is deferred, so the read
// must succeed with the queue still holding an unflushed intent.
func TestGateRecord_WriteThenReadBeforeCommitQueueFlush(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}
	projectRoot := setupGateTestProject(t)
	initGitRepo(t, projectRoot)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	outbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(outbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	t.Setenv("WIPNOTE_ARTIFACT_COMMIT_POLICY", string(workitemArtifactCommitPolicyDefer))
	initGateLedgerCommitSeam()
	t.Cleanup(func() { gateledger.OnCommit = nil })

	if _, err := runSessionGate(projectRoot, "sess-unflushed", "feat-unflushed", "check", "quality", os.Stdout, os.Stderr); err != nil {
		t.Fatalf("runSessionGate: %v", err)
	}

	// The intent must still be PENDING — otherwise the read below would be
	// proving nothing about the unflushed window.
	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	pending, err := ob.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if !hasGateLedgerIntent(pending) {
		t.Fatalf("expected an unflushed gate-ledger commit intent, got %d intents", len(pending))
	}

	// A fresh Store, as a separate process would construct: the bytes are on
	// disk and fsynced even though nothing has been committed.
	record, err := gateledger.StoreForProject(projectRoot).LatestForSession("sess-unflushed")
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if record == nil {
		t.Fatal("gate record unreadable before the commit queue flushed")
	}
	if !record.Passed() {
		t.Fatalf("record status = %q, want pass", record.Status)
	}
	if err := validateCompletionGateRecord(projectRoot, "sess-unflushed", "feat-unflushed"); err != nil {
		t.Fatalf("completion refused before the commit queue flushed: %v", err)
	}
}

func hasGateLedgerIntent(intents []commitqueue.Intent) bool {
	for _, in := range intents {
		for _, p := range in.RelPaths {
			if strings.HasSuffix(p, gateledger.FileName) {
				return true
			}
		}
	}
	return false
}

// TestReindexGateRecords_RebuildsPurgedIndex proves the index is genuinely
// derived: a wiped gate_records table rebuilds from the ledger alone, carrying
// the allowlist detail with it, and a second pass inserts nothing.
func TestReindexGateRecords_RebuildsPurgedIndex(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	store := gateledger.NewStore(wipnoteDir)
	now := time.Now().UTC()
	forgiven := gateledger.Record{
		SessionID:         "sess-rebuild",
		WorkItemID:        "feat-rebuild",
		Harness:           "claude-code",
		ProjectType:       "go",
		GateCommand:       "go test -short ./...",
		Status:            gateledger.StatusPass,
		CheckedAt:         now,
		Source:            "check",
		OutputSummary:     "go test allowlisted",
		AllowlistHitsJSON: `[{"id":"listener-socket-sandbox","justification":"sandbox forbids listener binds"}]`,
		AllowlistHitCount: 1,
	}
	if _, err := store.Append(forgiven); err != nil {
		t.Fatalf("Append forgiven: %v", err)
	}
	clean := forgiven
	clean.ID = ""
	clean.Signature = ""
	clean.CheckedAt = now.Add(time.Minute)
	clean.OutputSummary = "all commands passed"
	clean.AllowlistHitsJSON = "[]"
	clean.AllowlistHitCount = 0
	if _, err := store.Append(clean); err != nil {
		t.Fatalf("Append clean: %v", err)
	}

	purgeGateRecords(t, database)

	inserted, errs := reindexGateRecords(database, wipnoteDir, true)
	if errs != 0 {
		t.Fatalf("reindexGateRecords reported %d errors", errs)
	}
	if inserted != 2 {
		t.Fatalf("rebuilt %d rows, want 2", inserted)
	}
	if got := countGateRecords(t, database); got != 2 {
		t.Fatalf("gate_records holds %d rows, want 2", got)
	}

	// The forgiven pass must remain distinguishable from the clean one. This is
	// the property that dies permanently if the allowlist detail is dropped.
	var hitsJSON string
	var hitCount int
	if err := database.QueryRow(
		`SELECT allowlist_hits_json, allowlist_hit_count FROM gate_records WHERE output_summary = 'go test allowlisted'`,
	).Scan(&hitsJSON, &hitCount); err != nil {
		t.Fatalf("read rebuilt allowlist detail: %v", err)
	}
	if hitCount != 1 || !strings.Contains(hitsJSON, "listener-socket-sandbox") {
		t.Fatalf("allowlist detail did not rebuild: count=%d json=%s", hitCount, hitsJSON)
	}

	// Idempotent: a warm cache must not accumulate duplicates.
	again, errs := reindexGateRecords(database, wipnoteDir, true)
	if errs != 0 {
		t.Fatalf("second reindexGateRecords reported %d errors", errs)
	}
	if again != 0 {
		t.Fatalf("second pass inserted %d rows, want 0", again)
	}
	if got := countGateRecords(t, database); got != 2 {
		t.Fatalf("gate_records holds %d rows after a second pass, want 2", got)
	}
}

// TestBackfillGateLedgerFromIndex gives pre-ledger gate runs a canonical home.
// Without this the ledger would protect only future runs and every historical
// record would still be lost on the first cache purge.
func TestBackfillGateLedgerFromIndex(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	legacy := &dbpkg.GateRecord{
		SessionID:         "sess-legacy",
		WorkItemID:        "feat-legacy",
		Harness:           "claude-code",
		ProjectType:       "go",
		GateCommand:       "go build ./...",
		Status:            "pass",
		CheckedAt:         time.Now().UTC().Add(-2 * time.Hour),
		Source:            "check",
		OutputSummary:     "go test allowlisted",
		AllowlistHitsJSON: `[{"id":"tmp-noexec","justification":"noexec temp dir"}]`,
		AllowlistHitCount: 1,
	}
	if err := dbpkg.InsertGateRecord(database, legacy); err != nil {
		t.Fatalf("insert legacy record: %v", err)
	}

	written, errs := backfillGateLedgerFromIndex(database, wipnoteDir, true)
	if errs != 0 {
		t.Fatalf("backfill reported %d errors", errs)
	}
	if written != 1 {
		t.Fatalf("backfilled %d rows, want 1", written)
	}

	records, err := gateledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("ledger holds %d records, want 1", len(records))
	}
	got := records[0]
	if got.Signature != legacy.Signature {
		t.Fatalf("signature = %q, want the legacy signature %q carried over verbatim", got.Signature, legacy.Signature)
	}
	if !got.SignatureValid() {
		t.Fatal("backfilled record's signature does not re-verify")
	}
	if got.AllowlistHitCount != 1 || !strings.Contains(got.AllowlistHitsJSON, "tmp-noexec") {
		t.Fatalf("allowlist detail lost in backfill: count=%d json=%s", got.AllowlistHitCount, got.AllowlistHitsJSON)
	}

	// The source row must now be stamped, so a second pass is a no-op.
	var stamped string
	if err := database.QueryRow(`SELECT record_id FROM gate_records WHERE id = ?`, legacy.ID).Scan(&stamped); err != nil {
		t.Fatalf("read record_id: %v", err)
	}
	if stamped != got.ID {
		t.Fatalf("index row stamped %q, want the canonical id %q", stamped, got.ID)
	}
	again, errs := backfillGateLedgerFromIndex(database, wipnoteDir, true)
	if errs != 0 || again != 0 {
		t.Fatalf("second backfill wrote %d rows with %d errors, want 0/0", again, errs)
	}
}

// TestBackfillGateLedger_DedupesBySignatureWhenStampWasLost covers the crash
// window: the ledger append succeeded but the record_id stamp did not, so the
// row is offered again. Matching on the signature keeps that retry from writing
// the same gate run twice.
func TestBackfillGateLedger_DedupesBySignatureWhenStampWasLost(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	legacy := &dbpkg.GateRecord{
		SessionID:   "sess-crash",
		WorkItemID:  "feat-crash",
		ProjectType: "go",
		GateCommand: "go build ./...",
		Status:      "pass",
		CheckedAt:   time.Now().UTC().Add(-time.Hour),
		Source:      "check",
	}
	if err := dbpkg.InsertGateRecord(database, legacy); err != nil {
		t.Fatalf("insert legacy record: %v", err)
	}
	if written, errs := backfillGateLedgerFromIndex(database, wipnoteDir, true); written != 1 || errs != 0 {
		t.Fatalf("first backfill wrote %d rows with %d errors, want 1/0", written, errs)
	}

	// Simulate the crash: the stamp is rolled back, leaving the row unledgered
	// again even though its record is already canonical.
	if _, err := database.Exec(`UPDATE gate_records SET record_id = '' WHERE id = ?`, legacy.ID); err != nil {
		t.Fatalf("clear record_id: %v", err)
	}

	if written, errs := backfillGateLedgerFromIndex(database, wipnoteDir, true); written != 0 || errs != 0 {
		t.Fatalf("retry wrote %d rows with %d errors, want 0/0", written, errs)
	}
	records, err := gateledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("ledger holds %d records after the retry, want 1", len(records))
	}
}
