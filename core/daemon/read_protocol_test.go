package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db/writequeue"
)

// newTestReadListener starts a Listener with both an Applier and a Reader so
// the read and write halves of the protocol can be exercised on one socket.
func newTestReadListener(t *testing.T, reader Reader) (*Listener, string, func()) {
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
	ln, err := NewListener(ListenerConfig{
		SocketPath: sock,
		Queue:      q,
		Applier:    RejectingApplier,
		Reader:     reader,
	})
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = ln.Serve(ctx) }()
	waitForSocket(t, sock)

	return ln, sock, func() {
		cancel()
		_ = ln.Close()
		q.Stop(time.Second)
	}
}

// stubReader answers workitem.get with a fixed item so protocol behaviour can
// be tested without a canonical corpus.
func stubReader(item WorkItem, found bool) Reader {
	return func(req ReadRequest) (json.RawMessage, *CacheStats, error) {
		if req.ReadOp != ReadOpWorkItemGet {
			return nil, nil, ErrUnknownReadOp
		}
		body, err := json.Marshal(WorkItemGetResult{Found: found, Item: item})
		return body, &CacheStats{Hits: 1}, err
	}
}

func TestReadPingRoundTrip(t *testing.T) {
	_, sock, cleanup := newTestReadListener(t, nil)
	defer cleanup()

	res, err := NewReadClientForSocket(sock).Ping(context.Background())
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if res.ReadFormatVersion != ReadFormatVersion {
		t.Fatalf("ping read_format_version = %d, want %d", res.ReadFormatVersion, ReadFormatVersion)
	}
	if res.PID != os.Getpid() {
		t.Fatalf("ping pid = %d, want %d", res.PID, os.Getpid())
	}
}

func TestWorkItemGetRoundTrip(t *testing.T) {
	want := WorkItem{ID: "feat-abcdef01", Type: "feature", Status: "in-progress", Title: "T", TrackID: "trk-1"}
	_, sock, cleanup := newTestReadListener(t, stubReader(want, true))
	defer cleanup()

	got, stats, err := NewReadClientForSocket(sock).GetWorkItem(context.Background(), want.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Found || got.Item != want {
		t.Fatalf("get = %+v, want found item %+v", got, want)
	}
	if stats == nil || stats.Hits != 1 {
		t.Fatalf("cache stats = %+v, want Hits=1", stats)
	}
}

// TestReadFrameDoesNotDisturbWritePath proves the two frame kinds are
// discriminated: the same connection must answer an Envelope with an Ack and a
// ReadRequest with a ReadResponse.
func TestReadFrameDoesNotDisturbWritePath(t *testing.T) {
	_, sock, cleanup := newTestReadListener(t, stubReader(WorkItem{ID: "feat-1"}, true))
	defer cleanup()

	// Write frame still behaves exactly as before (RejectingApplier ⇒ error ack).
	ack, err := NewWriterClientForSocket(sock).Submit(context.Background(), Envelope{
		OpID: "op-1", OpType: "whatever",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ack.Status != AckError {
		t.Fatalf("write ack status = %q, want %q", ack.Status, AckError)
	}

	// Read frame on the same socket gets a read response.
	if _, _, err := NewReadClientForSocket(sock).GetWorkItem(context.Background(), "feat-1"); err != nil {
		t.Fatalf("read after write: %v", err)
	}
}

func TestReadVersionSkewIsUnsupportedNotEmpty(t *testing.T) {
	_, sock, cleanup := newTestReadListener(t, stubReader(WorkItem{}, true))
	defer cleanup()

	resp := rawReadRoundTrip(t, sock, ReadRequest{
		ReadOp:            ReadOpWorkItemGet,
		ReadFormatVersion: ReadFormatVersion + 1,
	})
	if resp.ReadStatus != ReadStatusUnsupported {
		t.Fatalf("status = %q, want %q", resp.ReadStatus, ReadStatusUnsupported)
	}
	if resp.Result != nil {
		t.Fatalf("version-skew reply carried a result: %s", resp.Result)
	}
}

// TestNilReaderIsUnsupportedNotEmpty is the distinction a caller depends on: a
// write-only daemon must say "I do not serve this read", never "no rows".
func TestNilReaderIsUnsupportedNotEmpty(t *testing.T) {
	_, sock, cleanup := newTestReadListener(t, nil)
	defer cleanup()

	_, _, err := NewReadClientForSocket(sock).GetWorkItem(context.Background(), "feat-1")
	if !errors.Is(err, ErrReadUnsupported) {
		t.Fatalf("err = %v, want ErrReadUnsupported", err)
	}
}

func TestUnknownReadOpIsUnsupported(t *testing.T) {
	_, sock, cleanup := newTestReadListener(t, stubReader(WorkItem{}, false))
	defer cleanup()

	_, _, err := NewReadClientForSocket(sock).Read(context.Background(), ReadOpWorkItemList, nil)
	if !errors.Is(err, ErrReadUnsupported) {
		t.Fatalf("err = %v, want ErrReadUnsupported", err)
	}
}

// TestUnreachableDaemonIsBoundedAndLoud asserts BOTH halves of the failure
// policy: the retry is bounded in time, and the outcome is a distinguishable
// error rather than a plausible-looking empty answer.
func TestUnreachableDaemonIsBoundedAndLoud(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nothing-here.sock")

	start := time.Now()
	_, _, err := NewReadClientForSocket(sock).GetWorkItem(context.Background(), "feat-1")
	elapsed := time.Since(start)

	if !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("err = %v, want ErrDaemonUnreachable", err)
	}
	budget := time.Duration(readRetryAttempts) * (readDialTimeout + readRetryBackoff)
	if elapsed > budget+time.Second {
		t.Fatalf("retry was not bounded: took %s, budget %s", elapsed, budget)
	}
}

// TestOlderDaemonReplyIsUnsupported simulates a daemon built before this
// protocol: it decodes the read frame as a write Envelope and replies with an
// Ack. The reply has no read_status, and that absence must be read as "does
// not speak the read protocol" — never as a successful empty result.
func TestOlderDaemonReplyIsUnsupported(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "old.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		if _, err := conn.Read(buf); err != nil {
			return
		}
		// Exactly what an older daemon's version-skew path emits.
		_ = json.NewEncoder(conn).Encode(Ack{
			Status: AckError,
			Seq:    1,
			Error:  "unsupported op_format_version 0 (daemon speaks 2)",
		})
	}()

	_, _, err = NewReadClientForSocket(sock).GetWorkItem(context.Background(), "feat-1")
	if !errors.Is(err, ErrReadUnsupported) {
		t.Fatalf("err = %v, want ErrReadUnsupported", err)
	}
}

