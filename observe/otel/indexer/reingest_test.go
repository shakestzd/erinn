package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/observe/otel/receiver"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
)

// spanIDRepairStepVersion is the schema version of
// 022_otel_span_id_index_not_unique, the step that drops the UNIQUE index and
// arms the re-ingest marker. Tests that simulate a pre-repair database must
// roll back to below THIS, not to "current minus one" — later migrations move
// the current version and would leave the repair step already applied.
const spanIDRepairStepVersion = 22

// sharedSpanLines builds NDJSON in the real shape: one span plus several log
// records correlated to it, all carrying the same span_id. This is what
// bug-0fc17d53's unique index collapsed to a single row.
func sharedSpanLines(session, span string, n int) []string {
	lines := []string{fmt.Sprintf(
		`{"kind":"span","harness":"claude_code","ts":"2026-08-09T10:00:00.000Z","signal_id":"sig-span-%s","session_id":%q,"span_id":%q,"canonical":"interaction","native":"claude_code.interaction"}`,
		span, session, span)}
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf(
			`{"kind":"log","harness":"claude_code","ts":"2026-08-09T10:00:%02d.000Z","signal_id":"sig-%s-%d","session_id":%q,"span_id":%q,"canonical":"unknown","native":"hook_execution_%d"}`,
			i+1, span, i, session, span, i))
	}
	return lines
}

func readOffset(t *testing.T, wipnoteDir, session string) (int64, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(wipnoteDir, "sessions", session, ".index-offset"))
	if os.IsNotExist(err) {
		return 0, false
	}
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	var n int64
	fmt.Sscanf(string(data), "%d", &n)
	return n, true
}

func shardSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat shard: %v", err)
	}
	return fi.Size()
}

// drain runs the indexer until its checkpoint reaches the shard size.
func drain(t *testing.T, idx *Indexer, wipnoteDir, session, shardPath string) {
	t.Helper()
	want := shardSize(t, shardPath)
	for i := 0; i < 64; i++ {
		idx.RunOnce(context.Background())
		if got, _ := readOffset(t, wipnoteDir, session); got >= want {
			return
		}
	}
	t.Fatalf("indexer did not drain %s within 64 passes", shardPath)
}

