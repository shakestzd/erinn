package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db/writequeue"
)

// syscallZero is the kill(pid,0) liveness probe signal.
var syscallZero = syscall.Signal(0)

// newTestListener starts a Listener on a temp-dir socket backed by a started
// writequeue with the given applier. It returns the listener, the socket
// path, and a cleanup func. The temp dir lives under TMPDIR (set by the
// caller / CI to an exec-capable, non-git location).
func newTestListener(t *testing.T, applier Applier) (*Listener, string, func()) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock := SocketPath(dir)

	q := writequeue.New(writequeue.Config{Capacity: 16})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}

	ln, err := NewListener(ListenerConfig{SocketPath: sock, Queue: q, Applier: applier})
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = ln.Serve(ctx) }()

	// Wait for the socket inode to appear so the first dial doesn't race the bind.
	waitForSocket(t, sock)

	cleanup := func() {
		cancel()
		_ = ln.Close()
		q.Stop(time.Second)
	}
	return ln, sock, cleanup
}

func waitForSocket(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s never appeared", sock)
}

// countingApplier returns an applier whose WriteOp increments calls when run
// by the queue worker, plus a pointer to the counter.
func countingApplier() (Applier, *atomic.Int64) {
	var calls atomic.Int64
	a := func(_ Envelope) (writequeue.WriteOp, error) {
		return func(_ context.Context) error {
			calls.Add(1)
			return nil
		}, nil
	}
	return a, &calls
}

// TestSocketRoundTrip submits an op via WriterClient and asserts an
// "applied" ack with a monotonic seq, and that the op actually ran through
// the writequeue.
func TestSocketRoundTrip(t *testing.T) {
	applier, calls := countingApplier()
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var lastSeq int64
	for i := 0; i < 3; i++ {
		ack, err := client.Submit(ctx, Envelope{OpID: "op-" + strconv.Itoa(i), OpType: "test"})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if ack.Status != AckApplied {
			t.Fatalf("submit %d: status = %q, want applied (err=%q)", i, ack.Status, ack.Error)
		}
		if ack.Seq <= lastSeq {
			t.Fatalf("submit %d: seq %d not monotonic (prev %d)", i, ack.Seq, lastSeq)
		}
		lastSeq = ack.Seq
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("applier ran %d times, want 3 (ops must funnel through the writequeue)", got)
	}
}

// TestDedup verifies the same op_id twice yields applied then duplicate, and
// the op runs exactly once (the duplicate is NOT re-applied).
func TestDedup(t *testing.T) {
	applier, calls := countingApplier()
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ack1, err := client.Submit(ctx, Envelope{OpID: "dup-1", OpType: "test"})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if ack1.Status != AckApplied {
		t.Fatalf("first ack = %q, want applied", ack1.Status)
	}

	ack2, err := client.Submit(ctx, Envelope{OpID: "dup-1", OpType: "test"})
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if ack2.Status != AckDuplicate {
		t.Fatalf("second ack = %q, want duplicate", ack2.Status)
	}
	if ack2.Seq <= ack1.Seq {
		t.Fatalf("duplicate seq %d not monotonic (prev %d)", ack2.Seq, ack1.Seq)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("applier ran %d times, want 1 (duplicate must not re-apply)", got)
	}
}

// TestVersionSkew verifies an unknown op_format_version is rejected with an
// error ack and the op never runs.
func TestVersionSkew(t *testing.T) {
	applier, calls := countingApplier()
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ack, err := client.Submit(ctx, Envelope{OpFormatVersion: 999, OpID: "skew", OpType: "test"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ack.Status != AckError {
		t.Fatalf("ack = %q, want error", ack.Status)
	}
	if ack.Error == "" {
		t.Fatalf("error ack must carry a reason")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("applier ran %d times, want 0 (skewed op must not apply)", got)
	}
}

// TestApplierError verifies an applier error becomes an error ack (not a
// panic, not a mis-applied write).
func TestApplierError(t *testing.T) {
	_, sock, cleanup := newTestListener(t, RejectingApplier)
	defer cleanup()

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ack, err := client.Submit(ctx, Envelope{OpID: "x", OpType: "unknown"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ack.Status != AckError {
		t.Fatalf("ack = %q, want error", ack.Status)
	}
}

// TestClientUnavailable verifies a missing socket yields ErrWriterUnavailable.
func TestClientUnavailable(t *testing.T) {
	dir := t.TempDir()
	client := NewWriterClient(dir) // no listener bound
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := client.Submit(ctx, Envelope{OpID: "x", OpType: "test"})
	if err != ErrWriterUnavailable {
		t.Fatalf("err = %v, want ErrWriterUnavailable", err)
	}
}

// TestLease_AcquireAndHold verifies a second O_EXCL attempt fails while the
// owner holds the lease, and succeeds after the stale lease is reclaimed.
func TestLease_AcquireAndHold(t *testing.T) {
	dir := t.TempDir()

	l1, err := AcquireLease(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second attempt while owner (this live process) holds it -> ErrLeaseHeld.
	if _, err := AcquireLease(dir); err != ErrLeaseHeld {
		t.Fatalf("second acquire err = %v, want ErrLeaseHeld", err)
	}

	// Release, then re-acquire succeeds.
	if err := l1.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	l2, err := AcquireLease(dir)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = l2.Release()
}

// TestLease_StaleTakeover verifies a lease recorded for a dead PID is
// reclaimable via the O_EXCL re-attempt.
func TestLease_StaleTakeover(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write a lease file pointing at a PID that is essentially never alive.
	deadPID := findDeadPID()
	if err := os.WriteFile(LeasePath(dir), []byte(strconv.Itoa(deadPID)+"\n"), 0o644); err != nil {
		t.Fatalf("write stale lease: %v", err)
	}

	l, err := AcquireLease(dir)
	if err != nil {
		t.Fatalf("acquire over stale lease: %v", err)
	}
	defer l.Release()

	// The reclaimed lease must now record OUR pid.
	data, err := os.ReadFile(LeasePath(dir))
	if err != nil {
		t.Fatalf("read reclaimed lease: %v", err)
	}
	if got := string(data); got != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("reclaimed lease pid = %q, want our pid %d", got, os.Getpid())
	}
}

// findDeadPID returns a PID that is not currently alive. High PIDs are
// almost never in use; we probe downward from a large value.
func findDeadPID() int {
	for pid := 999999; pid > 90000; pid -= 7919 {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return pid
		}
		if proc.Signal(syscallZero) != nil {
			return pid
		}
	}
	return 999983
}
