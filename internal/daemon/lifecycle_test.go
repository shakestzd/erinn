package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/db/writequeue"
)

// startQueue returns a started writequeue and a stop func for lifecycle tests.
func startQueue(t *testing.T) (*writequeue.Queue, func()) {
	t.Helper()
	q := writequeue.New(writequeue.Config{Capacity: 16})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	return q, func() { q.Stop(time.Second) }
}

// TestStaleSocketUnlinkedOnBind verifies that NewListener unlinks a leftover
// socket inode (from an unclean prior exit) so the bind succeeds — provided no
// live lease owner exists. A Unix-socket bind fails with EADDRINUSE if the path
// already exists, so this is required for clean restart after a crash.
func TestStaleSocketUnlinkedOnBind(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock := SocketPath(dir)

	// Simulate a crashed prior owner: a leftover bound socket inode plus a
	// stale lease pointing at a dead PID.
	prior, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	// Close the Go listener but DELETE-then-recreate the inode so the file
	// persists on disk without a live listener (mimics a crash where the
	// process died without unlinking). net.Listen unlinks on Close, so we
	// re-touch the path afterwards.
	_ = prior.Close()
	if f, ferr := os.Create(sock); ferr != nil {
		t.Fatalf("recreate stale socket inode: %v", ferr)
	} else {
		_ = f.Close()
	}
	deadPID := findDeadPID()
	if werr := os.WriteFile(LeasePath(dir), []byte(strconv.Itoa(deadPID)+"\n"), 0o644); werr != nil {
		t.Fatalf("write stale lease: %v", werr)
	}

	if _, statErr := os.Stat(sock); statErr != nil {
		t.Fatalf("precondition: stale socket must exist: %v", statErr)
	}

	q, stop := startQueue(t)
	defer stop()

	ln, err := NewListener(ListenerConfig{SocketPath: sock, Queue: q, Applier: RejectingApplier})
	if err != nil {
		t.Fatalf("NewListener over stale socket: %v", err)
	}
	defer ln.Close()

	// The bind must have succeeded against a fresh inode.
	if ln.Addr() != sock {
		t.Fatalf("listener addr = %q, want %q", ln.Addr(), sock)
	}
}

// TestStaleSocketUnlinkRefusedWhenOwnerAlive verifies the defence-in-depth
// guard: NewListener must NOT unlink a socket whose lease is held by a LIVE
// owner (the lease — not the inode — is the ownership authority).
func TestStaleSocketUnlinkRefusedWhenOwnerAlive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock := SocketPath(dir)
	if f, err := os.Create(sock); err != nil {
		t.Fatalf("create socket inode: %v", err)
	} else {
		_ = f.Close()
	}
	// Live lease: record OUR pid (this test process is alive).
	if err := os.WriteFile(LeasePath(dir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("write live lease: %v", err)
	}

	q, stop := startQueue(t)
	defer stop()

	if _, err := NewListener(ListenerConfig{SocketPath: sock, Queue: q, Applier: RejectingApplier}); err == nil {
		t.Fatal("NewListener succeeded but a live lease owner should have blocked the unlink")
	}
}

// TestIdleExitAfterTimeout verifies ServeWithIdleTimeout closes the listener
// (returning from Serve) after the configured idle window elapses with zero
// ops. A short injected timeout keeps the test fast.
func TestIdleExitAfterTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock := SocketPath(dir)

	q, stop := startQueue(t)
	defer stop()

	ln, err := NewListener(ListenerConfig{SocketPath: sock, Queue: q, Applier: RejectingApplier})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- ln.ServeWithIdleTimeout(context.Background(), 60*time.Millisecond)
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("idle ServeWithIdleTimeout returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = ln.Close()
		t.Fatal("writer did not idle-exit within 5s")
	}

	// After idle-exit the socket must be unlinked (Close removes it).
	if _, statErr := os.Stat(sock); !os.IsNotExist(statErr) {
		t.Fatalf("socket still present after idle-exit: stat err = %v", statErr)
	}
}

// TestIdleExitNotTriggeredWhileActive verifies a steady stream of ops keeps the
// writer alive past the idle window — idle-exit must not race in-flight work.
func TestIdleExitNotTriggeredWhileActive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock := SocketPath(dir)

	q, stop := startQueue(t)
	defer stop()

	// Accepting applier so SubmitSync succeeds (no-op write).
	applier := func(env Envelope) (writequeue.WriteOp, error) {
		return func(_ context.Context) error { return nil }, nil
	}
	ln, err := NewListener(ListenerConfig{SocketPath: sock, Queue: q, Applier: applier})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- ln.ServeWithIdleTimeout(context.Background(), 50*time.Millisecond)
	}()

	client := NewWriterClientForSocket(sock)
	// Drive ops for ~250ms (5x the idle window). The writer must stay up.
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := client.Submit(ctx, Envelope{OpType: "noop", OpID: ""})
		cancel()
		if err != nil {
			t.Fatalf("submit while active failed (writer exited early?): %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case err := <-serveErr:
		t.Fatalf("writer idle-exited while actively serving ops: %v", err)
	default:
		// Good — still serving.
	}

	// Now stop driving and confirm it eventually idle-exits.
	select {
	case <-serveErr:
	case <-time.After(5 * time.Second):
		_ = ln.Close()
		t.Fatal("writer did not idle-exit after activity stopped")
	}
}

// TestSingleOwnerLeaseRace verifies AcquireLease admits exactly one owner: a
// second acquire while held fails with ErrLeaseHeld, and succeeds once the
// first owner releases.
func TestSingleOwnerLeaseRace(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireLease(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := AcquireLease(dir); err != ErrLeaseHeld {
		t.Fatalf("second acquire while held: err = %v, want ErrLeaseHeld", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := AcquireLease(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = second.Release()
}
