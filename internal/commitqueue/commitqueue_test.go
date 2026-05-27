package commitqueue

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestOutbox returns an Outbox rooted in a per-test temp dir so tests never
// touch the real ~/.cache.
func newTestOutbox(t *testing.T) *Outbox {
	t.Helper()
	return NewOutbox(filepath.Join(t.TempDir(), "outbox.ndjson"))
}

func sampleIntent(id string) Intent {
	return Intent{
		RepoRoot:   "/repo",
		RelPaths:   []string{".wipnote/features/" + id + ".html"},
		Message:    "wipnote: complete " + id,
		WorkItemID: id,
		Action:     "complete",
		EnqueuedAt: time.Now().UTC(),
	}
}

// okCommitter records the intents it committed and always succeeds.
func okCommitter(committed *[]Intent) Committer {
	return func(i Intent) error {
		*committed = append(*committed, i)
		return nil
	}
}

// --- Step 2: recording an intent appends to the outbox ---

func TestAppendRecordsIntent(t *testing.T) {
	o := newTestOutbox(t)
	if err := o.Append(sampleIntent("feat-1")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].WorkItemID != "feat-1" {
		t.Fatalf("expected 1 intent feat-1, got %+v", pending)
	}
	depth, _ := o.Depth()
	if depth != 1 {
		t.Fatalf("Depth = %d, want 1", depth)
	}
}

func TestAppendRejectsInvalidIntent(t *testing.T) {
	o := newTestOutbox(t)
	if err := o.Append(Intent{Message: "no paths"}); err == nil {
		t.Fatal("expected validation error for intent with no repo_root/paths")
	}
	depth, _ := o.Depth()
	if depth != 0 {
		t.Fatalf("invalid intent should not be queued, depth = %d", depth)
	}
}

func TestAppendIsFIFO(t *testing.T) {
	o := newTestOutbox(t)
	for _, id := range []string{"feat-1", "feat-2", "feat-3"} {
		if err := o.Append(sampleIntent(id)); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}
	pending, _ := o.Pending()
	got := []string{pending[0].WorkItemID, pending[1].WorkItemID, pending[2].WorkItemID}
	want := []string{"feat-1", "feat-2", "feat-3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FIFO order broken: got %v want %v", got, want)
		}
	}
}

// --- Step 3: flush drains FIFO and commits via the injected committer ---

func TestFlushDrainsFIFOAndCommits(t *testing.T) {
	o := newTestOutbox(t)
	for _, id := range []string{"feat-1", "feat-2", "feat-3"} {
		_ = o.Append(sampleIntent(id))
	}
	var committed []Intent
	res, err := o.Flush(okCommitter(&committed), MaxAttempts)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if res.Committed != 3 || res.RemainingDepth != 0 {
		t.Fatalf("res = %+v, want Committed=3 Remaining=0", res)
	}
	order := []string{committed[0].WorkItemID, committed[1].WorkItemID, committed[2].WorkItemID}
	if order[0] != "feat-1" || order[2] != "feat-3" {
		t.Fatalf("commit order not FIFO: %v", order)
	}
	depth, _ := o.Depth()
	if depth != 0 {
		t.Fatalf("outbox not drained, depth = %d", depth)
	}
}

func TestFlushEmptyOutboxIsNoOp(t *testing.T) {
	o := newTestOutbox(t)
	var committed []Intent
	res, err := o.Flush(okCommitter(&committed), MaxAttempts)
	if err != nil {
		t.Fatalf("Flush empty: %v", err)
	}
	if res.Committed != 0 || len(committed) != 0 {
		t.Fatalf("empty flush did work: %+v", res)
	}
}

// --- Step 4: interrupted flush recovery (re-flush, idempotent) ---

// TestInterruptedFlushReFlushesWithoutDoubleCommitHarm simulates a flush that
// commits the first intent then "crashes" before draining the rest (the commit
// for the second intent errors). A subsequent flush re-processes the remaining
// intents. Because the underlying commit is idempotent, re-running is safe — we
// assert the already-committed intent is NOT re-removed-then-lost and that the
// queue converges to empty once commits succeed.
func TestInterruptedFlushReFlushesWithoutDoubleCommitHarm(t *testing.T) {
	o := newTestOutbox(t)
	for _, id := range []string{"feat-1", "feat-2", "feat-3"} {
		_ = o.Append(sampleIntent(id))
	}

	// First pass: feat-1 commits, feat-2 "crashes" (transient failure), feat-3
	// still gets attempted but also fails this pass.
	var firstPass []Intent
	failTransient := func(i Intent) error {
		if i.WorkItemID == "feat-1" {
			firstPass = append(firstPass, i)
			return nil
		}
		return fmt.Errorf("transient failure committing %s", i.WorkItemID)
	}
	res1, err := o.Flush(failTransient, MaxAttempts)
	if err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	if res1.Committed != 1 {
		t.Fatalf("first pass committed %d, want 1 (feat-1)", res1.Committed)
	}
	// feat-2 and feat-3 remain queued with Attempts incremented.
	pending, _ := o.Pending()
	if len(pending) != 2 {
		t.Fatalf("after interrupted pass, depth = %d, want 2", len(pending))
	}
	for _, p := range pending {
		if p.Attempts != 1 {
			t.Fatalf("intent %s Attempts = %d, want 1", p.WorkItemID, p.Attempts)
		}
	}

	// Second pass: everything now succeeds (idempotent commit). feat-1 is NOT
	// re-attempted because it was already removed; the queue converges to empty.
	var secondPass []Intent
	res2, err := o.Flush(okCommitter(&secondPass), MaxAttempts)
	if err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if res2.Committed != 2 || res2.RemainingDepth != 0 {
		t.Fatalf("second pass res = %+v, want Committed=2 Remaining=0", res2)
	}
	for _, p := range secondPass {
		if p.WorkItemID == "feat-1" {
			t.Fatal("feat-1 was re-committed on recovery — should have been removed in pass 1")
		}
	}
	depth, _ := o.Depth()
	if depth != 0 {
		t.Fatalf("queue did not converge to empty, depth = %d", depth)
	}
}

