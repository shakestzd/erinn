package retention_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/sessionledger"
	"github.com/shakestzd/wipnote/observe/otel/retention"
)

// archiveLedgerSessionID is session-shaped so the ledger will accept it; an id
// the target-validity gate would not recognise as a session must never become a
// row.
const archiveLedgerSessionID = "aaaabbbb-cccc-dddd-eeee-ffff00003333"

// TestArchiveSession_RecordsArchivePathInLedger pins the archive-time
// enrichment. Archiving is the moment the live session directory stops
// existing, so it is the last point at which anything knows where the raw
// events went; without this the ledger row survives but the pointer back to the
// detail does not.
func TestArchiveSession_RecordsArchivePathInLedger(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	makeSessionDir(t, wipnoteDir, archiveLedgerSessionID, "{\"kind\":\"log\"}\n")

	store := sessionledger.NewStore(wipnoteDir)
	start := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := store.Open(sessionledger.Record{
		SessionID: archiveLedgerSessionID,
		Harness:   "claude-code",
		StartedAt: start,
	}); err != nil {
		t.Fatalf("seed ledger row: %v", err)
	}

	if err := retention.ArchiveSession(wipnoteDir, archiveLedgerSessionID, false); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	rec, ok, err := store.Get(archiveLedgerSessionID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !ok {
		t.Fatal("archiving removed the session's ledger row")
	}
	if rec.ArchivePath == "" {
		t.Fatal("archive path was not recorded; the row no longer points at the raw events")
	}
	if filepath.IsAbs(rec.ArchivePath) {
		t.Errorf("archive path %q is absolute — a host path must never reach a canonical artifact",
			rec.ArchivePath)
	}
	// The recorded path must actually locate the tarball from the repo root.
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(wipnoteDir), rec.ArchivePath)); statErr != nil {
		t.Errorf("recorded archive path %q does not resolve: %v", rec.ArchivePath, statErr)
	}
	if rec.IsOpen() {
		t.Error("archiving left the row open; the events mtime is an end time and should have closed it")
	}
	if !rec.StartedAt.Equal(start) {
		t.Errorf("archiving moved the start time to %v, want %v", rec.StartedAt, start)
	}
}

// TestArchiveSession_CreatesLedgerRowForPreLedgerSession is the recovery case
// that runs without a backfill command: a session that started before the
// ledger existed still gets a row the moment its events are archived.
func TestArchiveSession_CreatesLedgerRowForPreLedgerSession(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	makeSessionDir(t, wipnoteDir, archiveLedgerSessionID, "{\"kind\":\"log\"}\n")

	if err := retention.ArchiveSession(wipnoteDir, archiveLedgerSessionID, false); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	rec, ok, err := sessionledger.NewStore(wipnoteDir).Get(archiveLedgerSessionID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !ok {
		t.Fatal("archiving a session with no prior row did not create one")
	}
	if rec.IsOpen() {
		t.Error("the created row has no end time, though the archive supplied one")
	}
}
