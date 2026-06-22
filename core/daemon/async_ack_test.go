package daemon

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db/writequeue"
)

// blockingApplier returns an applier whose WriteOp blocks on the supplied gate
// channel before "committing". It lets a test wedge the single-writer worker so
// it can prove that an async (enqueue-only) submission acks BEFORE the op is
// applied, whereas a sync submission would block until apply. The returned
// WaitGroup tracks ops that have started but not finished running.
func blockingApplier(gate <-chan struct{}) (Applier, *sync.WaitGroup) {
	var wg sync.WaitGroup
	a := func(_ Envelope) (writequeue.WriteOp, error) {
		wg.Add(1)
		return func(_ context.Context) error {
			defer wg.Done()
			<-gate // block until the test releases the worker
			return nil
		}, nil
	}
	return a, &wg
}

// TestAsyncAck_EnqueuedNotApplied proves that an Envelope with Async=true is
// acked AckEnqueued the instant the op is durably handed to the writequeue —
// NOT after it is applied. We wedge the single-writer worker with a blocking
// applier; a sync submit would hang on apply, but the async submit must return
// promptly with AckEnqueued while the op is still un-applied.
func TestAsyncAck_EnqueuedNotApplied(t *testing.T) {
	gate := make(chan struct{})
	applier, wg := blockingApplier(gate)
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)

	// First op: occupies the single worker and blocks inside apply.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel1()
	ack1, err := client.Submit(ctx1, Envelope{OpID: "async-occupy", OpType: "test", Async: true})
	if err != nil {
		t.Fatalf("occupy submit: %v", err)
	}
	if ack1.Status != AckEnqueued {
		t.Fatalf("occupy ack = %q, want %q", ack1.Status, AckEnqueued)
	}

	// Second op: the worker is now blocked applying op 1, so op 2 cannot have
	// been applied. An async submit must STILL return AckEnqueued well under
	// the applied-ack budget — proving ack-on-enqueue, not ack-on-apply.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()
	start := time.Now()
	ack2, err := client.Submit(ctx2, Envelope{OpID: "async-while-busy", OpType: "test", Async: true})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("async submit while worker busy: %v", err)
	}
	if ack2.Status != AckEnqueued {
		t.Fatalf("async ack = %q, want %q (must not wait for apply)", ack2.Status, AckEnqueued)
	}
	// Hard upper bound: enqueue-only must be sub-second even with a wedged
	// writer. The applied-ack path would block here until the gate releases.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("async submit took %v with a busy writer — it waited for apply, not enqueue", elapsed)
	}

	// Release the worker and let both ops drain so cleanup is clean.
	close(gate)
	wg.Wait()
}

// TestAsyncAck_SyncDefaultUnchanged asserts the DEFAULT (Async=false) path is
// unchanged: it still funnels through SubmitSync and returns AckApplied only
// after the op commits. The existing typed routes rely on this.
func TestAsyncAck_SyncDefaultUnchanged(t *testing.T) {
	applier, calls := countingApplier()
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// No Async flag → applied-ack, and the op has actually run by ack time.
	for i := 0; i < 3; i++ {
		ack, err := client.Submit(ctx, Envelope{OpID: "sync-" + strconv.Itoa(i), OpType: "test"})
		if err != nil {
			t.Fatalf("sync submit %d: %v", i, err)
		}
		if ack.Status != AckApplied {
			t.Fatalf("sync submit %d: status = %q, want applied", i, ack.Status)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("sync default: applier ran %d times, want 3 (must wait for apply)", got)
	}
}

// TestAsyncAck_DuplicateStillDeduped asserts the async path honours op_id
// dedup: a replayed async op_id returns AckDuplicate without re-enqueueing.
func TestAsyncAck_DuplicateStillDeduped(t *testing.T) {
	applier, calls := countingApplier()
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ack1, err := client.Submit(ctx, Envelope{OpID: "async-dup", OpType: "test", Async: true})
	if err != nil {
		t.Fatalf("first async submit: %v", err)
	}
	if ack1.Status != AckEnqueued {
		t.Fatalf("first async ack = %q, want %q", ack1.Status, AckEnqueued)
	}
	// Let the first op drain so the count settles before the duplicate.
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	ack2, err := client.Submit(ctx, Envelope{OpID: "async-dup", OpType: "test", Async: true})
	if err != nil {
		t.Fatalf("second async submit: %v", err)
	}
	if ack2.Status != AckDuplicate {
		t.Fatalf("second async ack = %q, want %q", ack2.Status, AckDuplicate)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("async dedup: applier ran %d times, want 1", got)
	}
}