// TestPendingToleratesPartialTrailingLine ensures a crash mid-append (a partial
// final line) does not abort the drain — earlier intents are still read.
func TestPendingToleratesPartialTrailingLine(t *testing.T) {
	o := newTestOutbox(t)
	_ = o.Append(sampleIntent("feat-1"))
	// Append a corrupt partial line directly to simulate a crash mid-write.
	if err := appendLineLocked(o.Path(), []byte(`{"repo_root":"/repo","rel`)); err != nil {
		t.Fatalf("appendLineLocked: %v", err)
	}
	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].WorkItemID != "feat-1" {
		t.Fatalf("partial line not tolerated: %+v", pending)
	}
}

// --- Step 5: dead-letter semantics ---

// TestDeadLetterAfterMaxAttemptsAndQueueKeepsDraining drives one poison intent
// to the dead-letter threshold and verifies that a healthy intent enqueued
// behind it still gets committed (the queue is not frozen).
func TestDeadLetterAfterMaxAttemptsAndQueueKeepsDraining(t *testing.T) {
	o := newTestOutbox(t)
	_ = o.Append(sampleIntent("poison"))
	_ = o.Append(sampleIntent("healthy"))

	const maxAttempts = 3
	commit := func(i Intent) error {
		if i.WorkItemID == "poison" {
			return fmt.Errorf("always fails")
		}
		return nil // healthy commits fine
	}

	// Pass 1 and 2: poison fails (Attempts 1, then 2), healthy commits on pass 1
	// and is gone thereafter.
	res1, _ := o.Flush(commit, maxAttempts)
	if res1.Committed != 1 {
		t.Fatalf("pass1 committed %d, want 1 (healthy drains past poison)", res1.Committed)
	}
	if res1.DeadLettered != 0 {
		t.Fatalf("pass1 dead-lettered %d, want 0", res1.DeadLettered)
	}
	res2, _ := o.Flush(commit, maxAttempts)
	if res2.DeadLettered != 0 {
		t.Fatalf("pass2 dead-lettered %d, want 0 (Attempts now 2)", res2.DeadLettered)
	}

	// Pass 3: poison reaches Attempts==3 == maxAttempts → dead-lettered.
	res3, _ := o.Flush(commit, maxAttempts)
	if res3.DeadLettered != 1 {
		t.Fatalf("pass3 dead-lettered %d, want 1", res3.DeadLettered)
	}
	depth, _ := o.Depth()
	if depth != 0 {
		t.Fatalf("pending not empty after dead-letter, depth = %d", depth)
	}
	dlDepth, _ := o.DeadLetterDepth()
	if dlDepth != 1 {
		t.Fatalf("dead-letter depth = %d, want 1", dlDepth)
	}
	dl, _ := o.DeadLettered()
	if dl[0].WorkItemID != "poison" || dl[0].Attempts != maxAttempts {
		t.Fatalf("dead-lettered intent wrong: %+v", dl[0])
	}
}

// TestPoisonDoesNotFreezeQueueInSinglePass verifies that within ONE pass, an
// intent that hits the dead-letter threshold does not block intents behind it.
func TestPoisonDoesNotFreezeQueueInSinglePass(t *testing.T) {
	o := newTestOutbox(t)
	// Pre-load the poison intent already at maxAttempts-1 so it dead-letters
	// this pass, with a healthy intent queued behind it.
	poison := sampleIntent("poison")
	poison.Attempts = 2
	_ = o.Append(poison)
	_ = o.Append(sampleIntent("behind"))

	const maxAttempts = 3
	var committed []Intent
	commit := func(i Intent) error {
		if i.WorkItemID == "poison" {
			return fmt.Errorf("poison")
		}
		committed = append(committed, i)
		return nil
	}
	res, err := o.Flush(commit, maxAttempts)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if res.DeadLettered != 1 {
		t.Fatalf("DeadLettered = %d, want 1", res.DeadLettered)
	}
	if res.Committed != 1 || len(committed) != 1 || committed[0].WorkItemID != "behind" {
		t.Fatalf("intent behind poison not committed in same pass: %+v", committed)
	}
	depth, _ := o.Depth()
	if depth != 0 {
		t.Fatalf("queue frozen: depth = %d", depth)
	}
}

