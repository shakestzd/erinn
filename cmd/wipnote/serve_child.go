package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shakestzd/wipnote/internal/childproc"
	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/daemon/apply"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/observe/otel/indexer"
	otelreceiver "github.com/shakestzd/wipnote/observe/otel/receiver"
	"github.com/shakestzd/wipnote/observe/otel/retention"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
	"github.com/shakestzd/wipnote/internal/registry"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

// writerService is the dashboard's instance of the slice-6 writer
// transport. It is constructed once per `wipnote serve` child process
// and shared by every in-process producer (the NDJSON indexer today;
// the OTLP HTTP receiver and sub-agent auto-ingest paths follow in
// slices 7 and beyond).
//
// Holding both the queue and the underlying Writer here lets the
// collector-status handler expose live depth + state without reaching
// into producer-local state. Nil-safe: an unset writerService means
// the dashboard is running without an index-update channel (e.g.
// during unit tests of buildSingleProjectMux that pass database=nil).
var writerService struct {
	queue *writequeue.Queue
	sink  *sqls.QueuedSink
}

// dashboardReadPoolMaxConns bounds the dashboard mux's read-only SQLite
// connection pool. bug-74a7bda7: an uncapped pool lets a request burst open
// arbitrarily many SHARED-lock-holding connections, which under DELETE
// journal mode serialise hard against the single writer and starve the
// completion path. 12 sits well above steady dashboard concurrency while
// bounding worst-case lock pressure on every filesystem.
const dashboardReadPoolMaxConns = 12

// serveChildCmd is the hidden internal subcommand the parent wipnote
// server spawns for each project in multi-project mode. It is NOT intended
// for direct invocation — end users run `wipnote serve`, which forks this
// command as a child process per project.
//
// The child binds to an ephemeral port (--port 0), prints exactly one
// handshake line to stdout so the parent supervisor can discover the port,
// and then redirects stdout/stderr to a per-project log file before the
// HTTP server begins accepting traffic. This guarantees the supervisor's
// scanner never sees stray startup logs between the handshake and the
// supervisor's stdout-drain goroutine attaching.
func serveChildCmd() *cobra.Command {
	var port int
	var headless bool
	var serveManaged bool
	cmd := &cobra.Command{
		Use:    "_serve-child",
		Hidden: true,
		Short:  "Internal: single-project HTTP server spawned by parent (do not invoke directly)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if headless {
				// feat-075c110d INCREMENT 2: writer-only daemon — acquire the
				// per-project write lease, open the SOLE writable DB + writequeue,
				// run the per-project background maintenance (auto-ingest,
				// indexer, ai-title backfill, retention) AND the daemon socket
				// listener. No HTTP mux. --serve-managed disables idle-exit so a
				// writer started by `wipnote serve` persists for serve's lifetime.
				return runWriterOnly(serveManaged)
			}
			return runServeChild(port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "TCP port (0 = ephemeral)")
	cmd.Flags().BoolVar(&headless, "headless", false, "Writer-only mode: run the daemon socket listener + writequeue + background maintenance without the HTTP dashboard (feat-075c110d)")
	cmd.Flags().BoolVar(&serveManaged, "serve-managed", false, "Headless writer is managed by a parent `wipnote serve` child: disable idle-exit so it persists for serve's lifetime (feat-075c110d increment 2)")
	return cmd
}

