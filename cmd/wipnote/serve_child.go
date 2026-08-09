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

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/daemon/apply"
	"github.com/shakestzd/wipnote/core/daemon/readsrv"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/internal/childproc"
	"github.com/shakestzd/wipnote/internal/registry"
	"github.com/shakestzd/wipnote/observe/otel/indexer"
	otelreceiver "github.com/shakestzd/wipnote/observe/otel/receiver"
	"github.com/shakestzd/wipnote/observe/otel/retention"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
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

	writeDB, err := dbpkg.OpenEphemeralProjection()
	if err != nil {
		return fmt.Errorf("open ephemeral compatibility db: %w", err)
	}
	if err := hydrateCompatibilityDB(writeDB, wipnoteDir); err != nil {
		writeDB.Close()
		return fmt.Errorf("hydrate compatibility db: %w", err)
	}
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
	startWriterMaintenance(ctx, wipnoteDir)

	// Wire the real derived-op Applier. writer.DB() is the SAME single
	// writable handle (writeDB, MaxOpenConns=1) the OTel sink, indexer, and
	// maintenance loops use, so all socket-delivered writes serialize on the
	// one connection — the structural single-writer invariant.
	// Wire the read side (feat-f6759e37). The Reader answers work-item queries
	// from CANONICAL state — the .wipnote HTML files — not from writeDB, so it
	// shares nothing with the write path above and cannot queue behind it. That
	// is the whole point: hooks are fresh processes that cannot afford the
	// canonical parse, and this daemon is where that parse gets amortised, so
	// hooks stop needing the derived index to answer work-item questions.
	readCache := readsrv.NewCache(wipnoteDir)

	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: daemon.SocketPath(projectRoot),
		Queue:      q,
		Applier:    apply.NewApplier(writer.DB()),
		Reader:     readsrv.Reader(readCache),
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

// retentionSweepInterval is how often the daemon runs the retention pass
// (archive completed sessions, sweep stale NDJSON). It matches the cadence
// retention.StartLoop used before this function drove the ticker itself.
const retentionSweepInterval = 24 * time.Hour