func TestDeadLetterPathDerivation(t *testing.T) {
	o := NewOutbox("/cache/wipnote/abc/commit-outbox.ndjson")
	want := "/cache/wipnote/abc/commit-outbox.deadletter.ndjson"
	if o.DeadLetterPath() != want {
		t.Fatalf("DeadLetterPath = %q, want %q", o.DeadLetterPath(), want)
	}
}

// TestConcurrentAppendDuringFlushIsNotLost is the regression for the lost-update
// race (roborev HIGH on feat-76504033): an Append that lands while a Flush is
// mid-commit must NOT be clobbered by the flush's stale-snapshot rewrite. The
// committer blocks the flush (holding the cross-operation lock) while a second
// goroutine attempts to Append a new intent; once the flush completes, the new
// intent must still be queued.
func TestConcurrentAppendDuringFlushIsNotLost(t *testing.T) {
	o := newTestOutbox(t)
	if err := o.Append(sampleIntent("a")); err != nil {
		t.Fatalf("seed append: %v", err)
	}

	// atLockBoundary fires (via the beforeLockForTest seam) each time an
	// operation reaches the flock acquisition: once for the flush, then once for
	// the concurrent append. Buffered so the synchronous seam never blocks.
	atLockBoundary := make(chan struct{}, 2)
	o.beforeLockForTest = func() { atLockBoundary <- struct{}{} }

	started := make(chan struct{})
	proceed := make(chan struct{})
	committer := func(_ Intent) error {
		close(started) // flush has snapshotted and is now committing "a"
		<-proceed      // hold the flush (and its lock) until the test releases
		return nil
	}

	flushDone := make(chan error, 1)
	go func() {
		_, err := o.Flush(committer, 0)
		flushDone <- err
	}()

	<-atLockBoundary // the flush has reached (and will hold) the outbox lock
	<-started        // flush is mid-commit, definitively holding the lock

	appendDone := make(chan error, 1)
	go func() { appendDone <- o.Append(sampleIntent("b")) }()

	// Deterministic (no sleep): wait until the append goroutine reaches the lock
	// boundary. If instead it COMPLETES without ever signaling the boundary —
	// i.e. a regression where Append bypasses withLock, the old lost-update
	// behavior — fail fast here rather than hanging until the global test timeout
	// (roborev #3669). In correct code atLockBoundary is always signaled before
	// Append could complete (it cannot pass the held lock), so there is no race.
	select {
	case <-atLockBoundary:
		// expected: append has reached the lock and will block on it
	case err := <-appendDone:
		t.Fatalf("Append completed without acquiring the outbox lock (not serialized): err=%v", err)
	}
	select {
	case err := <-appendDone:
		t.Fatalf("Append completed while the flush held the outbox lock (not serialized): err=%v", err)
	default:
		// expected: Append is parked on the outbox lock
	}

	close(proceed) // let the flush finish, rewrite, and release the lock

	if err := <-flushDone; err != nil {
		t.Fatalf("flush: %v", err)
	}
	// Only now — after the flush released the lock — may the Append complete.
	if err := <-appendDone; err != nil {
		t.Fatalf("concurrent append: %v", err)
	}

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].WorkItemID != "b" {
		t.Fatalf("concurrent-append intent was lost: pending=%+v, want exactly [b]", pending)
	}
}

// TestAppend_RecoversAfterTornTrailingLine is the regression for roborev #3723:
// after a crash leaves a partial (newline-less) trailing line, the next Append
// must NOT merge into it (which would make both lines unparseable and silently
// drop the new intent).
func TestAppend_RecoversAfterTornTrailingLine(t *testing.T) {
	o := newTestOutbox(t)
	if err := o.Append(sampleIntent("a")); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	// Simulate a crash mid-append: a truncated JSON line with no trailing newline.
	f, err := os.OpenFile(o.Path(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"work_item_id":"partial`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := o.Append(sampleIntent("b")); err != nil {
		t.Fatalf("append after torn tail: %v", err)
	}
	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	ids := map[string]bool{}
	for _, in := range pending {
		ids[in.WorkItemID] = true
	}
	if !ids["a"] || !ids["b"] {
		t.Errorf("both intents must survive a torn trailing line; got %v", ids)
	}
}

// TestValidate_RejectsUnsafeRelPaths is the regression for roborev #3723: a
// blank/absolute/escaping rel_path must be rejected so a malformed intent can't
// stage the whole repo or files outside it.
func TestValidate_RejectsUnsafeRelPaths(t *testing.T) {
	for _, bad := range []string{"", "   ", "/abs/path", "../escape"} {
		i := sampleIntent("x")
		i.RelPaths = []string{bad}
		if err := i.Validate(); err == nil {
			t.Errorf("rel_path %q must be rejected by Validate", bad)
		}
	}
}
