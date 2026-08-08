package indexer

// TestIndexer_PromptIDRoutingThroughQueue is the focused regression test for
// bug-272c5e34 Change 2.  It constructs an Indexer with the production
// split-handle wiring:
//
//	indexer.New(...).WithDB(readDB).WithWriteDB(writeDB).WithQueue(q)
//
// and feeds a real user_prompt NDJSON signal through the normal processing
// entrypoint (processSession → writeParsedBatch → maybeSetPromptID).
//
// The test proves TWO things:
//
//  1. The prompt-ID correlation actually lands — the agent_events row's
//     prompt_id column is non-NULL after the queue drains.
//
//  2. It arrived via the queue worker, not a direct writeDB write — the
//     queue's Dequeued counter increments strictly more than the batch
//     writes (which all go through WriteBatchSync), confirming the
//     prompt-ID WriteOp was enqueued as a separate fire-and-forget op.
//
// The proof that it went through the queue (not a direct write) is
// structural: the writeDB handle is only touched by the queue's consumer
// goroutine in this test — there is no other path that can set prompt_id
// on the row.  If WithQueue routing regresses to a direct write, the
// test still passes (the value lands), but the Dequeued > batchDequeued
// assertion below will catch the bypass because the direct path would
// never increment q.Stats().Dequeued.
//
// roborev followup for commit 3333b22c4.
import (
	"context"
	"database/sql"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
)

func TestIndexer_PromptIDRoutingThroughQueue(t *testing.T) {
	// ── DB setup ─────────────────────────────────────────────────────────
	// setupIndexerDB opens+migrates, then builds a receiver.Writer (which we
	// need so we can also use it as the QueuedSink's underlying writer for
	// the WriteBatchSync batch path).
	w, dbPath := setupIndexerDB(t)

	// Open a separate writable handle — this is the handle passed to
	// WithWriteDB and used by the queue worker for prompt-ID writes.
	// It mirrors serve_child.go's writeDB.
	writeDB, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open writeDB: %v", err)
	}
	defer writeDB.Close()

	// Open a read-only handle — passed to WithDB for the orphan-filter
	// SELECT path. Mirrors serve_child.go's database (read-only pool).
	readDSN := dbPath + "?_pragma=busy_timeout(5000)&mode=ro"
	readDB, err := sql.Open("sqlite", readDSN)
	if err != nil {
		t.Fatalf("open readDB: %v", err)
	}
	defer readDB.Close()

	// ── Seed: sessions row (required FK) ──────────────────────────────────
	sessionID := "prmq-sess-01"
	if _, err := writeDB.Exec(
		`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, 'claude-code')
		 ON CONFLICT(session_id) DO NOTHING`,
		sessionID,
	); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	// ── Seed: agent_events UserQuery row with NULL prompt_id ──────────────
	// SetPromptID matches by session_id + timestamp ±5s, so the seeded
	// timestamp must be within 5s of the signal's timestamp.
	eventTS := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	eventID := "prmq-evt-01"
	if _, err := writeDB.Exec(
		`INSERT INTO agent_events
		     (event_id, agent_id, event_type, tool_name, session_id, timestamp, prompt_id)
		 VALUES (?, 'human', 'tool_call', 'UserQuery', ?, ?, NULL)`,
		eventID, sessionID, eventTS.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed agent_events: %v", err)
	}

	// ── Write queue ───────────────────────────────────────────────────────
	// The queue worker is the only thing that will touch writeDB for
	// prompt-ID writes — providing the observable invariant that
	// Dequeued > batchDequeued iff the queue path was taken.
	q := writequeue.New(writequeue.Config{
		Capacity: writequeue.DefaultCapacity,
		OnError: func(err error) {
			t.Logf("queue op error: %v", err)
		},
	})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue.Start: %v", err)
	}

	// ── NDJSON fixture ────────────────────────────────────────────────────
	// A user_prompt log signal.  The canonical name triggers maybeSetPromptID.
	// The timestamp is exactly eventTS so SetPromptID finds the seeded row.
	wipnoteDir := t.TempDir()
	promptID := "prmq-prompt-id-xyz"
	userPromptLine := `{"kind":"log","harness":"claude_code","ts":"2026-05-18T10:00:00Z","signal_id":"prmq-sig-01","session_id":"prmq-sess-01","canonical":"user_prompt","native":"claude_code.user_prompt","prompt_id":"prmq-prompt-id-xyz"}`
	writeNDJSONFixture(t, wipnoteDir, sessionID, []string{userPromptLine})

	// ── Indexer construction — production wiring ──────────────────────────
	queued := sqls.NewQueued(q, w)
	idxr := New(wipnoteDir, queued).
		WithDB(readDB).
		WithWriteDB(writeDB).
		WithQueue(q)

	// Capture Dequeued before processing so we can isolate the prompt-ID op.
	statsBefore := q.Stats()

	// ── Process ───────────────────────────────────────────────────────────
	if err := idxr.processSession(context.Background(), sessionID); err != nil {
		t.Fatalf("processSession: %v", err)
	}

	// Drain the queue fully before reading back the prompt_id value.
	// Stop with a generous timeout — the queue only has one WriteBatchSync
	// op (for the signal batch) plus one fire-and-forget prompt-ID op.
	q.Stop(5 * time.Second)

	statsAfter := q.Stats()

	// ── Assertion 1: prompt_id actually landed ─────────────────────────
	var gotPromptID sql.NullString
	if err := writeDB.QueryRow(
		`SELECT prompt_id FROM agent_events WHERE event_id = ?`, eventID,
	).Scan(&gotPromptID); err != nil {
		t.Fatalf("query prompt_id: %v", err)
	}
	if !gotPromptID.Valid || gotPromptID.String != promptID {
		t.Errorf("prompt_id = %v, want %q (bridge did not run or wrong value)", gotPromptID, promptID)
	}

	// ── Assertion 2: the prompt-ID write went through the queue ───────────
	// The batch write (WriteBatchSync for the user_prompt signal) consumes
	// exactly 1 Dequeued tick.  The prompt-ID fire-and-forget op submits an
	// additional WriteOp, so total Dequeued must be > batchDequeued (1).
	// If maybeSetPromptID regresses to a direct write, it never calls
	// q.Submit, so Dequeued stays at 1 and this assertion fails.
	batchDequeued := statsAfter.Dequeued - statsBefore.Dequeued
	if batchDequeued <= 1 {
		t.Errorf("queue Dequeued delta = %d, want > 1 (expected at least 1 batch op + 1 prompt-ID op); "+
			"prompt-ID bridge may have bypassed the queue and written directly to writeDB",
			batchDequeued)
	}
}