// startWriterMaintenance launches the per-project background maintenance that
// does not fit the socket/writequeue op API. It runs INSIDE the headless writer
// daemon (feat-075c110d increment 2), the one long-lived per-project process
// and therefore the only place a periodic pass can live.
//
// # The test every loop had to pass
//
// These loops were written when the project database was a file on disk shared
// by every wipnote process. The daemon wrote, the dashboard read, and a row the
// daemon inserted was a row the dashboard could serve. That is no longer true:
// each process builds its OWN in-memory projection (feat-fc3cc9e0), hydrated
// from canonical files by hydrateCompatibilityDB.
//
// That breaks loops in two different ways, and the second is the dangerous one:
//
//  1. A loop whose entire OUTPUT was a DB row now produces nothing.
//  2. A loop whose GUARD reads a table the projection does not hydrate does not
//     become cautious — it becomes reckless. An empty table reads as "nothing
//     to protect", never as "I cannot tell". Every such guard fails OPEN.
//
// So the question for each loop is not "does it still run" but "is its output
// durable, and is every table its guards read actually hydrated". The hydration
// set is exactly what hydrateCompatibilityDB writes: tracks, features/bugs/
// spikes, archived work items, claim_episodes, sessions (from the ledger, nine
// columns), graph_edges, plans, gate_records, recaps. Notably NOT hydrated:
// claims, agent_events, messages, tool_calls.
//
// # Running (2)
//
//   - retention (24h) — reads sessions.status/completed_at, which the ledger
//     hydration does populate, and writes NOTHING to the database. Destructive
//     by intent: it tar.gz's completed sessions into .wipnote/archive/ and then
//     os.RemoveAll's the live directory. Its own interlock is the .index-offset
//     progress marker (retention.go:267), which is missing or behind whenever
//     the dashboard indexer has not caught up — and a missing marker reads as
//     "not caught up", so it defers rather than deleting un-indexed telemetry.
//   - reconcile drain (5m) — git-commits done-but-uncommitted work-item
//     artifacts (feat-c08d1ba1 slice-6 moved this off the Stop hook's ~5.45s
//     hot path). It needs no projection at all: hooks.Reconcile-
//     DoneButUncommittedForProject ignores its *sql.DB (session_end.go:733,
//     `_ = database`) and works entirely from canonical files plus git.
//
// # Off, with cause (5)
//
// None of these had a production caller before this function was restored, so
// leaving them off returns to the status quo rather than losing anything.
//
//   - EMPTY-SPIKE WORKTREE GC — OFF. The feature is opt-in and DEFAULT OFF
//     (worktree.LoadCleanupConfig sets EmptySpikeWorktreeCleanup: false), so
//     wiring it would change nothing for anyone today. It is unwired anyway
//     because its LIVENESS GUARD IS INERT, and the previous comment here
//     claimed the opposite — it called the sweep "liveness-aware", which is the
//     sentence a future reader would trust when deciding whether turning the
//     feature ON is safe. It is not.
//     workItemHasLiveHeartbeat (config.go:198) joins the `claims` table.
//     `claims` has no hydration path, and an audit found no production writer
//     for it at all, so the query returns ErrNoRows and the guard returns false
//     for EVERY work item — it cannot protect anything, in any configuration.
//     With it inert, all that stands between a live agent's worktree and
//     destruction is an mtime TTL, a git-lock check and a clean-tree check —
//     which an agent idling on a clean tree satisfies — after which the sweep
//     calls `git worktree remove` and shells out to `wipnote <type> complete`.
//     Tracked as bug-0b322d67; rewire once liveness is answerable from
//     canonical data. claim_episodes is hydrated but carries no
//     last_heartbeat_at, so it is not a substitute: "episode still open" is not
//     "heartbeat within N minutes".
//   - ORPHAN DRAIN — OFF. hooks.SweepOrphanedEventsForProject selects from
//     agent_events (event_repo.go:392), which the projection never hydrates, so
//     it finds zero orphans on every pass. This one fails CLOSED — an empty
//     table means it sweeps nothing — so it is harmless, but it would pay a
//     full project hydrate every five minutes to guarantee finding nothing.
//   - REAPER — OFF. Its heartbeat guard is dead the same way the sweep's is:
//     db.SessionLivenessByHeartbeat reads MAX(last_heartbeat_at) FROM claims
//     (session_repo.go:586), so heartbeatStale is permanently true. Unlike the
//     sweep it is NOT dangerous, because SessionReapEligible ANDs that with
//     !IsSessionProcessAlive (session_liveness.go:224), a pid-file + kill(0)
//     check that degrades to LIVE on any uncertainty. It stays off for a
//     different reason: its only durable output is closing claim episodes in
//     the canonical ledger, and it cannot record that it did so — the session
//     stays open in sessions-ledger.html because the remediation is an UPDATE
//     against the projection — so every daemon restart re-reaps the same dead
//     sessions. Restore it once that close is durable; the orphaned-collector
//     reaping it also does is real work being deferred with it.
//   - AUTO-INGEST — OFF. Both of its skip guards read unhydrated state:
//     CountMessages reads `messages`, so it always returns 0, and the
//     transcript_synced mtime guard reads a column the ledger hydration never
//     populates. needsIngest is therefore true for every session on every
//     pass — a full re-ingest of every transcript, forever. In a repo with
//     bug-1f338b5b and bug-4e5816f4 in its history that is a severe regression.
//   - AI-TITLE BACKFILL — OFF. It selects transcript_path, which is not among
//     the columns the ledger hydration inserts, so every row short-circuits and
//     zero titles are updated — and it then writes the persistent sentinel
//     .wipnote/migrations/ai-title-backfill.done, durably guaranteeing a pass
//     that accomplished nothing never runs again.
//   - RECAPS REINDEX — OFF. It refilled the recaps table so the dashboard's
//     Recap tab was not blank after a restart (bug-95d2d493). The dashboard now
//     populates recaps in its own projection on every start, so the loop
//     refilled a table nobody queries.
//
// The NDJSON→SQLite indexer moved rather than died: startDashboardTelemetryIndexer
// runs it in the process whose projection the dashboard actually reads.
//
// ctx cancellation (from SIGTERM/SIGINT or idle-exit) stops every loop.
func startWriterMaintenance(ctx context.Context, wipnoteDir string) {
	projectRoot := filepath.Dir(wipnoteDir)

	// Retention archival: startup, then every 24h. A fresh projection per pass
	// rather than one hydrated at daemon boot — a daemon lives for hours, and a
	// boot-time snapshot would never show a session completed since.
	startDrainLoop(ctx, retentionSweepInterval, func() {
		withFreshProjection(wipnoteDir, "retention", func(database *sql.DB) {
			if _, err := retention.Sweep(database, wipnoteDir, "", os.Getenv("WIPNOTE_RETENTION_DRYRUN") == "1"); err != nil {
				log.Printf("retention: sweep: %v", err)
			}
		})
	})

	// Out-of-band reconcile drain (feat-c08d1ba1 slice-6). No projection: the
	// callee ignores the handle and reads canonical artifacts directly, so
	// building one would be pure cost.
	startDrainLoop(ctx, reconcileDrainInterval, func() {
		hooks.ReconcileDoneButUncommittedForProject(nil, projectRoot)
	})
}

