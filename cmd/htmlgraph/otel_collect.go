package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/otel"
	"github.com/shakestzd/htmlgraph/internal/otel/adapter"
	"github.com/shakestzd/htmlgraph/internal/otel/indexer"
	"github.com/shakestzd/htmlgraph/internal/otel/receiver"
	"github.com/shakestzd/htmlgraph/internal/otel/sink/ndjson"
	sqlsink "github.com/shakestzd/htmlgraph/internal/otel/sink/sqlite"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

const (
	defaultIdleTimeout = 5 * time.Minute
	defaultDrainBudget = 10 * time.Second
)

func otelCollectCmd() *cobra.Command {
	var (
		sessionID  string
		projectDir string
		listen     string
	)
	cmd := &cobra.Command{
		Use:    "otel-collect",
		Hidden: true,
		Short:  "Internal: per-session OTel collector (do not invoke directly)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runOtelCollect(sessionID, projectDir, listen)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session-id", "", "Session ULID (required)")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "Project root (required)")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "Listen address (default ephemeral)")
	_ = cmd.MarkFlagRequired("session-id")
	_ = cmd.MarkFlagRequired("project-dir")
	return cmd
}

func runOtelCollect(sessionID, projectDir, listenAddr string) error {
	htmlgraphDir := filepath.Join(projectDir, ".htmlgraph")
	sessDir := filepath.Join(htmlgraphDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	snk, err := ndjson.New(projectDir, sessionID)
	if err != nil {
		return fmt.Errorf("create ndjson sink: %w", err)
	}
	defer snk.Close()

	// Mount the NDJSON→SQLite indexer goroutine so dashboard reads see
	// live span data within ~one tick of OTLP receive. Two SQLite handles
	// share the canonical DB file: receiver.NewWriter is the dedicated
	// single-conn writer for signal inserts; db.Open opens the canonical
	// pool used by indexer.WithDB for prompt_id bridging.
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return fmt.Errorf("resolve canonical db path: %w", err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open canonical db: %w", err)
	}
	defer database.Close()

	writer, err := receiver.NewWriter(dbPath)
	if err != nil {
		return fmt.Errorf("open otel writer: %w", err)
	}
	defer writer.Close()

	idx := indexer.New(htmlgraphDir, sqlsink.New(writer)).WithDB(database)

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	lastActivity := &atomic.Int64{}
	lastActivity.Store(time.Now().UnixMilli())

	mux := buildCollectorMux(snk, lastActivity)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if _, err := fmt.Fprintf(os.Stdout, "htmlgraph-otel-ready port=%d\n", port); err != nil {
		return fmt.Errorf("write handshake: %w", err)
	}
	_ = os.Stdout.Sync()

	if err := writeCollectorStartEvent(snk, sessionID, port); err != nil {
		fmt.Fprintf(os.Stderr, "collector_start event: %v\n", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "otel-collect serve: %v\n", err)
		}
	}()

	var indexerWG sync.WaitGroup
	indexerWG.Add(1)
	go func() {
		defer indexerWG.Done()
		idx.Start(ctx)
		// Final drain pass after main ctx cancels — gives signals already
		// in NDJSON one more chance to land in SQLite. Uses a fresh context
		// so the cancelled poll-loop ctx does not short-circuit runOnce.
		drainCtx, drainCancel := context.WithTimeout(context.Background(), parseDrainBudget())
		defer drainCancel()
		idx.RunOnce(drainCtx)
	}()

	return awaitShutdown(ctx, cancel, srv, snk, lastActivity, &indexerWG, htmlgraphDir, sessionID)
}

// buildCollectorMux creates the OTLP HTTP mux with activity tracking.
func buildCollectorMux(snk *ndjson.Sink, lastActivity *atomic.Int64) *http.ServeMux {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewClaudeAdapter())

	handler := &collectorHandler{
		registry:     reg,
		sink:         snk,
		lastActivity: lastActivity,
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/traces", handler)
	mux.Handle("/v1/metrics", handler)
	mux.Handle("/v1/logs", handler)
	return mux
}

