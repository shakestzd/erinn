package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/observe/otel/indexer"
)

// --- resetOtelCheckpoints / drainedToSnapshot: pure-logic unit tests -------

// TestResetOtelCheckpoints_SnapshotsSizeAndDeletesCheckpoint verifies the
// three things reindexOtelEvents' bounded drain (bug-b2471635) depends on:
// the returned session list, the size snapshot taken at that moment, and
// that any existing checkpoint file is removed so the next indexer pass
// starts from byte 0.
func TestResetOtelCheckpoints_SnapshotsSizeAndDeletesCheckpoint(t *testing.T) {
	sessionsDir := t.TempDir()
	sessDir := filepath.Join(sessionsDir, "sess-1")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"kind":"span"}` + "\n")
	if err := os.WriteFile(filepath.Join(sessDir, "events.ndjson"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	checkpointPath := filepath.Join(sessDir, ".index-offset")
	if err := os.WriteFile(checkpointPath, []byte("123"), 0o644); err != nil {
		t.Fatal(err)
	}

	sids, snapshot, err := resetOtelCheckpoints(sessionsDir)
	if err != nil {
		t.Fatalf("resetOtelCheckpoints: %v", err)
	}
	if len(sids) != 1 || sids[0] != "sess-1" {
		t.Errorf("sids = %v, want [sess-1]", sids)
	}
	if got := snapshot["sess-1"]; got != int64(len(content)) {
		t.Errorf("snapshot[sess-1] = %d, want %d", got, len(content))
	}
	if _, err := os.Stat(checkpointPath); !os.IsNotExist(err) {
		t.Errorf("expected .index-offset to be removed, stat err = %v", err)
	}
}

// TestResetOtelCheckpoints_SkipsDirsWithoutNDJSON verifies a session
// directory with no events.ndjson is excluded from both the session list
// and the snapshot, and its checkpoint (if any) is left untouched.
func TestResetOtelCheckpoints_SkipsDirsWithoutNDJSON(t *testing.T) {
	sessionsDir := t.TempDir()
	emptyDir := filepath.Join(sessionsDir, "sess-empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sids, snapshot, err := resetOtelCheckpoints(sessionsDir)
	if err != nil {
		t.Fatalf("resetOtelCheckpoints: %v", err)
	}
	if len(sids) != 0 {
		t.Errorf("sids = %v, want empty", sids)
	}
	if len(snapshot) != 0 {
		t.Errorf("snapshot = %v, want empty", snapshot)
	}
}

// TestResetOtelCheckpoints_MissingSessionsDir verifies the no-sessions-dir
// case (project has never received any OTel traffic) is not an error.
func TestResetOtelCheckpoints_MissingSessionsDir(t *testing.T) {
	sids, snapshot, err := resetOtelCheckpoints(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil error for missing sessions dir, got %v", err)
	}
	if sids != nil || snapshot != nil {
		t.Errorf("expected nil results, got sids=%v snapshot=%v", sids, snapshot)
	}
}

// TestDrainedToSnapshot covers the pure termination-condition logic used by
// reindexOtelEvents' bounded drain loop.
func TestDrainedToSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		status   map[string]indexer.FileInfo
		snapshot map[string]int64
		want     bool
	}{
		{
			name:     "empty snapshot is vacuously drained",
			status:   map[string]indexer.FileInfo{},
			snapshot: map[string]int64{},
			want:     true,
		},
		{
			name:     "session not yet in status is not drained",
			status:   map[string]indexer.FileInfo{},
			snapshot: map[string]int64{"sess-1": 100},
			want:     false,
		},
		{
			name:     "offset below snapshot is not drained",
			status:   map[string]indexer.FileInfo{"sess-1": {LastOffset: 50}},
			snapshot: map[string]int64{"sess-1": 100},
			want:     false,
		},
		{
			name:     "offset equal to snapshot is drained",
			status:   map[string]indexer.FileInfo{"sess-1": {LastOffset: 100}},
			snapshot: map[string]int64{"sess-1": 100},
			want:     true,
		},
		{
			name:     "offset past snapshot (grew further, still caught up to the mark) is drained",
			status:   map[string]indexer.FileInfo{"sess-1": {LastOffset: 150}},
			snapshot: map[string]int64{"sess-1": 100},
			want:     true,
		},
		{
			name: "one of two sessions still behind blocks the whole check",
			status: map[string]indexer.FileInfo{
				"sess-1": {LastOffset: 100},
				"sess-2": {LastOffset: 40},
			},
			snapshot: map[string]int64{"sess-1": 100, "sess-2": 50},
			want:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := drainedToSnapshot(tc.status, tc.snapshot); got != tc.want {
				t.Errorf("drainedToSnapshot() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- reindexOtelEvents: end-to-end bounded-drain proof ---------------------

// signalLineFixture builds one minimal valid NDJSON signal line accepted by
// the indexer's parseLine, matching the wire format other reindex tests use
// (see writeFixtureCollectorArtifacts in reindex_rebuild_test.go).
func signalLineFixture(sessionID, signalID string, ts time.Time) string {
	return fmt.Sprintf(
		`{"kind":"span","harness":"claude","ts":%q,"signal_id":%q,"session_id":%q,"trace_id":"trace-1","span_id":%q,"canonical":"agent.tool_call","native":"Tool","tool_name":"Bash","attrs":{}}`+"\n",
		ts.Format(time.RFC3339Nano), signalID, sessionID, signalID)
}

// TestReindexOtelEvents_BoundedDespiteConcurrentGrowth is the headline proof
// for bug-b2471635: while another process keeps appending signals to a
// session's events.ndjson for the ENTIRE duration of the call (simulating
// other live agent sessions in a busy multi-agent environment), reindexOtelEvents
// must still return promptly and must NOT capture every signal appended
// during the run -- only what was on disk at the moment it started. The
// pre-fix "drain until two stable ticks" loop would have kept chasing the
// appender for as long as it kept writing (up to the 256-iteration cap).
func TestReindexOtelEvents_BoundedDespiteConcurrentGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real concurrent-writer race")
	}

	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	sessionID := "aaaa1111-bbbb-2222-cccc-333344445555"
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	// Initial snapshot: N signals present before reindexOtelEvents starts.
	const initialN = 5
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	f, err := os.Create(ndjsonPath)
	if err != nil {
		t.Fatalf("create ndjson: %v", err)
	}
	base := time.Now().UTC()
	for i := 0; i < initialN; i++ {
		if _, err := f.WriteString(signalLineFixture(sessionID, fmt.Sprintf("initial-%d", i), base.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatalf("write initial line: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(t.TempDir(), "wipnote.db")
	initDB, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := initDB.Close(); err != nil {
		t.Fatal(err)
	}

	// Appender: writes new, distinct signals to the SAME file continuously
	// until told to stop. Started BEFORE reindexOtelEvents and stopped only
	// AFTER it returns, so append activity spans the entire call -- there is
	// no way for the old "wait for quiet" loop to have observed stability
	// while this test still holds the stop signal closed.
	stop := make(chan struct{})
	done := make(chan struct{})
	var appended int64
	go func() {
		defer close(done)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			f, err := os.OpenFile(ndjsonPath, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return
			}
			line := signalLineFixture(sessionID, fmt.Sprintf("appended-%d", i), base.Add(time.Duration(1000+i)*time.Millisecond))
			if _, err := f.WriteString(line); err != nil {
				f.Close()
				return
			}
			f.Close()
			atomic.AddInt64(&appended, 1)
			i++
		}
	}()

	start := time.Now()
	sessCount, _, errCount := reindexOtelEvents(dbPath, wipnoteDir)
	elapsed := time.Since(start)

	close(stop)
	<-done
	totalAppended := atomic.LoadInt64(&appended)

	if errCount != 0 {
		t.Fatalf("reindexOtelEvents returned errCount=%d", errCount)
	}
	if sessCount != 1 {
		t.Fatalf("sessCount = %d, want 1", sessCount)
	}

	// The appender wrote continuously for at least `elapsed` -- if
	// reindexOtelEvents had waited for it to go quiet, elapsed would be
	// bounded below by however long this test lets the appender run, which
	// in practice (given the appender never stops on its own) would mean
	// hitting the 256-iteration cap. A generous bound here (well under what
	// 256 iterations of a 4 MiB-capped drain plus 1ms sleeps would take)
	// is enough to distinguish "bounded to a snapshot" from "chased the
	// writer".
	if elapsed > 5*time.Second {
		t.Errorf("reindexOtelEvents took %s while a writer kept appending -- looks unbounded again", elapsed)
	}
	t.Logf("reindexOtelEvents: %s elapsed, appender wrote %d extra signals concurrently", elapsed, totalAppended)

	db, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID).Scan(&total); err != nil {
		t.Fatalf("count otel_signals: %v", err)
	}

	// All initial signals must be present -- the snapshot must never exclude
	// data that was already on disk when the call started.
	for i := 0; i < initialN; i++ {
		var got string
		id := fmt.Sprintf("initial-%d", i)
		if err := db.QueryRow(`SELECT signal_id FROM otel_signals WHERE signal_id = ?`, id).Scan(&got); err != nil {
			t.Errorf("initial signal %q not found: %v", id, err)
		}
	}

	// The core equivalence proof: NOT everything the appender wrote during
	// the call made it in. If this fails, the drain is chasing concurrent
	// writers again rather than stopping at a fixed snapshot.
	if int64(total) >= initialN+totalAppended {
		t.Errorf("otel_signals has %d rows for this session (initial=%d + appended=%d) -- drain captured everything the concurrent writer produced, expected a bounded subset",
			total, initialN, totalAppended)
	}
	if total < initialN {
		t.Errorf("otel_signals has %d rows, want at least the %d initial signals", total, initialN)
	}
}
