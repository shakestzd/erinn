package indexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
)

// openMainDB opens a wipnote main DB (which includes the sessions table)
// at the given path.
func openMainDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// makeSessionDir creates a session directory with an events.ndjson under wipnoteDir.
func makeSessionDir(t *testing.T, wipnoteDir, sessionID string) string {
	t.Helper()
	dir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	ndjson := filepath.Join(dir, "events.ndjson")
	if err := os.WriteFile(ndjson, []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile events.ndjson: %v", err)
	}
	return dir
}

// makeAgedSessionDir creates a session directory and back-dates it by age.
func makeAgedSessionDir(t *testing.T, wipnoteDir, sessionID string, age time.Duration) string {
	t.Helper()
	dir := makeSessionDir(t, wipnoteDir, sessionID)
	oldTime := time.Now().Add(-age)
	if err := os.Chtimes(dir, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes dir: %v", err)
	}
	ndjson := filepath.Join(dir, "events.ndjson")
	if err := os.Chtimes(ndjson, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes ndjson: %v", err)
	}
	return dir
}

// insertSessionRow inserts a minimal sessions row into the given DB.
func insertMainSessionRow(t *testing.T, database *sql.DB, sessionID string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES (?, 'claude-code', 'active', ?)`,
		sessionID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert session %s: %v", sessionID, err)
	}
}

// TestDiscoverSessions_SkipsOrphans seeds FS with 5 session dirs and DB with
// 3 corresponding sessions; asserts only the 3 in-DB sessions are returned.
//
// Note (roborev #1505): the orphan-filter policy now only skips
// directories that are BOTH stale (>= orphanMinAge) AND quiescent (no
// writes in orphanQuiescenceWindow). To exercise the skip path we
// back-date the orphan directories so they qualify; fresh orphan dirs
// are intentionally NOT skipped — see TestDiscoverSessions_KeepsRecentOrphans
// for that case.
func TestDiscoverSessions_SkipsOrphans(t *testing.T) {
	_, dbPath := setupIndexerDB(t)
	mainDB := openMainDB(t, dbPath)
	wipnoteDir := t.TempDir()

	knownSessions := []string{"sess-known-b1", "sess-known-b2", "sess-known-b3"}
	orphanSessions := []string{"sess-orphan-a1", "sess-orphan-a2"}

	// Known sessions: fresh dirs are fine — they have a DB row.
	for _, sid := range knownSessions {
		makeSessionDir(t, wipnoteDir, sid)
	}
	// Orphan sessions: age them well past orphanMinAge (1h) and make
	// the ndjson stale beyond orphanQuiescenceWindow (5m). 2 hours is
	// comfortably past both thresholds.
	for _, sid := range orphanSessions {
		makeAgedSessionDir(t, wipnoteDir, sid, 2*time.Hour)
	}
	for _, sid := range knownSessions {
		insertMainSessionRow(t, mainDB, sid)
	}

	snk := sqls.New(&fakeWriter{})
	idxr := New(wipnoteDir, snk).WithDB(mainDB)

	sessions, err := idxr.discoverSessions()
	if err != nil {
		t.Fatalf("discoverSessions: %v", err)
	}

	if len(sessions) != 3 {
		t.Errorf("want 3 sessions (in-DB only), got %d: %v", len(sessions), sessions)
	}

	knownSet := map[string]bool{}
	for _, s := range knownSessions {
		knownSet[s] = true
	}
	for _, s := range sessions {
		if !knownSet[s] {
			t.Errorf("returned orphan session %q (not in DB)", s)
		}
	}
}

// TestDiscoverSessions_KeepsRecentOrphans is the roborev #1505
// regression test: orphan session directories that are EITHER recent
// (< orphanMinAge) OR actively producing telemetry (last write within
// orphanQuiescenceWindow) must remain in the indexer's working set
// even if no sessions row exists yet. Hook-failure, late-plugin-load,
// and OTel-only sessions all rely on this: their session row is
// written by a downstream code path (writer queue / hook gate) AFTER
// the NDJSON starts appearing.
//
// Three fixtures cover the policy matrix:
//
//   - "recent-orphan-30s"  : 30s old, no DB row.            Keep.
//   - "active-old-orphan"  : 2h old dir, ndjson written 1m ago. Keep.
//   - "stale-quiet-orphan" : 2h old dir, ndjson stale 2h.   Skip.
func TestDiscoverSessions_KeepsRecentOrphans(t *testing.T) {
	_, dbPath := setupIndexerDB(t)
	mainDB := openMainDB(t, dbPath)
	wipnoteDir := t.TempDir()

	// Recent orphan — fresh dir (< 1h).
	makeSessionDir(t, wipnoteDir, "recent-orphan-30s")

	// Active old orphan — back-date the dir but refresh the ndjson so
	// it reads as actively producing.
	makeAgedSessionDir(t, wipnoteDir, "active-old-orphan", 2*time.Hour)
	ndjson := filepath.Join(wipnoteDir, "sessions", "active-old-orphan", "events.ndjson")
	recent := time.Now().Add(-1 * time.Minute)
	if err := os.Chtimes(ndjson, recent, recent); err != nil {
		t.Fatalf("Chtimes active ndjson: %v", err)
	}

	// Truly orphan — old dir AND stale ndjson.
	makeAgedSessionDir(t, wipnoteDir, "stale-quiet-orphan", 2*time.Hour)

	snk := sqls.New(&fakeWriter{})
	idxr := New(wipnoteDir, snk).WithDB(mainDB)

	sessions, err := idxr.discoverSessions()
	if err != nil {
		t.Fatalf("discoverSessions: %v", err)
	}

	want := map[string]bool{
		"recent-orphan-30s": true,
		"active-old-orphan": true,
	}
	got := map[string]bool{}
	for _, s := range sessions {
		got[s] = true
	}
	for sid := range want {
		if !got[sid] {
			t.Errorf("orphan session %q was dropped; should have been kept (recent or actively writing)", sid)
		}
	}
	if got["stale-quiet-orphan"] {
		t.Errorf("stale+quiescent orphan was kept; should have been skipped")
	}
}

// TestDiscoverSessions_NoDBAttached verifies all sessions are returned when
// no DB is attached (no orphan filtering).
func TestDiscoverSessions_NoDBAttached(t *testing.T) {
	wipnoteDir := t.TempDir()
	for _, sid := range []string{"sess-1", "sess-2", "sess-3"} {
		makeSessionDir(t, wipnoteDir, sid)
	}
	snk := sqls.New(&fakeWriter{})
	idxr := New(wipnoteDir, snk) // no WithDB

	sessions, err := idxr.discoverSessions()
	if err != nil {
		t.Fatalf("discoverSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("want 3 sessions (no DB filter), got %d", len(sessions))
	}
}

// TestFindOrphanSessions_ReturnsOrphans verifies FindOrphanSessions detects
// directories with no DB row.
func TestFindOrphanSessions_ReturnsOrphans(t *testing.T) {
	_, dbPath := setupIndexerDB(t)
	mainDB := openMainDB(t, dbPath)
	wipnoteDir := t.TempDir()

	makeSessionDir(t, wipnoteDir, "known-sess-001")
	makeSessionDir(t, wipnoteDir, "orphan-sess-001")
	makeSessionDir(t, wipnoteDir, "orphan-sess-002")
	insertMainSessionRow(t, mainDB, "known-sess-001")

	orphans, err := FindOrphanSessions(wipnoteDir, mainDB)
	if err != nil {
		t.Fatalf("FindOrphanSessions: %v", err)
	}
	if len(orphans) != 2 {
		t.Errorf("want 2 orphans, got %d: %v", len(orphans), orphans)
	}
	for _, o := range orphans {
		if o.SessionID == "known-sess-001" {
			t.Errorf("known session should not appear in orphans")
		}
	}
}

// TestIsEligibleForDeletion_RespectsRetention verifies a young orphan (< 14d)
// is not eligible even with no recent writes.
func TestIsEligibleForDeletion_RespectsRetention(t *testing.T) {
	young := OrphanInfo{
		SessionID:   "young-orphan",
		Age:         10 * 24 * time.Hour, // 10 days < 14 day retention
		LastWriteAt: time.Now().Add(-48 * time.Hour),
	}
	if IsEligibleForDeletion(young) {
		t.Error("young orphan (10d) should NOT be eligible for deletion (retention=14d)")
	}

	old := OrphanInfo{
		SessionID:   "old-orphan",
		Age:         20 * 24 * time.Hour, // 20 days > 14 day retention
		LastWriteAt: time.Now().Add(-48 * time.Hour),
	}
	if !IsEligibleForDeletion(old) {
		t.Error("old orphan (20d) with no recent writes SHOULD be eligible for deletion")
	}
}

// TestIsEligibleForDeletion_RejectsRecentWrites verifies an old orphan with
// recent writes is not eligible.
func TestIsEligibleForDeletion_RejectsRecentWrites(t *testing.T) {
	recentWrite := OrphanInfo{
		SessionID:   "old-but-active-orphan",
		Age:         30 * 24 * time.Hour,            // 30 days old
		LastWriteAt: time.Now().Add(-1 * time.Hour), // written 1h ago
	}
	if IsEligibleForDeletion(recentWrite) {
		t.Error("orphan with recent write (1h ago) should NOT be eligible")
	}
}

// TestCleanupOrphanSessions_DryRunListsOnly verifies that listing orphans
// (the dry-run path) does not delete any directories.
func TestCleanupOrphanSessions_DryRunListsOnly(t *testing.T) {
	_, dbPath := setupIndexerDB(t)
	mainDB := openMainDB(t, dbPath)
	wipnoteDir := t.TempDir()
	orphanDir := makeAgedSessionDir(t, wipnoteDir, "orphan-list-test-01", 20*24*time.Hour)

	orphans, err := FindOrphanSessions(wipnoteDir, mainDB)
	if err != nil {
		t.Fatalf("FindOrphanSessions: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("want 1 orphan, got %d", len(orphans))
	}

	// Simulate dry-run: just call FindOrphanSessions, do not delete.
	// Assert the directory still exists.
	if _, err := os.Stat(orphanDir); err != nil {
		t.Errorf("orphan dir should still exist after dry-run list: %v", err)
	}
}

// TestCleanupOrphanSessions_RespectsRetention verifies that an orphan dir
// younger than OrphanRetentionDays is NOT eligible for deletion even with
// --delete --yes.
func TestCleanupOrphanSessions_RespectsRetention(t *testing.T) {
	wipnoteDir := t.TempDir()
	youngAge := 5 * 24 * time.Hour // 5 days < 14-day retention
	makeAgedSessionDir(t, wipnoteDir, "young-orphan-test-01", youngAge)

	_, dbPath := setupIndexerDB(t)
	mainDB := openMainDB(t, dbPath)

	orphans, err := FindOrphanSessions(wipnoteDir, mainDB)
	if err != nil {
		t.Fatalf("FindOrphanSessions: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("want 1 orphan, got %d", len(orphans))
	}
	if IsEligibleForDeletion(orphans[0]) {
		t.Errorf("orphan dir younger than %d days should NOT be eligible for deletion", OrphanRetentionDays)
	}
}

// TestOrphanSession_NoIndexerRetry verifies that a stale + quiescent
// orphan session NDJSON is never processed by the indexer across
// multiple ticks (zero INSERT attempts). The fixture is intentionally
// back-dated past both orphanMinAge and orphanQuiescenceWindow so the
// new "keep recent / active orphans" policy (roborev #1505) cannot
// keep it.
func TestOrphanSession_NoIndexerRetry(t *testing.T) {
	w, dbPath := setupIndexerDB(t)
	wipnoteDir := t.TempDir()

	orphanID := "orphan-no-retry-sess"
	lines := []string{
		`{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"orp-s1","session_id":"` + orphanID + `","canonical":"api_request","native":"claude_code.api_request"}`,
	}
	ndjsonPath := writeNDJSONFixture(t, wipnoteDir, orphanID, lines)
	// Age both the directory and the ndjson file so the orphan is
	// stale+quiescent and the filter skips it.
	oldTime := time.Now().Add(-2 * time.Hour)
	dir := filepath.Dir(ndjsonPath)
	if err := os.Chtimes(dir, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes dir: %v", err)
	}
	if err := os.Chtimes(ndjsonPath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes ndjson: %v", err)
	}

	// Open main DB — intentionally do NOT insert a sessions row for orphanID.
	mainDB := openMainDB(t, dbPath)

	snk := sqls.New(w)
	idxr := New(wipnoteDir, snk).WithDB(mainDB)
	ctx := context.Background()

	// Two ticks — orphan should be skipped both times.
	idxr.runOnce(ctx)
	idxr.runOnce(ctx)

	got := countSignals(t, w, orphanID)
	if got != 0 {
		t.Errorf("orphan session should have 0 signals in DB after 2 ticks, got %d", got)
	}
}

// TestFormatAge verifies formatAge produces non-empty output for typical durations.
func TestFormatAge(t *testing.T) {
	cases := []time.Duration{
		30 * time.Second,
		5 * time.Minute,
		3 * time.Hour,
		10 * 24 * time.Hour,
	}
	for _, d := range cases {
		got := formatAge(d)
		if len(got) == 0 {
			t.Errorf("formatAge(%v) returned empty string", d)
		}
	}
}

// TestQueryKnownSessionIDs_Empty verifies the function handles empty input.
func TestQueryKnownSessionIDs_Empty(t *testing.T) {
	_, dbPath := setupIndexerDB(t)
	mainDB := openMainDB(t, dbPath)

	known, err := queryKnownSessionIDs(mainDB, nil)
	if err != nil {
		t.Fatalf("queryKnownSessionIDs: %v", err)
	}
	if len(known) != 0 {
		t.Errorf("want empty map, got %v", known)
	}
}

// TestFilterSessionsByDB_NilDB verifies fail-open when no DB is attached.
func TestFilterSessionsByDB_NilDB(t *testing.T) {
	candidates := []string{"a", "b", "c"}
	result := filterSessionsByDB(nil, "/tmp", candidates)
	if len(result) != 3 {
		t.Errorf("nil DB should return all candidates, got %d", len(result))
	}
}
