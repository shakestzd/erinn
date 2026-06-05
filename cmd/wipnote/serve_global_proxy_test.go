//go:build integration

// Package main — integration tests for the multi-project doorway server.
//
// These tests exercise the full parent + reverse proxy + childproc
// supervisor + real `wipnote _serve-child` binary pipeline. They are
// gated behind `//go:build integration` so `go test ./...` keeps the
// fast unit suite by default; CI adds a second job with
// `-tags=integration` to run these.
//
// Per feasibility critic A10/C8, TestMain builds the wipnote binary
// ONCE at package startup and every test reuses that binary via the
// package-level testBinPath. A per-test go build would cost 5-15s each
// and blow the 60s CI budget.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/childproc"
	"github.com/shakestzd/wipnote/internal/registry"
)

var testBinPath string

// TestMain builds the wipnote binary once, stores the path in
// testBinPath, runs the tests, and (best-effort) cleans up afterwards.
// The built binary is used by every test that needs a real
// _serve-child process.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "wipnote-integration-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdirtemp:", err)
		os.Exit(1)
	}
	testBinPath = filepath.Join(tmp, "wipnote")

	build := exec.Command("go", "build", "-o", testBinPath, "./")
	build.Dir = "."
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build wipnote for integration tests:", err)
		_ = os.RemoveAll(tmp)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// mkIntegrationProject creates a tmpdir project with .wipnote/ and a
// SQLite DB containing a minimal schema plus the given feature IDs so
// tests can distinguish which project served a request by inspecting
// the returned count. Returns the project root (parent of .wipnote).
func mkIntegrationProject(t *testing.T, featureIDs ...string) string {
	t.Helper()
	tmp := t.TempDir()
	hgDir := filepath.Join(tmp, ".wipnote")
	if err := os.MkdirAll(hgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(hgDir, "wipnote.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Schema subset that statsHandler and featuresHandler read.
	stmts := []string{
		`CREATE TABLE features (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL DEFAULT 'feature',
			title TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'todo',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE sessions (
			session_id TEXT PRIMARY KEY,
			status TEXT DEFAULT 'completed',
			is_subagent INTEGER DEFAULT 0,
			metadata TEXT,
			title TEXT,
			transcript_synced DATETIME
		)`,
		`CREATE TABLE agent_events (
			id INTEGER PRIMARY KEY,
			session_id TEXT,
			event_type TEXT,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE messages (
			id INTEGER PRIMARY KEY,
			session_id TEXT,
			model TEXT,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	for _, id := range featureIDs {
		if _, err := db.Exec(`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', ?, 'todo')`,
			id, id+" title"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	db.Close()
	return tmp
}

// newTestRegistry points registry.DefaultPath at a tmpdir (via HOME
// override) and upserts the given project dirs. Returns the registry
// file path.
func newTestRegistry(t *testing.T, projectDirs ...string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	regPath := registry.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(regPath), 0o755); err != nil {
		t.Fatal(err)
	}
	reg, _ := registry.Load(regPath)
	for _, d := range projectDirs {
		reg.Upsert(d, filepath.Base(d), "")
	}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
}

// mkProxyServer wires a supervisor pointing at the test-built binary
// into buildParentMux and wraps it in an httptest.Server. The returned
// cleanup closes the server and shuts the supervisor down (SIGTERM all
// children, wait, SIGKILL survivors).
func mkProxyServer(t *testing.T, idleTimeout time.Duration) (*httptest.Server, *childproc.Supervisor) {
	t.Helper()
	sup := childproc.NewSupervisor(childproc.Options{
		BinPath:     testBinPath,
		IdleTimeout: idleTimeout,
		// SpawnTimeout unset — falls back to childproc.DefaultSpawnTimeout.
	})
	mux := buildParentMux(sup)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})
	return srv, sup
}

// projectID derives the registry ID for a given project dir (must
// match registry.ComputeID exactly).
func projectID(t *testing.T, dir string) string {
	t.Helper()
	return registry.ComputeID(dir)
}

