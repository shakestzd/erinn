package daemon

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db/writequeue"
)

// failingApplier returns an applier whose WriteOp increments calls every time
// it runs and returns the error produced by failNext(). failNext is consulted
// PER apply so a test can make the first apply fail and a later resubmit
// succeed. The counter lets the test prove the op actually re-ran (rather than
// being deduped away).
func failingApplier(failNext func() error) (Applier, *atomic.Int64) {
	var calls atomic.Int64
	a := func(_ Envelope) (writequeue.WriteOp, error) {
		return func(_ context.Context) error {
			calls.Add(1)
			return failNext()
		}, nil
	}
	return a, &calls
}

// waitForCalls spins until the worker has run the applier n times or the
// deadline elapses. Async submits ack before apply, so a test must wait for the
// out-of-band apply to land before asserting on its side effects.
func waitForCalls(t *testing.T, calls *atomic.Int64, n int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("applier ran %d times, want >= %d within deadline", calls.Load(), n)
}

// TestAsyncDedup_RemovedOnApplyFailure_ResubmitReApplies is the roborev-480
// finding 1 regression test. An async op is acked AckEnqueued and its op_id is
// recorded for in-flight dedup; when the deferred apply FAILS the dedup entry
// must be removed so a resubmit of the SAME op_id re-applies (is NOT swallowed
// as AckDuplicate). Before the fix the resubmit was acked AckDuplicate and the
// derived write was silently lost until the next reindex.
func TestAsyncDedup_RemovedOnApplyFailure_ResubmitReApplies(t *testing.T) {
	// First apply fails; every subsequent apply succeeds.
	var failed atomic.Bool
	applier, calls := failingApplier(func() error {
		if failed.CompareAndSwap(false, true) {
			return errors.New("simulated apply failure")
		}
		return nil
	})
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Submit 1 (async): acked on enqueue; the worker then FAILS the apply,
	// which must roll back the enqueue-time dedup entry.
	ack1, err := client.Submit(ctx, Envelope{OpID: "async-fail", OpType: "test", Async: true})
	if err != nil {
		t.Fatalf("first async submit: %v", err)
	}
	if ack1.Status != AckEnqueued {
		t.Fatalf("first ack = %q, want %q", ack1.Status, AckEnqueued)
	}
	waitForCalls(t, calls, 1) // apply ran (and failed)

	// Submit 2 (same op_id): because the failed apply removed the dedup entry,
	// this must NOT be deduped — it must re-run. The second apply succeeds.
	ack2, err := client.Submit(ctx, Envelope{OpID: "async-fail", OpType: "test", Async: true})
	if err != nil {
		t.Fatalf("resubmit after failure: %v", err)
	}
	if ack2.Status == AckDuplicate {
		t.Fatalf("resubmit after apply FAILURE was deduped (%q) — the lost-write bug", ack2.Status)
	}
	if ack2.Status != AckEnqueued {
		t.Fatalf("resubmit ack = %q, want %q (must re-enqueue, not dedup)", ack2.Status, AckEnqueued)
	}
	waitForCalls(t, calls, 2) // the resubmit actually re-applied

	if got := calls.Load(); got != 2 {
		t.Fatalf("applier ran %d times, want 2 (one failed apply + one successful re-apply)", got)
	}
}

// TestAsyncDedup_KeptOnApplySuccess_DuplicateIgnored is the inverse guard: a
// SUCCESSFUL async op must KEEP its dedup entry, so a later resubmit of the same
// op_id is ignored (AckDuplicate, applier not re-run). This proves the fix does
// not over-correct into dropping the entry for ops that applied cleanly.
func TestAsyncDedup_KeptOnApplySuccess_DuplicateIgnored(t *testing.T) {
	applier, calls := failingApplier(func() error { return nil }) // always succeeds
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ack1, err := client.Submit(ctx, Envelope{OpID: "async-ok", OpType: "test", Async: true})
	if err != nil {
		t.Fatalf("first async submit: %v", err)
	}
	if ack1.Status != AckEnqueued {
		t.Fatalf("first ack = %q, want %q", ack1.Status, AckEnqueued)
	}
	waitForCalls(t, calls, 1) // apply succeeded; dedup entry must remain

	ack2, err := client.Submit(ctx, Envelope{OpID: "async-ok", OpType: "test", Async: true})
	if err != nil {
		t.Fatalf("duplicate async submit: %v", err)
	}
	if ack2.Status != AckDuplicate {
		t.Fatalf("duplicate after SUCCESS = %q, want %q (success must stay deduped)", ack2.Status, AckDuplicate)
	}
	// Give any (incorrect) re-apply a chance to land before asserting the count.
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("applier ran %d times, want 1 (a successful op's duplicate must not re-apply)", got)
	}
}

// TestSyncDedup_Unchanged_AppliedThenDuplicate guards that the SYNC path's
// dedup behaviour is untouched by the finding-1 fix: a sync op records dedup on
// successful apply and a duplicate is ignored. (The remove-on-failure logic is
// async-only; sync ops only record dedup AFTER SubmitSync confirms the commit,
// so there is nothing to roll back.)
func TestSyncDedup_Unchanged_AppliedThenDuplicate(t *testing.T) {
	applier, calls := countingApplier()
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for i, want := range []AckStatus{AckApplied, AckDuplicate} {
		ack, err := client.Submit(ctx, Envelope{OpID: "sync-dedup", OpType: "test"})
		if err != nil {
			t.Fatalf("sync submit %d: %v", i, err)
		}
		if ack.Status != want {
			t.Fatalf("sync submit %d: status = %q, want %q", i, ack.Status, want)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("sync dedup: applier ran %d times, want 1", got)
	}
}

// TestVersionSkew_MixedVersionRoundTrip is the roborev-480 finding 2 wire test.
// After bumping OpFormatVersion to 2, an envelope carrying a DIFFERENT version
// (a stale OLD client, or a new client hitting an old daemon) must be
// error-acked by the daemon's version check and the op must NEVER run — no
// silent mis-apply. We exercise both a too-old (1) and a too-new (3) version to
// prove the check is an equality gate, not a floor.
func TestVersionSkew_MixedVersionRoundTrip(t *testing.T) {
	applier, calls := countingApplier()
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for _, ver := range []int{1, OpFormatVersion + 1} {
		ack, err := client.Submit(ctx, Envelope{
			OpFormatVersion: ver,
			OpID:            "skew-" + strconv.Itoa(ver),
			OpType:          "test",
		})
		if err != nil {
			t.Fatalf("submit version %d: %v", ver, err)
		}
		if ack.Status != AckError {
			t.Fatalf("version %d ack = %q, want %q (daemon speaks %d)", ver, ack.Status, AckError, OpFormatVersion)
		}
		if ack.Error == "" {
			t.Fatalf("version %d: error ack must carry a reason", ver)
		}
	}

	// A matching-version op still applies — the gate rejects only the skew.
	ack, err := client.Submit(ctx, Envelope{OpType: "test", OpID: "matched"}) // version defaulted to current
	if err != nil {
		t.Fatalf("matched-version submit: %v", err)
	}
	if ack.Status != AckApplied {
		t.Fatalf("matched-version ack = %q, want %q", ack.Status, AckApplied)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("applier ran %d times, want 1 (skewed ops must not apply, matched op must)", got)
	}
}
