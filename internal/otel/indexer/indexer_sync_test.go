package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/internal/otel"
	"github.com/shakestzd/wipnote/internal/otel/sink"
	sqls "github.com/shakestzd/wipnote/internal/otel/sink/sqlite"
)

// recordingWriter is a sqls.WriterCloser that counts WriteBatch calls and
// optionally fails them. We need this to drive the QueuedSink without
// touching a real SQLite DB so the test focuses on the queue/checkpoint
// contract rather than schema details.
type recordingWriter struct {
	calls atomic.Int32
	// failWith, when set, causes every WriteBatch call to return this
	// error instead of succeeding. Used to simulate a writer that the
	// queue consumer cannot satisfy.
	failWith atomic.Value // error or nil
	// sleep mimics a slow downstream writer so we can pile up work in
	// the queue and reliably trigger ErrQueueFull on subsequent submits.
	sleep atomic.Int64 // nanoseconds
}

func (r *recordingWriter) WriteBatch(_ context.Context, _ otel.Harness, _ map[string]any, signals []otel.UnifiedSignal) (int, error) {
	r.calls.Add(1)
	if d := r.sleep.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
	if v := r.failWith.Load(); v != nil {
		if err, ok := v.(error); ok && err != nil {
			return 0, err
		}
	}
	return len(signals), nil
}

func (r *recordingWriter) Close() error { return nil }

// TestIndexerCheckpoint_AdvancesOnlyAfterCommit is the regression test
// for roborev #1501. Before the fix, processSession called
// QueuedSink.WriteBatch (fire-and-forget) and unconditionally advanced
// the .index-offset checkpoint — so a queue rejection silently stranded
// records in NDJSON while the checkpoint claimed they were indexed.
//
// The fix introduces sqlite.QueuedSink.WriteBatchSync (and the writequeue
// SubmitSync primitive) so the indexer can wait for the actual commit
// outcome and refuse to checkpoint on failure. This test exercises that
// contract end-to-end:
//
//  1. Stop the queue BEFORE the indexer runs (so SubmitSync returns
//     ErrWriterUnavailable — the cleanest, most deterministic shape of
//     "the write did not happen").
//  2. Run processSession with a non-empty NDJSON fixture.
//  3. Assert processSession returned a non-nil error.
//  4. Assert the .index-offset checkpoint file is absent (or 0). If the
//     bug returns, this assertion will catch it: the old behaviour
//     wrote the offset regardless of WriteBatch outcome.
func TestIndexerCheckpoint_AdvancesOnlyAfterCommit(t *testing.T) {
	wipnoteDir := t.TempDir()
	sessionID := "ckpt-no-advance-on-reject"

	lines := []string{
		`{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"ck-s1","session_id":"ckpt-no-advance-on-reject","canonical":"api_request","native":"claude_code.api_request"}`,
		`{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:01Z","signal_id":"ck-s2","session_id":"ckpt-no-advance-on-reject","canonical":"api_request","native":"claude_code.api_request"}`,
	}
	writeNDJSONFixture(t, wipnoteDir, sessionID, lines)

	rw := &recordingWriter{}
	q := writequeue.New(writequeue.Config{Capacity: 4})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue.Start: %v", err)
	}
	// Stop the queue immediately so SubmitSync rejects with
	// ErrWriterUnavailable. This is the deterministic regression shape
	// — the old code path called WriteBatch (which swallowed
	// ErrWriterUnavailable to nil) and would have advanced the
	// checkpoint anyway.
	q.Stop(time.Second)
	queued := sqls.NewQueued(q, rw)

	idxr := New(wipnoteDir, queued)

	err := idxr.processSession(context.Background(), sessionID)
	if err == nil {
		t.Fatal("processSession returned nil error despite stopped queue; checkpoint will advance with no commit (regression of roborev #1501)")
	}
	if !errors.Is(err, writequeue.ErrWriterUnavailable) {
		t.Logf("processSession returned %v (want it to surface a queue rejection; tolerated as long as the checkpoint is not advanced)", err)
	}

	checkpointPath := filepath.Join(wipnoteDir, "sessions", sessionID, ".index-offset")
	if data, statErr := os.ReadFile(checkpointPath); statErr == nil {
		t.Errorf(".index-offset exists after rejected submit: %q (regression: indexer advanced the checkpoint without a DB commit)", string(data))
	} else if !os.IsNotExist(statErr) {
		t.Errorf("unexpected error stat'ing checkpoint: %v", statErr)
	}

	if got := rw.calls.Load(); got != 0 {
		t.Errorf("recordingWriter.WriteBatch called %d times after stopped-queue rejection; want 0 (consumer was already gone)", got)
	}
}

