package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shakestzd/wipnote/internal/db/writequeue"
)

// dedupCapacity bounds the in-memory LRU that tracks recently-applied
// op_ids. MVP-2 is in-memory ONLY — durable cross-restart dedup is slice-3
// scope (DEFERRED). 4096 covers a generous burst window for the hot path.
const dedupCapacity = 4096

// Applier turns a received Envelope into a writequeue.WriteOp. It runs on
// the listener goroutine (not the writer goroutine) and must be cheap +
// side-effect-free: the returned closure is what actually mutates the DB,
// and it executes later inside the single-writer queue. An unknown op_type
// returns an error, which the listener surfaces as an error ack (never a
// mis-applied write).
//
// MVP-2 ships no real appliers wired to callers — the listener is
// constructed with whatever Applier its owner provides (serve_child's
// headless mode, or a test). MVP-3 supplies the dbgate derived-op applier.
type Applier func(env Envelope) (writequeue.WriteOp, error)

// RejectingApplier is the MVP-2 default Applier: it rejects every op_type
// with an error so the headless writer-only mode can prove the transport
// (dial, version-check, dedup, ack) end-to-end WITHOUT applying any real
// write. MVP-3 replaces this with the dbgate derived-op applier. Callers
// that wire real ops must supply their own Applier.
func RejectingApplier(env Envelope) (writequeue.WriteOp, error) {
	return nil, fmt.Errorf("no applier registered for op_type %q (MVP-2 transport only)", env.OpType)
}

// Listener accepts versioned write-op envelopes on a per-project Unix
// socket and funnels each accepted op through the SAME single-writer
// mechanism serve_child already uses: an internal/db/writequeue.Queue.
//
// Every op is submitted with Queue.SubmitSync so the ack reflects the
// actual commit outcome (applied/error) rather than mere enqueue. This is
// the structural single-writer guarantee — all writes for a project DB
// serialize through one Queue consumer goroutine owned by one process
// (the lease holder), regardless of how many clients dial the socket.
type Listener struct {
	ln      net.Listener
	queue   *writequeue.Queue
	applier Applier
	sockPath string

	seq atomic.Int64

	dedupMu sync.Mutex
	dedup   *lruSet

	closeOnce sync.Once
	wg        sync.WaitGroup

	// Idle-exit accounting (feat-075c110d lifecycle hardening). lastActivity
	// is the UnixNano of the most recent op submission; inFlight counts ops
	// currently between accept and ack. ServeWithIdleTimeout exits only when
	// BOTH no op has arrived within the idle window AND inFlight == 0, so an
	// in-progress Submit can never race the idle shutdown.
	lastActivity atomic.Int64
	inFlight     atomic.Int64
}

// ListenerConfig configures a Listener. SocketPath is the Unix socket the
// listener binds (caller derives it via SocketPath(projectRoot)). Queue is
// the single-writer queue ops funnel through — the SAME type serve_child
// constructs. Applier maps op_type+payload to a WriteOp.
type ListenerConfig struct {
	SocketPath string
	Queue      *writequeue.Queue
	Applier    Applier
}

// NewListener binds the Unix socket and returns a Listener ready to Serve.
//
// A leftover socket file from a crashed prior owner is unlinked before bind
// (a Unix socket bind fails with EADDRINUSE if the path already exists). The
// lease — not the socket inode — is the ownership authority: the caller is
// expected to already hold the write lease (see AcquireLease) before binding,
// and AcquireLease only returns to a single live owner. So when we reach
// here a leftover socket is necessarily stale and safe to unlink. For defence
// in depth NewListener still confirms no live lease owner before unlinking, so
// a programming error that called NewListener without the lease cannot stomp a
// live writer's socket.
func NewListener(cfg ListenerConfig) (*Listener, error) {
	if cfg.Queue == nil {
		return nil, errors.New("daemon: NewListener requires a non-nil Queue")
	}
	if cfg.Applier == nil {
		return nil, errors.New("daemon: NewListener requires a non-nil Applier")
	}
	if err := unlinkStaleSocket(cfg.SocketPath); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", cfg.SocketPath, err)
	}
	l := &Listener{
		ln:       ln,
		queue:    cfg.Queue,
		applier:  cfg.Applier,
		sockPath: cfg.SocketPath,
		dedup:    newLRUSet(dedupCapacity),
	}
	l.lastActivity.Store(time.Now().UnixNano())
	return l, nil
}

