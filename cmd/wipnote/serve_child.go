package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/internal/daemon"
	dbpkg "github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/db/writequeue"
	"github.com/shakestzd/wipnote/internal/otel/indexer"
	otelreceiver "github.com/shakestzd/wipnote/internal/otel/receiver"
	"github.com/shakestzd/wipnote/internal/otel/retention"
	sqls "github.com/shakestzd/wipnote/internal/otel/sink/sqlite"
	"github.com/shakestzd/wipnote/internal/registry"
	"github.com/shakestzd/wipnote/internal/storage"
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
	cmd := &cobra.Command{
		Use:    "_serve-child",
		Hidden: true,
		Short:  "Internal: single-project HTTP server spawned by parent (do not invoke directly)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if headless {
				// MVP-2 (feat-075c110d): writer-only mode — acquire the
				// per-project write lease, open the writable DB + writequeue,
				// and run ONLY the daemon socket listener. No HTTP mux. The
				// default (non-headless) path below is unchanged.
				return runWriterOnly()
			}
			return runServeChild(port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "TCP port (0 = ephemeral)")
	cmd.Flags().BoolVar(&headless, "headless", false, "Writer-only mode: run the daemon socket listener + writequeue without the HTTP dashboard (feat-075c110d MVP-2)")
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
func runWriterOnly() error {
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
	// default path does, before the writequeue worker touches the DB.
	if writeDB, err := dbpkg.Open(dbPath); err != nil {
		return fmt.Errorf("open db (writable, schema): %w", err)
	} else {
		// The headless writer holds the schema-bearing handle open for its
		// lifetime so reindex/migrations stay consistent; the writequeue
		// worker owns the actual write connection via receiver.NewWriter.
		defer writeDB.Close()
	}

	writer, err := otelreceiver.NewWriter(dbPath)
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

	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: daemon.SocketPath(projectRoot),
		Queue:      q,
		Applier:    daemon.RejectingApplier,
	})
	if err != nil {
		return fmt.Errorf("bind writer socket: %w", err)
	}
	defer ln.Close()

	// Readiness signal: the socket is bound and the queue is running. A
	// future auto-spawn parent (MVP follow-on) can dial to confirm; for
	// MVP-2 we just log it.
	fmt.Fprintf(os.Stderr, "wipnote: writer-only daemon ready socket=%s pid=%d\n", ln.Addr(), os.Getpid())

	return ln.Serve(ctx)
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

	dbPath, err := storage.CanonicalDBPath(filepath.Dir(wipnoteDir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return fmt.Errorf("ensure db dir: %w", err)
	}
	// bug-74a7bda7 topology split: the dashboard mux gets a READ-ONLY handle
	// so no HTTP request can ever escalate a SHARED lock to RESERVED on the
	// project DB (the root cause of SQLITE_BUSY blocking completions while a
	// parallel session writes — on every filesystem, WAL or DELETE).
	//
	// dbpkg.Open (writable + migrations) still runs FIRST so the schema
	// exists / is current before any read-only connection opens (mode=ro
	// never creates a file and never migrates). That same writable handle is
	// then reused by the background maintenance loops that legitimately write
	// but do NOT fit the slice-6 signal-batch write queue API
	// (auto-ingest, ai-title backfill, indexer prompt-ID bridge). The
	// slice-6 Writer still owns all OTLP/indexer-batch writes.
	writeDB, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db (writable, schema): %w", err)
	}
	database, err := dbpkg.OpenReadOnly(dbPath)
	if err != nil {
		return fmt.Errorf("open db (read-only mux): %w", err)
	}
	// Cap the dashboard read pool so a burst of concurrent HTTP requests
	// cannot open an unbounded number of SQLite connections (each of which
	// takes a SHARED lock and, under DELETE journal mode, serialises against
	// the single writer). 12 is comfortably above the dashboard's steady
	// concurrency while bounding worst-case lock pressure.
	database.SetMaxOpenConns(dashboardReadPoolMaxConns)
	// Both handles live for the process lifetime; no defer Close — Serve blocks.

	// Slice 6 writer service (plan-ae0c37b2): one Writer + one queue
	// per project DB. Every in-process producer (indexer, future OTLP
	// receiver, sub-agent auto-ingest) submits SignalSink batches
	// through this queue instead of opening its own writable handle.
	// This is the architectural fix for the SQLITE_BUSY contention the
	// plan targets — see plan q-service-owner for the post-launch
	// `wipnote daemon` graduation path.
	if writer, err := otelreceiver.NewWriter(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "writer service init: %v\n", err)
	} else {
		q := writequeue.New(writequeue.Config{
			Capacity: writequeue.DefaultCapacity,
			OnError: func(err error) {
				// Slice-10 contention observability: BUSY classification
				// already lands at the WriteBatch boundary in
				// internal/otel/sink/sqlite/writer.go under the writer_service
				// subsystem (which captures the actual SQL contention
				// site). Counting again here would double-bill the same
				// event. We keep the OnError hook as the log-only path
				// so operators can correlate the queue depth surfaced via
				// /api/collector-status with worker errors.
				log.Printf("writequeue: op error: %v", err)
			},
		})
		if startErr := q.Start(context.Background()); startErr != nil {
			fmt.Fprintf(os.Stderr, "writer queue start: %v\n", startErr)
			_ = writer.Close()
		} else {
			writerService.queue = q
			writerService.sink = sqls.NewQueued(q, writer)
		}
	}

	mux := buildSingleProjectMux(database, writeDB, wipnoteDir)

	// NDJSON→SQLite indexer (unconditional per Q5 cutover decision).
	// The indexer now routes every SignalSink batch through the slice-6
	// writer queue rather than holding its own writable handle. Canonical
	// persistence is upstream of this path — the indexer reads NDJSON
	// produced by per-session collectors, so user work is durable on
	// disk before any submit hits the queue (canonical-first contract).
	if writerService.sink != nil {
		// Change 1 (bug-272c5e34): pass the read-only `database` handle so
		// filterSessionsByDB / queryKnownSessionIDs SELECTs no longer hold a
		// SHARED lock on the writable handle and can't block the queue
		// worker's BEGIN IMMEDIATE.
		//
		// Change 2 (bug-272c5e34): wire the writequeue so maybeSetPromptID
		// routes its SELECT+UPDATE through the single writer instead of
		// issuing an independent write on a second connection.  WithDB is
		// still needed for the read-only filter path above; the writable
		// operations now go through the queue.
		idxr := indexer.New(wipnoteDir, writerService.sink).
			WithDB(database).
			WithWriteDB(writeDB).
			WithQueue(writerService.queue)
		ctx := context.Background()
		go idxr.Start(ctx)
		// /api/indexer/status — per-file health for observability (Q7).
		mux.Handle("/api/indexer/status", indexerStatusHandler(idxr))
	}

	// /api/collector-status — slice-6 diagnostic surface. Returns writer
	// queue depth/state/rates so the dashboard can show backpressure.
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

	// Auto-ingest transcripts on startup and every 60s, scoped to this
	// project via the explicit wipnoteDir argument (not CWD). After the
	// first ingest cycle completes we kick off a one-time ai-title backfill
	// so it observes any newly-ingested legacy sessions instead of writing
	// its `.done` marker against an empty sessions table.
	// auto-ingest and ai-title backfill both issue INSERT/UPDATE/DELETE on
	// sessions/messages/tool_calls — route them to the writable handle, not
	// the read-only mux handle (bug-74a7bda7 STEP 0 reroute).
	go autoIngestLoop(writeDB, wipnoteDir, func() {
		startAITitleBackfill(context.Background(), writeDB, wipnoteDir)
	})

	// Retention job: archive sessions older than WIPNOTE_SESSION_RETAIN_DAYS
	// (default 30) at startup and every 24h. Dry-run via WIPNOTE_RETENTION_DRYRUN=1.
	retention.StartLoop(context.Background(), database, wipnoteDir)

	return (&http.Server{Handler: mux}).Serve(ln)
}

// indexerStatusHandler returns an HTTP handler for GET /api/indexer/status.
// The response body is a JSON object with per-session file health metrics.
func indexerStatusHandler(idxr *indexer.Indexer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := idxr.Status()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"files": status}); err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
		}
	})
}