// fetchBody issues GET url and returns (status, body).
func fetchBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// --------------------------------------------------------------------
// Test A: cross-project isolation proof
// --------------------------------------------------------------------
// Routes a request to /p/<idA>/api/stats and verifies the response
// reflects projA's feature count, NOT projB's. Then opens projB's DB
// directly from disk (bypassing the proxy) and verifies it contains
// its own features — proving the A request did not contaminate B.
func TestCrossProjectIsolation(t *testing.T) {
	projA := mkIntegrationProject(t, "feat-in-A-1", "feat-in-A-2", "feat-in-A-3")
	projB := mkIntegrationProject(t, "feat-in-B")

	newTestRegistry(t, projA, projB)
	srv, _ := mkProxyServer(t, 30*time.Second)

	idA := projectID(t, projA)
	idB := projectID(t, projB)

	// Hit project A via the proxy.
	statusA, bodyA := fetchBody(t, srv.URL+"/p/"+idA+"/api/stats")
	if statusA != http.StatusOK {
		t.Fatalf("GET /p/%s/api/stats: got %d, body=%s", idA, statusA, bodyA)
	}
	// statsHandler returns JSON with "total" field. Project A has 3 features.
	if !strings.Contains(bodyA, `"features_total":3`) {
		t.Errorf("project A stats: expected total=3, got %s", bodyA)
	}

	// Hit project B via the proxy.
	statusB, bodyB := fetchBody(t, srv.URL+"/p/"+idB+"/api/stats")
	if statusB != http.StatusOK {
		t.Fatalf("GET /p/%s/api/stats: got %d, body=%s", idB, statusB, bodyB)
	}
	if !strings.Contains(bodyB, `"features_total":1`) {
		t.Errorf("project B stats: expected total=1, got %s", bodyB)
	}

	// Cross-check: open projB's DB directly and assert its feature set
	// is untouched — the A request did not leak into B.
	dbB, err := sql.Open("sqlite", filepath.Join(projB, ".wipnote", "wipnote.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	var count int
	if err := dbB.QueryRow(`SELECT COUNT(*) FROM features WHERE id LIKE 'feat-in-A%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("project B was contaminated with %d rows from project A", count)
	}
}

// --------------------------------------------------------------------
// Test B: crash recovery
// --------------------------------------------------------------------
// Spawns a child via a proxy request, kills the child with SIGKILL,
// waits briefly for the reaper goroutine to clean up the map entry,
// issues a second request and asserts the supervisor spawned a fresh
// child (different PID).
func TestCrashRecovery(t *testing.T) {
	proj := mkIntegrationProject(t, "feat-1")
	newTestRegistry(t, proj)
	srv, sup := mkProxyServer(t, 30*time.Second)
	id := projectID(t, proj)

	// First request spawns a child.
	status, _ := fetchBody(t, srv.URL+"/p/"+id+"/api/stats")
	if status != http.StatusOK {
		t.Fatalf("first request: got %d", status)
	}
	children := sup.Children()
	if len(children) != 1 {
		t.Fatalf("expected 1 child after first request, got %d", len(children))
	}
	firstPID := children[0].PID

	// SIGKILL the child.
	if err := syscall.Kill(firstPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", firstPID, err)
	}

	// Wait for the reaper goroutine to remove the dead child from the map.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sup.Children()) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(sup.Children()) != 0 {
		t.Fatalf("child not reaped after SIGKILL: %+v", sup.Children())
	}

	// Second request should spawn a fresh child.
	status2, _ := fetchBody(t, srv.URL+"/p/"+id+"/api/stats")
	if status2 != http.StatusOK {
		t.Fatalf("post-crash request: got %d", status2)
	}
	children = sup.Children()
	if len(children) != 1 {
		t.Fatalf("expected 1 child after recovery, got %d", len(children))
	}
	if children[0].PID == firstPID {
		t.Errorf("recovery spawned same PID %d — reaper did not clean up", firstPID)
	}
}

// --------------------------------------------------------------------
// Test C: idle reap
// --------------------------------------------------------------------
// Spawns a child, waits past the idle timeout without sending any
// requests, and asserts the idle reaper killed the child and removed
// its map entry.
func TestIdleReapViaProxy(t *testing.T) {
	proj := mkIntegrationProject(t, "feat-1")
	newTestRegistry(t, proj)

	// Very short idle timeout so the test runs fast. The supervisor's
	// RunIdleReaper ticks at idleTimeout/4, so 200ms → 50ms tick.
	srv, sup := mkProxyServer(t, 200*time.Millisecond)
	id := projectID(t, proj)

	// Start the idle reaper goroutine on the supervisor (not started by
	// mkProxyServer because runParentServer owns that goroutine in
	// production).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.RunIdleReaper(ctx)

	// Spawn a child.
	status, _ := fetchBody(t, srv.URL+"/p/"+id+"/api/stats")
	if status != http.StatusOK {
		t.Fatalf("spawn: got %d", status)
	}
	if len(sup.Children()) != 1 {
		t.Fatalf("expected 1 child, got %d", len(sup.Children()))
	}

	// Wait long enough for the reaper to observe the stale child.
	// 200ms idle timeout × 2 = 400ms, plus one tick interval, plus
	// reaper->cmd.Wait goroutine delay.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sup.Children()) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child was not reaped after idle: children=%d", len(sup.Children()))
}

// TestServeManagedWriterReapedOnShutdown is the increment-2 lifecycle proof
// (feat-075c110d). It drives a real `wipnote _serve-child` (the HTTP dashboard
// child) through the proxy. That child, being strictly read-only now, ensures a
// per-project headless writer daemon is running as a MANAGED child. We assert:
//
//	(a) a headless writer daemon comes up and holds the lease + binds the socket
//	    (so exactly one writable owner exists);
//	(b) on serve shutdown (supervisor SIGTERMs the serve_child) the managed
//	    writer is reaped — its lease (writer.pid) and socket (writer.sock) are
//	    removed by the writer's deferred cleanup.
func TestServeManagedWriterReapedOnShutdown(t *testing.T) {
	proj := mkIntegrationProject(t, "feat-1")
	newTestRegistry(t, proj)

	srv, sup := mkProxyServer(t, 0) // idle reaping disabled
	id := projectID(t, proj)

	// One request spawns the serve_child, which in turn ensures the writer.
	status, _ := fetchBody(t, srv.URL+"/p/"+id+"/api/stats")
	if status != http.StatusOK {
		t.Fatalf("spawn serve_child: got %d", status)
	}

	leasePath := filepath.Join(proj, ".wipnote", "writer.pid")
	sockPath := filepath.Join(proj, ".wipnote", "writer.sock")

	// (a) The managed writer must come up and claim the lease + socket.
	deadline := time.Now().Add(5 * time.Second)
	var up bool
	for time.Now().Before(deadline) {
		_, lerr := os.Stat(leasePath)
		_, serr := os.Stat(sockPath)
		if lerr == nil && serr == nil {
			up = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !up {
		t.Fatalf("managed writer never came up: lease/socket not present at %s", filepath.Dir(leasePath))
	}

	// Shut serve down: supervisor SIGTERMs the serve_child, whose deferred
	// stopWriter() SIGTERMs the managed writer; the writer's deferred cleanup
	// removes the lease + socket.
	srv.Close()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sup.Shutdown(shutCtx)

	// (b) After shutdown the writer must be reaped — lease + socket gone.
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		_, lerr := os.Stat(leasePath)
		_, serr := os.Stat(sockPath)
		if os.IsNotExist(lerr) && os.IsNotExist(serr) {
			return // reaped cleanly
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, lerr := os.Stat(leasePath)
	_, serr := os.Stat(sockPath)
	t.Fatalf("managed writer not reaped on shutdown: lease err=%v socket err=%v", lerr, serr)
}