// unlinkStaleSocket removes a leftover socket inode so a fresh bind succeeds
// after an unclean exit. It refuses to unlink when a LIVE lease owner exists
// for the same project (lease co-located with the socket under .wipnote/): the
// lease, not the socket, is the ownership authority, so a live owner's socket
// must never be stomped. A missing socket is a no-op.
func unlinkStaleSocket(sockPath string) error {
	if _, err := os.Lstat(sockPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat socket %s: %w", sockPath, err)
	}
	// The lease lives next to the socket: .wipnote/writer.pid alongside
	// .wipnote/writer.sock. If a live owner still holds it, do not unlink.
	leasePath := filepath.Join(filepath.Dir(sockPath), "writer.pid")
	if leaseOwnerAlive(leasePath) {
		return fmt.Errorf("daemon: refusing to unlink socket %s: a live writer holds the lease", sockPath)
	}
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear stale socket: %w", err)
	}
	return nil
}

// Serve accepts connections until the listener is closed or ctx is done.
// Each connection is handled on its own goroutine; a connection may carry
// multiple envelopes (one ack per envelope) so callers can reuse a dial.
func (l *Listener) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				l.wg.Wait()
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.handleConn(ctx, conn)
		}()
	}
}

// DefaultIdleTimeout is how long a headless writer waits with zero ops before
// it self-terminates (running the graceful-shutdown cleanup). This bounds the
// lifetime of an auto-spawned writer so it never lingers indefinitely holding
// the lease + socket; the next write simply auto-spawns a fresh writer. Five
// minutes is long enough to amortise spawn cost across a burst of hook/CLI
// writes yet short enough that an idle project releases its writer promptly.
const DefaultIdleTimeout = 5 * time.Minute

// idleCheckInterval is how often ServeWithIdleTimeout samples idleness. Kept
// well below DefaultIdleTimeout so the actual exit lands within one interval of
// the configured deadline.
const idleCheckInterval = 15 * time.Second

// ServeWithIdleTimeout runs Serve and additionally cancels (via the returned
// derived context) once the writer has been idle — no op in flight AND no op
// submitted within idleTimeout. It returns when Serve returns. Passing
// idleTimeout <= 0 disables idle-exit (equivalent to plain Serve).
//
// The idle watcher resets implicitly: process() stamps lastActivity on every
// op and increments inFlight for the op's whole lifetime, so a steady stream of
// writes — or even a single slow in-flight commit — keeps the writer alive.
// On idle, the watcher cancels an internal context which closes the listener;
// the CALLER's cleanup (socket + lease removal) then runs exactly as it does
// for a signal-driven shutdown.
func (l *Listener) ServeWithIdleTimeout(ctx context.Context, idleTimeout time.Duration) error {
	if idleTimeout <= 0 {
		return l.Serve(ctx)
	}
	idleCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	interval := idleCheckInterval
	if idleTimeout < interval {
		interval = idleTimeout / 2
		if interval <= 0 {
			interval = idleTimeout
		}
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-idleCtx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if l.isIdle(idleTimeout) {
					cancel() // triggers Serve's listener close
					return
				}
			}
		}
	}()

	err := l.Serve(idleCtx)
	close(done)
	return err
}

// isIdle reports whether no op is in flight and the last op completed more than
// idleTimeout ago. Both conditions are required: a long-running commit keeps
// inFlight > 0 even if lastActivity briefly looks old.
func (l *Listener) isIdle(idleTimeout time.Duration) bool {
	if l.inFlight.Load() != 0 {
		return false
	}
	last := time.Unix(0, l.lastActivity.Load())
	return time.Since(last) >= idleTimeout
}