// runWriterOnly is the headless writer-only serve_child path (plan-bb91616a
// slice-2, feat-075c110d MVP-2). It is ADDITIVE: it shares no code path with
// runServeChild's HTTP setup and changes no existing write path.
//
// Lifecycle:
//  1. Acquire the O_EXCL single-owner lease. A racing loser gets
//     daemon.ErrLeaseHeld and exits 0 (the existing owner serves the socket).
//  2. Open the writable DB (same dbpkg.Open + receiver.NewWriter the default
//     serve_child uses) and start a writequeue.Queue — the SAME single-writer
//     mechanism. Every op submitted over the socket funnels through it.
//  3. Bind the per-project Unix socket and serve until interrupted.
//
// MVP-2 wires NO real op appliers (callers cut over in MVP-3/4); the listener
// uses daemon.RejectingApplier so any op_type is acked error rather than
// mis-applied. This proves the transport end-to-end without touching dbgate.
func runWriterOnly(serveManaged bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return fmt.Errorf("locate .wipnote: %w", err)
	}
	projectRoot := filepath.Dir(wipnoteDir)

	lease, err := daemon.AcquireLease(projectRoot)
	if err != nil {
		if err == daemon.ErrLeaseHeld {
			// Another process already owns the writer — nothing to do.
			fmt.Fprintln(os.Stderr, "wipnote: writer already owned; exiting")
			return nil
		}
		return fmt.Errorf("acquire writer lease: %w", err)
	}
	defer lease.Release()

	dbPath, err := storage.CanonicalDBPath(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return fmt.Errorf("ensure db dir: %w", err)
	}
	// dbpkg.Open runs migrations / ensures schema exists, exactly as the
	// default path did in runServeChild, before the writequeue worker (or the
	// background maintenance loops) touches the DB. The daemon now OWNS this
	// single writable handle for the whole process.
	//
	// SINGLE-WRITER CONSOLIDATION (feat-075c110d): this is the ONE and ONLY
	// writable SQLite handle the daemon opens. We cap it at MaxOpenConns=1 so
	// the database/sql pool itself guarantees exactly ONE physical connection
	// — and therefore that at most one BEGIN IMMEDIATE transaction is ever in
	// flight across EVERY write path in the process:
	//
	//   - the writequeue applier (socket-delivered derived ops),
	//   - the OTel sink (otel_signals inserts via the receiver Writer),
	//   - the indexer (its direct writes + orphan-filter SELECTs),
	//   - auto-ingest, the one-time ai-title backfill, and retention.
	//
	// Previously the daemon opened TWO writable pools to the same file —
	// `dbpkg.Open` here AND a second pool inside receiver.NewWriter — so two
	// connections could each issue BEGIN IMMEDIATE concurrently, producing the
	// "database is locked (5) (SQLITE_BUSY)" thrash that kept the indexer
	// failing and left otel_signals empty. Sharing this single handle (passed
	// to receiver.NewWriterFromDB below) and capping it to one connection
	// removes the second pool entirely.
	//
	// Read amplification note: collapsing to a single connection serializes
	// the daemon's same-process SELECTs (orphan-filter, prompt-ID bridge)
	// behind writes. That is acceptable here — the daemon is write-dominated,
	// the SELECTs are tiny and infrequent, and the HTTP dashboard reads run in
	// a SEPARATE read-only handle inside serve_child (not this pool). No user-
	// facing read path shares this connection.
	writeDB, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db (writable, schema): %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	defer writeDB.Close()

	// The OTel signals Writer BORROWS the single writable handle above rather
	// than opening its own pool (feat-075c110d). With MaxOpenConns=1 it cannot
	// pin a lifetime connection (that would starve the applier + maintenance),
	// so NewWriterFromDB acquires the pool connection per-batch and releases it
	// — the pool serialization is the single-writer guarantee.
	writer, err := otelreceiver.NewWriterFromDB(writeDB)
	if err != nil {
		return fmt.Errorf("writer service init: %w", err)
	}
	defer writer.Close()

	q := writequeue.New(writequeue.Config{
		Capacity: writequeue.DefaultCapacity,
		OnError:  func(err error) { log.Printf("writequeue: op error: %v", err) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := q.Start(ctx); err != nil {
		return fmt.Errorf("writer queue start: %w", err)
	}
	defer q.Stop(drainGrace)

	// Graceful shutdown (feat-075c110d lifecycle hardening): trap SIGTERM/
	// SIGINT and cancel the serve context. Cancellation closes the listener
	// (Serve returns), stops the maintenance loops, and fires the deferred
	// ln.Close() — unlinking .wipnote/writer.sock — and the deferred
	// lease.Release() — removing .wipnote/writer.pid. Draining of in-flight
	// ops happens in the deferred q.Stop(drainGrace) above. Without this the
	// headless writer left both the stale socket and the lease behind on
	// SIGTERM, blocking the dashboard's serve_child and the next writer.
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigC)
	go func() {
		select {
		case <-sigC:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Per-project background maintenance (MOVED from runServeChild in
	// feat-075c110d increment 2). These are the writable workloads that don't
	// fit the socket/writequeue op API and previously ran inside the HTTP
	// serve_child against its own writable handle — the source of the
	// serve_child↔writer contention. They now run HERE, against the daemon's
	// single writable handle / queue, so the HTTP serve_child can be strictly
	// read-only.
	startWriterMaintenance(ctx, writeDB, wipnoteDir, q, writer)

	// Wire the real derived-op Applier. writer.DB() is the SAME single
	// writable handle (writeDB, MaxOpenConns=1) the OTel sink, indexer, and
	// maintenance loops use, so all socket-delivered writes serialize on the
	// one connection — the structural single-writer invariant.
	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: daemon.SocketPath(projectRoot),
		Queue:      q,
		Applier:    apply.NewApplier(writer.DB()),
		OwnerPID:   os.Getpid(),
	})
	if err != nil {
		return fmt.Errorf("bind writer socket: %w", err)
	}
	defer ln.Close()

	// Readiness signal: the socket is bound and the queue is running. A parent
	// `wipnote serve` child dials this socket to confirm the writer is up
	// before serving HTTP read traffic.
	fmt.Fprintf(os.Stderr, "wipnote: writer-only daemon ready socket=%s pid=%d serve_managed=%t\n", ln.Addr(), os.Getpid(), serveManaged)

	// Idle-exit resolution (feat-075c110d increment 2): a CLI/hook auto-spawned
	// writer is detached and idle-exits after writerIdleTimeout so it never
	// lingers. But a SERVE-MANAGED writer must persist for serve's whole
	// lifetime — serve opens read-only and depends on this writer for every
	// write, and serve reaps it on shutdown (Pdeathsig + Supervisor SIGTERM).
	// So when serveManaged is set we DISABLE idle-exit (idleTimeout <= 0 ⇒
	// plain Serve) and rely entirely on serve's managed lifecycle to stop it.
	idle := writerIdleTimeout()
	if serveManaged {
		idle = 0
	}
	return ln.ServeWithIdleTimeout(ctx, idle)
}

// startWriterMaintenance launches the per-project background maintenance loops
// that legitimately write to the project DB but do not fit the socket/
// writequeue op API. It runs INSIDE the headless writer daemon (feat-075c110d
// increment 2) so these writes share the daemon's single writable handle/queue
// rather than opening a second writer inside the HTTP serve_child.
//
// Loops started:
//   - auto-ingest (every 60s) + a one-time ai-title backfill after the first
//     ingest cycle, both against writeDB.
//   - the NDJSON→SQLite indexer: SELECTs run against writeDB (the daemon holds
//     no separate read-only handle), and its writes route through the same
//     writequeue (WithQueue) / OTel sink the socket ops use.
//   - retention archival (startup + every 24h) against writeDB.
//
// ctx cancellation (from SIGTERM/SIGINT or idle-exit) stops every loop.
func startWriterMaintenance(ctx context.Context, writeDB *sql.DB, wipnoteDir string, q *writequeue.Queue, writer *otelreceiver.Writer) {
	// Auto-ingest + one-time ai-title backfill. These issue INSERT/UPDATE/
	// DELETE on sessions/messages/tool_calls directly on the writable handle.
	go autoIngestLoop(writeDB, wipnoteDir, func() {
		startAITitleBackfill(ctx, writeDB, wipnoteDir)
	})

	// NDJSON→SQLite indexer. Routes every SignalSink batch through the daemon's
	// writequeue (via the OTel sink) and its prompt-ID bridge through the same
	// queue (WithQueue). The daemon holds no separate read-only handle, so the
	// orphan-filter SELECTs use the writable handle directly here — acceptable
	// because they execute in the SAME process as the single writer (no cross-
	// process SHARED↔RESERVED lock escalation, which was the bug-272c5e34/
	// bug-74a7bda7 contention root cause).
	snk := sqls.NewQueued(q, writer)
	idxr := indexer.New(wipnoteDir, snk).
		WithDB(writeDB).
		WithWriteDB(writeDB).
		WithQueue(q)
	go idxr.Start(ctx)

	// Retention archival: archive sessions older than the retention window at
	// startup and every 24h.
	retention.StartLoop(ctx, writeDB, wipnoteDir)
}

// writerIdleTimeout resolves the headless writer's idle-exit window. It honours
// WIPNOTE_WRITER_IDLE_TIMEOUT (e.g. "200ms", "10m") for tests/operators and
// otherwise falls back to daemon.DefaultIdleTimeout.
func writerIdleTimeout() time.Duration {
	if v := os.Getenv("WIPNOTE_WRITER_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return daemon.DefaultIdleTimeout
}

// drainGrace bounds how long the headless writer waits for in-flight ops to
// commit on shutdown.
const drainGrace = 2 * time.Second

// runServeChild opens the project DB, builds the single-project mux, binds
// the listener, prints the handshake, redirects stdio, and serves HTTP.
func runServeChild(port int) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return fmt.Errorf("locate .wipnote: %w", err)
	}

	projectRoot := filepath.Dir(wipnoteDir)
	dbPath, err := storage.CanonicalDBPath(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return fmt.Errorf("ensure db dir: %w", err)
	}

	// feat-075c110d increment 2: the HTTP serve_child is now STRICTLY
	// read-only. The headless writer daemon (runWriterOnly) is the SOLE owner
	// of the per-project writable workload — the writequeue AND all background
	// maintenance (auto-ingest, indexer, ai-title backfill, retention). With
	// serve running there is exactly ONE writable SQLite handle for the
	// project: the daemon's. serve_child no longer opens a writable handle and
	// therefore can never contend with the writer.
	//
	// OpenReadOnlyMigrated bootstraps the schema (a brief writable Open that
	// runs migrations, then is closed immediately) and returns a read-only
	// handle. mode=ro never creates a file and never migrates, so the bootstrap
	// is still required for a fresh/schema-behind workspace; it does NOT leave
	// a writable handle open. Once the writer daemon is up it owns migrations,
	// but serve may start first, so we keep the bootstrap here.
	database, err := dbpkg.OpenReadOnlyMigrated(dbPath)
	if err != nil {
		return fmt.Errorf("open db (read-only mux): %w", err)
	}
	// Cap the dashboard read pool so a burst of concurrent HTTP requests
	// cannot open an unbounded number of SQLite connections (each of which
	// takes a SHARED lock and, under DELETE journal mode, serialises against
	// the single writer). 12 is comfortably above the dashboard's steady
	// concurrency while bounding worst-case lock pressure.
	database.SetMaxOpenConns(dashboardReadPoolMaxConns)
	// The read-only handle lives for the process lifetime; no defer Close —
	// Serve blocks. The dashboard performs no in-process writes: any write the
	// HTTP layer still needs goes via the writer daemon over its socket.

	// Ensure the per-project writer daemon is running and reaped on shutdown
	// (feat-075c110d increment 2). If a live writer lease already exists
	// (CLI/hook auto-spawn, or a prior serve), we reuse it; otherwise we start
	// the headless writer as a MANAGED child (Setpgid + Pdeathsig) so it is
	// SIGTERMed when this serve_child exits. The O_EXCL lease guarantees a
	// single writer regardless of who starts it.
	stopWriter := ensureWriterDaemon(projectRoot)
	defer stopWriter()

	// Dashboard interactive write routes (manual session-ingest button; plan
	// feedback/finalize/delete/chat) genuinely mutate the DB and capture their
	// *sql.DB at mux-build time. These are LOW-FREQUENCY, user-triggered writes
	// — NOT the high-frequency background maintenance that increment 2 moved
	// into the daemon (auto-ingest/indexer/ai-title/retention, the real
	// contention source). They cannot yet be expressed as daemon op_types:
	// routing them would require expanding the apply dispatch, which is
	// explicitly OUT OF SCOPE for this increment (no wire-protocol / new-op
	// changes). A dedicated writable handle is opened here for exactly these
	// mutation endpoints (plan feedback POST, finalize, delete, chat, and
	// manual session ingest). Read routes use the read-only `database` handle.
	//
	// bug-528478ad: previously this passed `database` (read-only, query_only=ON)
	// for BOTH arguments, so every dashboard Approve/Finalize click returned 500
	// "attempt to write a readonly database". The writable handle is capped at
	// MaxOpenConns=1 so it serialises with the writer daemon; low-frequency
	// user-triggered writes at dashboard speed will never contend in practice.
	dashWriteDB, err := dbpkg.OpenWritable(dbPath)
	if err != nil {
		return fmt.Errorf("open db (dashboard write handle): %w", err)
	}
	dashWriteDB.SetMaxOpenConns(1)
	dashWriteDB.SetMaxIdleConns(1)
	defer dashWriteDB.Close()

	mux := buildSingleProjectMux(database, dashWriteDB, wipnoteDir)

	// /api/collector-status — diagnostic surface. The writer queue now lives in
	// the daemon, so writerService.queue is nil here; readWriterServiceStatus
	// is nil-safe and still reports the process-level BUSY counters.
	mux.Handle("/api/collector-status", collectorWriterStatusHandler())

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	assigned := ln.Addr().(*net.TCPAddr).Port

	// Handshake: MUST be the first output of this process. The parent
	// supervisor (internal/childproc, slice 2) reads exactly one line
	// matching `wipnote-serve-ready port=<N> pid=<P>` with a 5s deadline.
	// Any prior stdout write — log line, deprecation warning, anything —
	// corrupts the scanner. Do not add prints above this line.
	if _, err := fmt.Printf("wipnote-serve-ready port=%d pid=%d\n", assigned, os.Getpid()); err != nil {
		return fmt.Errorf("write handshake: %w", err)
	}
	if err := os.Stdout.Sync(); err != nil {
		// Non-fatal: the parent has already read the line via its pipe.
		_ = err
	}

	// Redirect stdout/stderr to a per-project log file so subsequent logs
	// (auto-ingest, handler errors, etc.) don't leak through the supervisor's
	// drain goroutine to the parent's terminal.
	projectID := registry.ComputeID(filepath.Dir(wipnoteDir))
	logsDir := filepath.Join(wipnoteDir, "logs")
	_ = os.MkdirAll(logsDir, 0o755)
	logPath := filepath.Join(logsDir, fmt.Sprintf("serve-%s.log", projectID))
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		os.Stdout = f
		os.Stderr = f
	}

	// Background maintenance (auto-ingest, ai-title backfill, indexer,
	// retention) is NO LONGER started here — feat-075c110d increment 2 moved
	// it into the headless writer daemon (runWriterOnly→startWriterMaintenance)
	// so it runs against the daemon's single writable handle. serve_child is
	// read-only.

	// Graceful shutdown: trap SIGTERM/SIGINT so the deferred stopWriter() (which
	// reaps a serve-managed writer) actually runs. The childproc supervisor
	// SIGTERMs us on serve-parent shutdown / idle-reap; without this handler the
	// process would die before the defer fires and a serve-managed writer would
	// be orphaned (Pdeathsig is the kernel-level backstop, but we reap
	// explicitly for portability and promptness).
	srv := &http.Server{Handler: mux}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ln) }()

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigC)
	select {
	case err := <-srvErr:
		return err
	case <-sigC:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		// stopWriter() runs via defer after we return, reaping the managed writer.
		return nil
	}
}