// writeCollectorStartEvent writes the collector_start NDJSON line.
func writeCollectorStartEvent(snk *ndjson.Sink, sessionID string, port int) error {
	attrs := map[string]any{
		"htmlgraph_sid": sessionID,
		"pid":           os.Getpid(),
		"port":          port,
	}

	sig := otel.UnifiedSignal{
		Harness:       "htmlgraph",
		SignalID:      "collector-start-" + sessionID,
		Kind:          "collector_start",
		CanonicalName: "collector_start",
		NativeName:    "collector_start",
		Timestamp:     time.Now().UTC(),
		SessionID:     sessionID,
		RawAttrs:      attrs,
	}
	return snk.WriteBatch(context.Background(), "htmlgraph", nil, []otel.UnifiedSignal{sig})
}

// awaitShutdown blocks until SIGTERM or idle timeout, then gracefully shuts down.
// indexerWG must be Add'd for each indexer goroutine the caller spawned;
// gracefulShutdown waits on it so signals already in NDJSON drain to SQLite
// before the process exits.
func awaitShutdown(ctx context.Context, cancel context.CancelFunc, srv *http.Server, snk *ndjson.Sink, lastActivity *atomic.Int64, indexerWG *sync.WaitGroup, htmlgraphDir, sessionID string) error {
	idleTimeout := parseIdleTimeout()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			cancel()
			return gracefulShutdown(srv, snk, indexerWG, htmlgraphDir, sessionID)
		case <-ticker.C:
			elapsed := time.Since(time.UnixMilli(lastActivity.Load()))
			if elapsed >= idleTimeout {
				cancel()
				return gracefulShutdown(srv, snk, indexerWG, htmlgraphDir, sessionID)
			}
		case <-ctx.Done():
			return gracefulShutdown(srv, snk, indexerWG, htmlgraphDir, sessionID)
		}
	}
}

// gracefulShutdown stops the HTTP server, waits for the indexer goroutine to
// drain (caller must have cancelled the indexer ctx before calling), then
// closes the NDJSON sink.
//
// Drain budget defaults to defaultDrainBudget and is overridable via
// HTMLGRAPH_OTEL_DRAIN_SECS. If drain exceeds the budget, the count of
// unindexed NDJSON lines is logged and a non-zero error is returned —
// orphaned signals recover via checkpoint replay on next collector launch.
func gracefulShutdown(srv *http.Server, snk *ndjson.Sink, indexerWG *sync.WaitGroup, htmlgraphDir, sessionID string) error {
	httpCtx, httpCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer httpCancel()
	_ = srv.Shutdown(httpCtx)

	budget := parseDrainBudget()
	drained := waitWithBudget(indexerWG, budget)

	closeErr := snk.Close()
	if !drained {
		leftover := countUnindexedSignals(htmlgraphDir, sessionID)
		log.Printf("otel-collect: indexer drain exceeded %s budget — %d signals unindexed in session %s NDJSON",
			budget, leftover, sessionID)
		return fmt.Errorf("indexer drain exceeded %s: %d signals unindexed", budget, leftover)
	}
	return closeErr
}

// waitWithBudget blocks on wg up to budget, returning true when the
// WaitGroup completed within budget and false on timeout. nil wg returns true.
func waitWithBudget(wg *sync.WaitGroup, budget time.Duration) bool {
	if wg == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		return false
	}
}

// countUnindexedSignals reads the indexer checkpoint and the events.ndjson
// file for sessionID, returning the number of newline-terminated lines
// past the checkpoint offset. Best-effort: returns -1 on read error.
func countUnindexedSignals(htmlgraphDir, sessionID string) int {
	sessDir := filepath.Join(htmlgraphDir, "sessions", sessionID)
	checkpointPath := filepath.Join(sessDir, ".index-offset")
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")

	var offset int64
	if data, err := os.ReadFile(checkpointPath); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			offset = v
		}
	}

	f, err := os.Open(ndjsonPath)
	if err != nil {
		return -1
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return -1
		}
	}
	r := bufio.NewReaderSize(f, 64*1024)
	count := 0
	for {
		_, err := r.ReadString('\n')
		if err != nil {
			break
		}
		count++
	}
	return count
}

func parseIdleTimeout() time.Duration {
	if s := os.Getenv("HTMLGRAPH_OTEL_IDLE_TIMEOUT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
		if ms, err := strconv.Atoi(s); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultIdleTimeout
}

// parseDrainBudget reads HTMLGRAPH_OTEL_DRAIN_SECS as either a Go
// duration string ("750ms", "5s") or a bare integer second count.
// Falls back to defaultDrainBudget when unset or unparseable.
func parseDrainBudget() time.Duration {
	if s := os.Getenv("HTMLGRAPH_OTEL_DRAIN_SECS"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultDrainBudget
}