// handleConn reads newline-delimited envelopes and writes one ack per
// envelope. The connection closes on read EOF or a malformed frame.
func (l *Listener) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	encoder := json.NewEncoder(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			ack := l.process(ctx, line)
			if encErr := encoder.Encode(ack); encErr != nil {
				return // client went away
			}
		}
		if err != nil {
			return // EOF or read error — done with this connection
		}
	}
}

// process decodes one envelope, validates the version, dedups, and funnels
// the op through the writequeue. It always returns an Ack (never panics on
// bad input). A monotonic seq is assigned to every accepted submission.
func (l *Listener) process(ctx context.Context, line []byte) Ack {
	// Idle-exit accounting: an op is "in flight" for the whole decode→ack
	// span. Recording activity on BOTH entry and exit, plus the inFlight
	// counter, guarantees ServeWithIdleTimeout never declares the writer idle
	// while a Submit is mid-process (decode/apply/commit can outlast the idle
	// window for a slow op).
	l.inFlight.Add(1)
	l.lastActivity.Store(time.Now().UnixNano())
	defer func() {
		l.lastActivity.Store(time.Now().UnixNano())
		l.inFlight.Add(-1)
	}()

	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Ack{Status: AckError, Seq: l.seq.Add(1), Error: "malformed envelope: " + err.Error()}
	}
	// Version skew: reject unknown formats loudly rather than risk a
	// mis-applied write (slice-1 decision).
	if env.OpFormatVersion != OpFormatVersion {
		return Ack{
			Status: AckError,
			Seq:    l.seq.Add(1),
			Error:  fmt.Sprintf("unsupported op_format_version %d (daemon speaks %d)", env.OpFormatVersion, OpFormatVersion),
		}
	}

	// In-memory dedup: a previously-applied op_id is acked duplicate
	// without re-running. (Durable dedup across restart is slice-3.)
	if env.OpID != "" {
		l.dedupMu.Lock()
		dup := l.dedup.contains(env.OpID)
		l.dedupMu.Unlock()
		if dup {
			return Ack{Status: AckDuplicate, Seq: l.seq.Add(1)}
		}
	}

	op, err := l.applier(env)
	if err != nil {
		return Ack{Status: AckError, Seq: l.seq.Add(1), Error: "apply " + env.OpType + ": " + err.Error()}
	}

	// Funnel through the SAME single-writer queue serve_child uses.
	// SubmitSync blocks until the writer goroutine commits the op so the
	// ack reflects the real outcome. Queue rejections (full/unavailable)
	// surface as an error ack — the caller (MVP-3/4) decides whether to
	// fall back to direct-open.
	if err := l.queue.SubmitSync(ctx, op); err != nil {
		return Ack{Status: AckError, Seq: l.seq.Add(1), Error: "writequeue: " + err.Error()}
	}

	// Commit succeeded — record op_id so retries dedup, then ack applied.
	seq := l.seq.Add(1)
	if env.OpID != "" {
		l.dedupMu.Lock()
		l.dedup.add(env.OpID)
		l.dedupMu.Unlock()
	}
	return Ack{Status: AckApplied, Seq: seq}
}

// Close stops accepting connections, waits briefly for in-flight handlers,
// and removes the socket file. Idempotent.
func (l *Listener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		err = l.ln.Close()
		// Best-effort socket unlink so the next owner binds cleanly.
		_ = os.Remove(l.sockPath)
	})
	return err
}

// Addr returns the bound socket path (for diagnostics/tests).
func (l *Listener) Addr() string { return l.sockPath }

// drainTimeout is the grace period Close-via-context handlers get; kept
// here as a named constant in case a future graceful-shutdown path wants
// to bound the wait explicitly.
const drainTimeout = 2 * time.Second

var _ = drainTimeout