// withFreshProjection builds a short-lived compatibility projection from
// canonical state, hands it to fn, and closes it. label names the caller in
// failure logs so a broken pass is attributable.
func withFreshProjection(wipnoteDir, label string, fn func(*sql.DB)) {
	database, err := dbpkg.OpenEphemeralProjection()
	if err != nil {
		log.Printf("%s: open projection: %v", label, err)
		return
	}
	defer database.Close()
	if err := hydrateCompatibilityDB(database, wipnoteDir); err != nil {
		log.Printf("%s: hydrate projection: %v", label, err)
		return
	}
	fn(database)
}

// startDashboardTelemetryIndexer tails every per-session events.ndjson and
// materialises the signals into the dashboard's OWN projection.
//
// This is the indexer that used to run in the writer daemon. Leaving it there
// would have kept the code alive and the dashboard blank: the daemon and the
// HTTP serve_child are separate processes with separate in-memory projections,
// so signals indexed by the daemon are unreachable from the handlers that query
// them. Those handlers are not incidental — api_otel.go, api_tree.go and
// api_feed.go read otel_signals for the event tree, the activity feed, token
// and cost rollups and the per-session transcript surface. Materialising into
// the reader's own projection is what keeps them populated.
//
// Two consequences worth stating plainly:
//
//   - Every serve_child start replays every session's NDJSON from byte zero.
//     There is no on-disk checkpoint to resume from, and there must not be: the
//     projection starts empty, so an offset carried over from a previous
//     process would leave everything before it permanently unindexed. Full
//     replay is the only correct behaviour against an ephemeral destination.
//   - The replayed signals are held in memory for the dashboard's lifetime.
//     On a large history that is a real, unbounded-in-history footprint —
//     observe/otel/signalvtab (feat-ba544d57) exists to answer these same
//     queries directly over the NDJSON without materialising anything, and is
//     the intended replacement for this loop.
func startDashboardTelemetryIndexer(ctx context.Context, database *sql.DB, wipnoteDir string) {
	if database == nil || wipnoteDir == "" {
		return
	}
	writer, err := otelreceiver.NewWriterFromDB(database)
	if err != nil {
		log.Printf("dashboard telemetry indexer: writer init: %v", err)
		return
	}
	// The projection is single-process with MaxOpenConns=1, so the pool itself
	// serialises writes; the writequeue the daemon needed for cross-producer
	// contention buys nothing here.
	idxr := indexer.New(wipnoteDir, sqls.New(writer)).
		WithDB(database).
		WithWriteDB(database)
	go idxr.Start(ctx)
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
	database, err := dbpkg.OpenEphemeralProjection()
	if err != nil {
		return fmt.Errorf("open ephemeral compatibility db: %w", err)
	}
	if err := hydrateCompatibilityDB(database, wipnoteDir); err != nil {
		database.Close()
		return fmt.Errorf("hydrate compatibility db: %w", err)
	}

	// Ensure the per-project writer daemon is running and reaped on shutdown
	// (feat-075c110d increment 2). If a live writer lease already exists
	// (CLI/hook auto-spawn, or a prior serve), we reuse it; otherwise we start
	// the headless writer as a MANAGED child (Setpgid + Pdeathsig) so it is
	// SIGTERMed when this serve_child exits. The O_EXCL lease guarantees a
	// single writer regardless of who starts it.
	stopWriter := ensureWriterDaemon(projectRoot)
	defer stopWriter()

	// Telemetry materialisation runs HERE, in the process that serves the
	// queries. hydrateCompatibilityDB populates work items, ledgers, plans and
	// recaps but never otel_signals — the signals live in per-session NDJSON, so
	// something has to read them in, and it has to be this process for the
	// dashboard's OTel handlers to see them at all. Cancelled on shutdown below.
	indexerCtx, stopIndexer := context.WithCancel(context.Background())
	defer stopIndexer()
	startDashboardTelemetryIndexer(indexerCtx, database, wipnoteDir)

	mux := buildSingleProjectMux(database, database, wipnoteDir)

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
	serveLogCfg := retention.LoadConfig(filepath.Dir(wipnoteDir))
	if f, err := retention.OpenBoundedLog(logPath, serveLogCfg.LogMaxBytes, serveLogCfg.LogKeep); err == nil {
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
	writerLogCfg := retention.LoadConfig(projectRoot)
	if f, ferr := retention.OpenBoundedLog(filepath.Join(logDir, "writer.log"),
		writerLogCfg.LogMaxBytes, writerLogCfg.LogKeep); ferr == nil {
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