// TestIndexer_PromptIDReindexPath verifies the legacy / reindex fallback
// branch in maybeSetPromptID.
//
// The ACTUAL wipnote reindex wiring (cmd/wipnote/reindex_otel_events.go:84-86)
// is:
//
//	idxr := indexer.New(wipnoteDir, sink)
//	if bridgeDB != nil {
//	    idxr = idxr.WithDB(bridgeDB)   // writable handle, no WithWriteDB, no WithQueue
//	}
//
// That exercises the third branch of maybeSetPromptID: idx.database (the
// legacy/single-handle fallback), NOT idx.writeDB and NOT idx.queue.  This
// test constructs the indexer with exactly that wiring so a regression in
// the idx.database branch would be caught here.
func TestIndexer_PromptIDReindexPath(t *testing.T) {
	w, dbPath := setupIndexerDB(t)

	// bridgeDB: writable handle, passed only through WithDB — exactly what
	// reindex_otel_events.go does.  WithWriteDB and WithQueue are NOT called.
	bridgeDB, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open bridgeDB: %v", err)
	}
	defer bridgeDB.Close()

	sessionID := "prmq-reindex-sess"
	if _, err := bridgeDB.Exec(
		`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, 'claude-code')
		 ON CONFLICT(session_id) DO NOTHING`,
		sessionID,
	); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	eventID := "prmq-reindex-evt"
	if _, err := bridgeDB.Exec(
		`INSERT INTO agent_events
		     (event_id, agent_id, event_type, tool_name, session_id, timestamp, prompt_id)
		 VALUES (?, 'human', 'tool_call', 'UserQuery', ?, '2026-05-18T10:00:00Z', NULL)`,
		eventID, sessionID,
	); err != nil {
		t.Fatalf("seed agent_events: %v", err)
	}

	wipnoteDir := t.TempDir()
	promptID := "prmq-reindex-prompt"
	line := `{"kind":"log","harness":"claude_code","ts":"2026-05-18T10:00:00Z","signal_id":"prmq-rx-s1","session_id":"prmq-reindex-sess","canonical":"user_prompt","native":"claude_code.user_prompt","prompt_id":"prmq-reindex-prompt"}`
	writeNDJSONFixture(t, wipnoteDir, sessionID, []string{line})

	// Mirror reindex_otel_events.go exactly: New + WithDB only.
	// No WithWriteDB, no WithQueue — exercises the idx.database legacy branch.
	idxr := New(wipnoteDir, sqls.New(w)).
		WithDB(bridgeDB)

	if err := idxr.processSession(context.Background(), sessionID); err != nil {
		t.Fatalf("processSession: %v", err)
	}

	var gotPromptID sql.NullString
	if err := bridgeDB.QueryRow(
		`SELECT prompt_id FROM agent_events WHERE event_id = ?`, eventID,
	).Scan(&gotPromptID); err != nil {
		t.Fatalf("query prompt_id: %v", err)
	}
	if !gotPromptID.Valid || gotPromptID.String != promptID {
		t.Errorf("reindex path: prompt_id = %v, want %q", gotPromptID, promptID)
	}
}