// TestExistingInstallRecoversDroppedRows is the acceptance test for
// bug-0fc17d53's repair, and it is the case that matters: an install whose
// checkpoints already sit at end-of-file.
//
// Dropping the unique index stops future loss but recovers nothing on its own,
// because the indexer records a byte offset per shard and those offsets are
// already at EOF everywhere the bug has run. A fresh database ingests
// correctly and looks like proof, while every real install stays holed. This
// test therefore ingests under the OLD schema first, asserts the hole exists,
// then upgrades and requires the rows to come back.
func TestExistingInstallRecoversDroppedRows(t *testing.T) {
	const session = "sess-recover"
	const span = "span-shared"
	const logCount = 5

	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	shardPath := writeNDJSONFixture(t, wipnoteDir, session, sharedSpanLines(session, span, logCount))
	wantRows := logCount + 1

	dbPath := filepath.Join(t.TempDir(), "otel.db")
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	seed.Close()

	// The writer runs migrations at construction, so build it first and only
	// then wind the database back to the pre-fix state: unique index present,
	// user_version behind the repair step, marker disarmed.
	oldWriter, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	admin, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_otel_span_id_unique ON otel_signals(span_id) WHERE span_id IS NOT NULL`); err != nil {
		t.Fatalf("recreate pre-fix index: %v", err)
	}
	// Roll back to BELOW the repair step, not merely one version back. The
	// repair step is what arms the re-ingest marker, so "current minus one"
	// silently stops testing anything the moment a later migration is added —
	// which is exactly what happened when 023 landed.
	if _, err := admin.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, spanIDRepairStepVersion-1)); err != nil {
		t.Fatalf("roll back user_version: %v", err)
	}
	if _, err := admin.Exec(`DELETE FROM metadata WHERE key = ?`, db.OtelReingestMetadataKey); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	admin.Close()

	// Ingest under the old schema. No DB is attached, matching a build that
	// predates the re-ingest marker entirely.
	oldIdx := New(wipnoteDir, sqls.New(oldWriter))
	drain(t, oldIdx, wipnoteDir, session, shardPath)
	holed := countRows(t, dbPath, session)
	oldWriter.Close()

	// NON-VACUITY: if the old schema did not lose rows, the recovery below
	// would pass trivially and prove nothing.
	if holed != 1 {
		t.Fatalf("pre-fix ingest stored %d rows for the shared span, want 1 — "+
			"the fixture no longer reproduces the loss, so this test cannot prove recovery", holed)
	}
	off, ok := readOffset(t, wipnoteDir, session)
	if !ok || off != shardSize(t, shardPath) {
		t.Fatalf("pre-fix checkpoint = %d (present=%v), want end-of-file %d — "+
			"the test must start from a fully-consumed shard or it is not the case that matters",
			off, ok, shardSize(t, shardPath))
	}

	// Upgrade: run migrations exactly as a new binary would on first open.
	upgradeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(upgradeDB); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	// Assert the precondition for recovery directly, so a future change that
	// stops the repair step from running fails HERE with the cause named,
	// rather than downstream as an unexplained row shortfall.
	if pending, _, err := db.OtelReingestPending(upgradeDB); err != nil {
		t.Fatal(err)
	} else if !pending {
		t.Fatalf("migrations ran but the re-ingest marker is not armed — the repair step "+
			"(v%d) did not apply. Check the user_version this test rolls back to.",
			spanIDRepairStepVersion)
	}
	upgradeDB.Close()

	// Ordinary post-upgrade operation: a writer and an indexer with the DB
	// attached, exactly as serve_child wires them.
	newWriter, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer newWriter.Close()
	newIdx := New(wipnoteDir, sqls.New(newWriter)).WithWriteDB(newWriter.DB())
	drain(t, newIdx, wipnoteDir, session, shardPath)

	got := countRows(t, dbPath, session)
	if got != wantRows {
		t.Errorf("after upgrade: %d rows for session %s, want %d — the repair did not recover "+
			"the dropped rows on an install whose checkpoints were already at end-of-file",
			got, session, wantRows)
	}

	// And the checkpoint must be restored, not left at zero, or every
	// subsequent pass would re-read the whole shard forever.
	off, ok = readOffset(t, wipnoteDir, session)
	if !ok || off != shardSize(t, shardPath) {
		t.Errorf("post-recovery checkpoint = %d (present=%v), want %d", off, ok, shardSize(t, shardPath))
	}
}

// TestReingestHappensOnlyOnce: the marker is consumed, so a later indexer must
// not keep clearing checkpoints and replaying the whole corpus on every start.
//
// Note on what is asserted and why. Comparing the checkpoint offset before and
// after a second pass does NOT work: if the marker were never consumed, the
// second pass would clear the checkpoint, re-read the shard, and write the
// same end-of-file offset back — identical number, all the work repeated. That
// version of this test passed against a deliberately broken consume step. So
// assert the mechanism instead: the marker row is gone, and a further
// EnsureReingest reports nothing cleared.
func TestReingestHappensOnlyOnce(t *testing.T) {
	const session = "sess-once"

	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	shardPath := writeNDJSONFixture(t, wipnoteDir, session, sharedSpanLines(session, "span-once", 3))

	dbPath := filepath.Join(t.TempDir(), "otel.db")
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer seed.Close()

	// Arm the marker as the migration does.
	if err := db.SetOtelReingestRequired(seed, "test"); err != nil {
		t.Fatal(err)
	}

	w, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	first := New(wipnoteDir, sqls.New(w)).WithWriteDB(w.DB())
	drain(t, first, wipnoteDir, session, shardPath)
	afterFirst, ok := readOffset(t, wipnoteDir, session)
	if !ok {
		t.Fatal("no checkpoint after the first drain")
	}

	// The marker must be gone from the database.
	if reason, err := db.GetMetadata(seed, db.OtelReingestMetadataKey); err != nil {
		t.Fatal(err)
	} else if reason != "" {
		t.Errorf("re-ingest marker still armed (%q) after a completed pass — every subsequent "+
			"indexer start would clear the checkpoints and replay the entire corpus", reason)
	}

	// And nothing further is clearable.
	cleared, err := EnsureReingest(wipnoteDir, seed)
	if err != nil {
		t.Fatalf("EnsureReingest: %v", err)
	}
	if cleared != 0 {
		t.Errorf("a second EnsureReingest cleared %d checkpoint(s), want 0", cleared)
	}

	// Belt and braces: the checkpoint itself survived that call.
	afterSecond, ok := readOffset(t, wipnoteDir, session)
	if !ok {
		t.Fatal("checkpoint removed by a second EnsureReingest")
	}
	if afterSecond != afterFirst {
		t.Errorf("checkpoint moved from %d to %d", afterFirst, afterSecond)
	}
}

// TestEnsureReingestNoMarkerIsNoOp: the common path must not touch anything.
func TestEnsureReingestNoMarkerIsNoOp(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	writeNDJSONFixture(t, wipnoteDir, "sess-x", sharedSpanLines("sess-x", "span-x", 2))
	checkpoint := filepath.Join(wipnoteDir, "sessions", "sess-x", ".index-offset")
	if err := os.WriteFile(checkpoint, []byte("123"), 0o644); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(t.TempDir(), "otel.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	// A freshly-created DB runs every migration including the repair step,
	// which arms the marker; disarm it so this test starts from the steady
	// state it is about.
	if err := db.ClearOtelReingestRequired(database); err != nil {
		t.Fatal(err)
	}

	cleared, err := EnsureReingest(wipnoteDir, database)
	if err != nil {
		t.Fatalf("EnsureReingest: %v", err)
	}
	if cleared != 0 {
		t.Errorf("cleared %d checkpoints with no marker armed, want 0", cleared)
	}
	if got, ok := readOffset(t, wipnoteDir, "sess-x"); !ok || got != 123 {
		t.Errorf("checkpoint = %d (present=%v), want 123 untouched", got, ok)
	}
}

// TestReingestMarkerSurvivesAFailedClear: if the checkpoints cannot be
// cleared, the marker must stay armed so the next start retries.
//
// The dangerous ordering is the natural one — read the marker, clear it, then
// do the work — because a failure after the clear loses the recovery with no
// trace, which is the same silent-skip shape as the defect being repaired.
func TestReingestMarkerSurvivesAFailedClear(t *testing.T) {
	const session = "sess-locked"

	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	writeNDJSONFixture(t, wipnoteDir, session, sharedSpanLines(session, "span-locked", 2))
	sessDir := filepath.Join(wipnoteDir, "sessions", session)
	if err := os.WriteFile(filepath.Join(sessDir, ".index-offset"), []byte("999"), 0o644); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(t.TempDir(), "otel.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.SetOtelReingestRequired(database, "test"); err != nil {
		t.Fatal(err)
	}

	// A read-only directory makes removing the checkpoint inside it fail.
	if err := os.Chmod(sessDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sessDir, 0o755) }) //nolint:errcheck

	if _, err := EnsureReingest(wipnoteDir, database); err == nil {
		t.Skip("checkpoint removal unexpectedly succeeded in a read-only directory " +
			"(running as root?) — cannot exercise the failure ordering here")
	}

	pending, _, err := db.OtelReingestPending(database)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Error("marker was disarmed even though clearing the checkpoints failed — " +
			"the recovery is now permanently skipped for this install")
	}
}

// TestEnsureReingestWithoutDatabase: callers with no DB attached must not
// panic or clear anything.
func TestEnsureReingestWithoutDatabase(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	cleared, err := EnsureReingest(wipnoteDir, nil)
	if err != nil {
		t.Errorf("EnsureReingest(nil db) = %v, want nil", err)
	}
	if cleared != 0 {
		t.Errorf("cleared = %d, want 0", cleared)
	}
}

func countRows(t *testing.T, dbPath, session string) int {
	t.Helper()
	read, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	var n int
	if err := read.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, session).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}