// TestIndexerCheckpoint_AdvancesAfterSyncCommit is the happy-path
// companion: with a healthy running queue, WriteBatchSync waits for the
// consumer to commit and the indexer advances .index-offset normally.
// This guards against a future "fix" that just disables the sync path
// (which would silently regress #1501 again).
func TestIndexerCheckpoint_AdvancesAfterSyncCommit(t *testing.T) {
	wipnoteDir := t.TempDir()
	sessionID := "ckpt-advance-on-commit"

	lines := []string{
		`{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"ok-s1","session_id":"ckpt-advance-on-commit","canonical":"api_request","native":"claude_code.api_request"}`,
	}
	writeNDJSONFixture(t, wipnoteDir, sessionID, lines)

	rw := &recordingWriter{}
	q := writequeue.New(writequeue.Config{Capacity: 4})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue.Start: %v", err)
	}
	defer q.Stop(time.Second)
	queued := sqls.NewQueued(q, rw)

	idxr := New(wipnoteDir, queued)
	if err := idxr.processSession(context.Background(), sessionID); err != nil {
		t.Fatalf("processSession: %v", err)
	}

	checkpointPath := filepath.Join(wipnoteDir, "sessions", sessionID, ".index-offset")
	off, err := readCheckpoint(checkpointPath)
	if err != nil {
		t.Fatalf("readCheckpoint: %v", err)
	}
	if off <= 0 {
		t.Errorf(".index-offset = %d, want > 0 after a successful commit", off)
	}
	if got := rw.calls.Load(); got != 1 {
		t.Errorf("recordingWriter.WriteBatch called %d times, want 1", got)
	}
}

// TestQueuedSink_WriteBatchSync_SurfacesOpError pins down the contract
// directly at the sink boundary: a downstream WriteBatch failure must
// propagate to the caller (unlike async WriteBatch, which swallows
// queue-level rejections to honour canonical-first). The indexer relies
// on this so it can refuse to advance .index-offset when the inner
// SQLite writer itself errors mid-batch.
func TestQueuedSink_WriteBatchSync_SurfacesOpError(t *testing.T) {
	rw := &recordingWriter{}
	rw.failWith.Store(errors.New("simulated SQLite failure"))
	q := writequeue.New(writequeue.Config{Capacity: 4})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue.Start: %v", err)
	}
	defer q.Stop(time.Second)
	queued := sqls.NewQueued(q, rw)

	sig := otel.UnifiedSignal{
		Kind:          otel.KindSpan,
		Harness:       otel.HarnessClaude,
		SignalID:      "err-1",
		SessionID:     "err-sess",
		CanonicalName: "api_request",
	}
	err := queued.WriteBatchSync(context.Background(), otel.HarnessClaude, nil, []otel.UnifiedSignal{sig})
	if err == nil {
		t.Fatal("WriteBatchSync returned nil despite inner writer failure")
	}

	// Sanity: the async path with the same fixture must NOT surface the
	// error (it swallows queue rejections only, not op errors, so it
	// surfaces this one — keep that contract clear).
	// We don't assert on it here to avoid coupling to internal-error
	// propagation choices; the sync surface is the load-bearing one.
	_ = sink.SignalSink(queued)
}