// ensureWriterDaemon makes sure a per-project writer daemon is running and
// returns a stop function that reaps it IF this serve_child started it.
//
// feat-075c110d increment 2 — serve→writer management:
//   - If a live writer lease already exists (a CLI/hook auto-spawned writer, or
//     a writer from a prior serve), we DO NOT start another one. The O_EXCL
//     lease is the single-owner authority; we simply use the existing writer
//     and the returned stop func is a no-op (we don't own its lifecycle).
//   - Otherwise we spawn the headless writer as a MANAGED child with
//     Setpgid + Pdeathsig (the MVP-1 spawn attrs) and --serve-managed (which
//     disables the writer's idle-exit). The returned stop func SIGTERMs that
//     child so the writer is reaped on serve shutdown.
//
// The lease check is racy by nature (another writer could appear between the
// check and our spawn), but that race is benign: the writer we fork calls
// AcquireLease itself and a loser exits 0 immediately, leaving the existing
// owner serving. So we never end up with two writers regardless of who starts.
func ensureWriterDaemon(projectRoot string) func() {
	if daemon.LeaseOwnerAlive(projectRoot) {
		// A live writer already owns the lease — reuse it, own nothing.
		fmt.Fprintf(os.Stderr, "wipnote: serve_child reusing existing writer daemon (lease held)\n")
		return func() {}
	}

	selfExe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wipnote: serve_child cannot resolve self exe to spawn writer: %v\n", err)
		return func() {}
	}

	logDir := filepath.Join(projectRoot, ".wipnote", "logs")
	_ = os.MkdirAll(logDir, 0o755)
	var out *os.File
	if f, ferr := os.OpenFile(filepath.Join(logDir, "writer.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
		out = f
	}

	cmd := exec.Command(selfExe, "--project-dir", projectRoot,
		"_serve-child", "--headless", "--serve-managed")
	cmd.Dir = projectRoot
	cmd.Stdin = nil
	if out != nil {
		cmd.Stdout = out
		cmd.Stderr = out
	}
	cmd.Env = os.Environ()
	// Managed-child spawn attrs (reuse the MVP-1 childproc attrs): Setpgid
	// isolates the writer in its own process group; Pdeathsig (Linux) delivers
	// SIGTERM to the writer the moment this serve_child dies — kernel-level
	// orphan prevention so a serve-managed writer never outlives its serve.
	cmd.SysProcAttr = childproc.WriterSysProcAttr()

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wipnote: serve_child failed to spawn writer daemon: %v\n", err)
		if out != nil {
			_ = out.Close()
		}
		return func() {}
	}
	if out != nil {
		_ = out.Close() // child holds its own fd after Start
	}
	fmt.Fprintf(os.Stderr, "wipnote: serve_child started managed writer daemon pid=%d\n", cmd.Process.Pid)

	stopped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(stopped) }()

	return func() {
		// SIGTERM the managed writer and wait briefly for it to run its
		// deferred socket+lease cleanup, escalating to SIGKILL if it lingers.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-stopped
		}
	}
}
