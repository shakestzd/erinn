package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/otel/retention"
	"github.com/shakestzd/htmlgraph/internal/registry"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

// serveChildCmd is the hidden internal subcommand the parent HtmlGraph
// server spawns for each project in multi-project mode. It is NOT intended
// for direct invocation — end users run `htmlgraph serve`, which forks this
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
	cmd := &cobra.Command{
		Use:    "_serve-child",
		Hidden: true,
		Short:  "Internal: single-project HTTP server spawned by parent (do not invoke directly)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runServeChild(port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "TCP port (0 = ephemeral)")
	return cmd
}

// runServeChild opens the project DB, builds the single-project mux, binds
// the listener, prints the handshake, redirects stdio, and serves HTTP.
func runServeChild(port int) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return fmt.Errorf("locate .htmlgraph: %w", err)
	}

	dbPath, err := storage.CanonicalDBPath(filepath.Dir(htmlgraphDir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return fmt.Errorf("ensure db dir: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	// database is closed when the process exits; no defer Close — Serve blocks.

	mux := buildSingleProjectMux(database, htmlgraphDir)

	// NOTE(bug-28a9d7a7 Part B): The NDJSON→SQLite indexer and its dedicated
	// otelreceiver.NewWriter have been removed. Opening a second sql.DB writer
	// (MaxOpenConns=1) against the same WAL SQLite file contended with the
	// read-pool handle used by the API handlers, causing SQLITE_BUSY errors.
	// The per-session otel-collect process is now the sole writer to otel_signals.
	// serve-child is read-only with respect to otel_signals.
	// TODO(bug-28a9d7a7): If NDJSON→SQLite replay is needed for pre-existing
	// sessions, run the indexer inside the otel-collect process so only one
	// writer DB handle exists across the entire project.

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	assigned := ln.Addr().(*net.TCPAddr).Port

	// Handshake: MUST be the first output of this process. The parent
	// supervisor (internal/childproc, slice 2) reads exactly one line
	// matching `htmlgraph-serve-ready port=<N> pid=<P>` with a 5s deadline.
	// Any prior stdout write — log line, deprecation warning, anything —
	// corrupts the scanner. Do not add prints above this line.
	if _, err := fmt.Printf("htmlgraph-serve-ready port=%d pid=%d\n", assigned, os.Getpid()); err != nil {
		return fmt.Errorf("write handshake: %w", err)
	}
	if err := os.Stdout.Sync(); err != nil {
		// Non-fatal: the parent has already read the line via its pipe.
		_ = err
	}

	// Redirect stdout/stderr to a per-project log file so subsequent logs
	// (auto-ingest, handler errors, etc.) don't leak through the supervisor's
	// drain goroutine to the parent's terminal.
	projectID := registry.ComputeID(filepath.Dir(htmlgraphDir))
	logsDir := filepath.Join(htmlgraphDir, "logs")
	_ = os.MkdirAll(logsDir, 0o755)
	logPath := filepath.Join(logsDir, fmt.Sprintf("serve-%s.log", projectID))
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		os.Stdout = f
		os.Stderr = f
	}

	// Auto-ingest transcripts on startup and every 60s, scoped to this
	// project via the explicit htmlgraphDir argument (not CWD). After the
	// first ingest cycle completes we kick off a one-time ai-title backfill
	// so it observes any newly-ingested legacy sessions instead of writing
	// its `.done` marker against an empty sessions table.
	go autoIngestLoop(database, htmlgraphDir, func() {
		startAITitleBackfill(context.Background(), database, htmlgraphDir)
	})

	// Retention job: archive sessions older than HTMLGRAPH_SESSION_RETAIN_DAYS
	// (default 30) at startup and every 24h. Dry-run via HTMLGRAPH_RETENTION_DRYRUN=1.
	retention.StartLoop(context.Background(), database, htmlgraphDir)

	return (&http.Server{Handler: mux}).Serve(ln)
}

