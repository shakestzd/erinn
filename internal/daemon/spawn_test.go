package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestSubmitOrSpawn_FastPath verifies that when a writer is already running,
// SubmitOrSpawn uses it directly (no spawn) and gets an applied ack.
func TestSubmitOrSpawn_FastPath(t *testing.T) {
	applier, calls := countingApplier()
	_, sock, cleanup := newTestListener(t, applier)
	defer cleanup()

	// projectRoot derived from the socket's parent-of-.wipnote dir.
	projectRoot := filepath.Dir(filepath.Dir(sock))

	client := NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// selfExe is irrelevant on the fast path — no spawn should occur.
	ack, err := client.SubmitOrSpawn(ctx, projectRoot, "/nonexistent/wipnote", Envelope{
		OpID: "fast-1", OpType: "test",
	})
	if err != nil {
		t.Fatalf("SubmitOrSpawn fast path: %v", err)
	}
	if ack.Status != AckApplied {
		t.Fatalf("ack = %q want applied", ack.Status)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("applier ran %d times, want 1", got)
	}
}

// TestSubmitOrSpawn_ForbiddenSpawn simulates a sandbox where no writer exists
// and the spawn cannot succeed (bogus self-exe that fails to exec). It MUST
// return ErrWriterUnavailable WITHIN the budget and never hang.
func TestSubmitOrSpawn_ForbiddenSpawn(t *testing.T) {
	dir := t.TempDir() // no listener bound; .wipnote will be created by AcquireLease
	client := NewWriterClient(dir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	// A self-exe path that cannot be executed → cmd.Start fails →
	// spawnHeadlessWriter errors → SubmitOrSpawn returns unavailable.
	_, err := client.SubmitOrSpawn(ctx, dir, "/nonexistent/definitely/not/a/binary", Envelope{
		OpID: "x", OpType: "test",
	})
	elapsed := time.Since(start)

	if err != ErrWriterUnavailable {
		t.Fatalf("err = %v, want ErrWriterUnavailable", err)
	}
	// Spawn failure is detected immediately (no readiness wait), so this is
	// far under the readiness budget. Generous ceiling to avoid flakiness.
	if elapsed > spawnReadinessBudget {
		t.Fatalf("forbidden-spawn took %v, exceeds readiness budget %v (must not hang)", elapsed, spawnReadinessBudget)
	}
}

// TestSubmitOrSpawn_RespectsDeadline verifies the call returns promptly when
// the caller's context deadline is already past — never blocking on dial,
// spawn, or readiness.
func TestSubmitOrSpawn_RespectsDeadline(t *testing.T) {
	dir := t.TempDir()
	client := NewWriterClient(dir)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(3 * time.Millisecond) // ensure deadline already elapsed

	start := time.Now()
	_, err := client.SubmitOrSpawn(ctx, dir, "/nonexistent/wipnote", Envelope{OpID: "x", OpType: "test"})
	if err != ErrWriterUnavailable {
		t.Fatalf("err = %v, want ErrWriterUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("expired-deadline call took %v, must be near-instant", elapsed)
	}
}