// TestAttachSuppressesIdleExit covers the guarantee's lifetime: a daemon a
// launcher attached to must not idle-exit under the session it was promised to.
func TestAttachSuppressesIdleExit(t *testing.T) {
	ln, sock, cleanup := newTestReadListener(t, nil)
	defer cleanup()

	// With no attachment, an already-elapsed idle window reports idle.
	ln.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	if !ln.isIdle(time.Minute) {
		t.Fatal("listener with no attachment should be idle")
	}

	if _, err := NewReadClientForSocket(sock).Attach(context.Background(), os.Getpid()); err != nil {
		t.Fatalf("attach: %v", err)
	}
	ln.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	if ln.isIdle(time.Minute) {
		t.Fatal("listener with a live attached pid must never report idle")
	}
}

// TestAttachedDeadPIDIsPruned proves the attachment is pid-scoped rather than
// open-ended: once the launcher that claimed the daemon is gone, ordinary
// idle-exit resumes so the daemon does not live forever.
func TestAttachedDeadPIDIsPruned(t *testing.T) {
	ln, sock, cleanup := newTestReadListener(t, nil)
	defer cleanup()

	dead := spawnAndReapPID(t)
	if _, err := NewReadClientForSocket(sock).Attach(context.Background(), dead); err != nil {
		t.Fatalf("attach: %v", err)
	}
	ln.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	if !ln.isIdle(time.Minute) {
		t.Fatal("attachment to a dead pid must not keep the daemon alive")
	}
}

// TestReadCountsAsActivity guards the interaction that would otherwise make the
// guarantee self-defeating: a session whose only daemon traffic is hook reads
// must keep the daemon alive.
func TestReadCountsAsActivity(t *testing.T) {
	ln, sock, cleanup := newTestReadListener(t, stubReader(WorkItem{ID: "feat-1"}, true))
	defer cleanup()

	ln.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	if !ln.isIdle(time.Minute) {
		t.Fatal("precondition: listener should look idle before the read")
	}
	if _, _, err := NewReadClientForSocket(sock).GetWorkItem(context.Background(), "feat-1"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if ln.isIdle(time.Minute) {
		t.Fatal("a served read did not count as activity")
	}
}

// rawReadRoundTrip sends a hand-built ReadRequest and returns the raw response,
// bypassing ReadClient so version-skew handling can be observed on the wire.
func rawReadRoundTrip(t *testing.T, sock string, req ReadRequest) ReadResponse {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp ReadResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// spawnAndReapPID returns the pid of a process that has exited and been reaped,
// so kill(pid, 0) reports ESRCH.
func spawnAndReapPID(t *testing.T) int {
	t.Helper()
	proc, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 0"}, &os.ProcAttr{})
	if err != nil {
		t.Skipf("cannot spawn helper process: %v", err)
	}
	if _, err := proc.Wait(); err != nil {
		t.Skipf("cannot reap helper process: %v", err)
	}
	return proc.Pid
}
