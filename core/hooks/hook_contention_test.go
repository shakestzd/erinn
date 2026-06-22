package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

// hook_contention_test.go — plan-2390966a slice-4.
//
// Proves the four hot hooks (pretooluse, user-prompt, subagent-start, stop) now
// route their primary derived-index write through the daemon-first enqueue seam
// (routeSQLAsync) instead of opening a direct writable handle, and that each:
//
//	(a) returns <1s with a daemon ack       — routeSQLAsync stubbed to true,
//	(b) returns <1s via the bounded fallback — no daemon + a held external write lock,
//	(c) never errors.
//
// The held-lock harness mirrors session_start_contention_test.go: the handler
// runs against a SHORT-busy_timeout handle so every write (routed or not)
// fail-fasts under contention, and a separate connection holds BEGIN IMMEDIATE
// for the whole call.

// daemonAckBound is the done_when ceiling for the daemon-routed path: the
// enqueue-only ack is a sub-millisecond local round-trip, so even with the
// handler's other (unrouted, non-contended) bookkeeping the hook returns in well
// under a second. This is the load-bearing slice-4 guarantee — the hot write
// never touches the contended write lock when the daemon is reachable.
const daemonAckBound = time.Second

// fallbackBound is the ceiling for the NO-DAEMON + held-lock fallback path. Here
// the routed write degrades to a bounded direct Exec (fail-fast on the handle's
// busy_timeout) AND a handler may issue a few OTHER unrouted bookkeeping writes
// that each independently fail-fast on the same bound (e.g. PreToolUse's
// ReapExpiredClaims). Their sum is bounded but legitimately exceeds 1s, so this
// path asserts the same 2s ceiling session_start_contention_test.go uses for a
// multi-write handler under contention — the point is "bounded + never stalls
// ~5s + never errors", not the sub-second daemon-path figure.
const fallbackBound = 2 * time.Second

// runHotHookBusyTimeout is the busy_timeout applied to the handler's writable
// handle in these tests. It is deliberately short so each fail-fast write under a
// held lock resolves quickly, keeping the multi-write fallback path inside
// fallbackBound without flaking on slow CI.
const runHotHookBusyTimeout = 250 * time.Millisecond

// seedHookDB creates and migrates a project DB so the handler's reopen/handle is
// a warm fast path (no DDL competing for the lock), and seeds a session row the
// hooks can reference. Returns the project dir and db path.
func seedHookDB(t *testing.T) (projectDir, dbPath string) {
	t.Helper()
	projectDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	dbPath = filepath.Join(projectDir, ".wipnote", "wipnote.db")
	boot, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	// Seed a session so the read-only existence probes resolve and the routed
	// INSERTs are the writes under test (not unrelated backfills).
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := boot.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status, created_at, project_dir)
		 VALUES (?, 'agent-1', 'active', ?, ?)`,
		"sess-contention", now, projectDir); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	boot.Close()
	return projectDir, dbPath
}

// clearNestedEnv unsets nested-session env that would change the resolved
// session ID or mark the event a subagent (see MEMORY: nested-session-hooks).
func clearNestedEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "WIPNOTE_PARENT_SESSION",
		"WIPNOTE_NESTING_DEPTH", "WIPNOTE_SESSION_ID", "CLAUDE_ENV_FILE",
		"WIPNOTE_SESSION_FAMILY_ID", "WIPNOTE_AGENT_ID",
	} {
		t.Setenv(k, "")
	}
}

// runHotHook opens a SHORT-busy_timeout writable handle (so any unrouted
// incidental write also fail-fasts under contention) and invokes one of the four
// hook handlers by name with a minimal event. It returns the handler error and
// the elapsed wall time.
func runHotHook(t *testing.T, name, projectDir, dbPath string) (time.Duration, error) {
	t.Helper()
	database, reason := OpenHookDBWithBusyTimeout(name, "sess-contention", dbPath, runHotHookBusyTimeout)
	if database == nil {
		t.Fatalf("open hook handle: nil (reason=%s)", reason)
	}
	defer database.Close()

	event := &CloudEvent{SessionID: "sess-contention", CWD: projectDir}

	start := time.Now()
	var err error
	switch name {
	case "pretooluse":
		// A plain read tool with no active feature: skips the claim heartbeat,
		// exercises recordEventAndAllow → RouteInsertEvent (the routed write).
		event.ToolName = "Read"
		event.ToolInput = map[string]any{"file_path": "/tmp/x"}
		_, err = PreToolUse(event, database)
	case "user-prompt":
		event.Prompt = "please refactor the parser"
		_, err = UserPrompt(event, database)
	case "subagent-start":
		event.AgentID = "agent-sub-1"
		event.AgentType = "feature-coder"
		_, err = SubagentStart(event, database)
	case "stop":
		event.LastAssistantMessage = "done"
		_, err = Stop(event, database)
	default:
		t.Fatalf("unknown hook %q", name)
	}
	return time.Since(start), err
}

// TestHotHooksRouteWriteUnderContention is the slice-4 table test. For each hot
// hook it asserts the daemon-ack path and the bounded-fallback path both return
// <1s and never error, and that the daemon path actually exercises the
// enqueue-only seam (proving the write was migrated off the direct handle).
func TestHotHooksRouteWriteUnderContention(t *testing.T) {
	hooks := []string{"pretooluse", "user-prompt", "subagent-start", "stop"}

	for _, name := range hooks {
		name := name
		t.Run(name+"/daemon-ack", func(t *testing.T) {
			clearNestedEnv(t)
			t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1")
			projectDir, dbPath := seedHookDB(t)

			called := stubRouteSQLAsync(t, true) // daemon enqueue succeeds

			elapsed, err := runHotHook(t, name, projectDir, dbPath)
			if err != nil {
				t.Fatalf("%s returned an error on the daemon path (must swallow): %v", name, err)
			}
			if elapsed >= daemonAckBound {
				t.Fatalf("%s took %v on the daemon path; bound is <%v", name, elapsed, daemonAckBound)
			}
			if !*called {
				t.Fatalf("%s did not invoke the daemon enqueue seam — its hot write was not migrated to the daemon route", name)
			}
		})

		t.Run(name+"/bounded-fallback", func(t *testing.T) {
			clearNestedEnv(t)
			t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1")
			projectDir, dbPath := seedHookDB(t)

			stubRouteSQLAsync(t, false) // force the bounded direct fallback

			// Hold a RESERVED write lock on a SEPARATE connection for the whole
			// call so the routed write's bounded fallback (and any incidental
			// unrouted write on the short-timeout handle) degrades fast.
			holderDB, err := db.Open(dbPath)
			if err != nil {
				t.Fatalf("open holder: %v", err)
			}
			defer holderDB.Close()
			holder, err := holderDB.Conn(context.Background())
			if err != nil {
				t.Fatalf("acquire holder conn: %v", err)
			}
			defer holder.Close()
			if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
				t.Fatalf("acquire write lock: %v", err)
			}
			defer holder.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck

			elapsed, hookErr := runHotHook(t, name, projectDir, dbPath)
			if hookErr != nil {
				t.Fatalf("%s returned an error under a held lock (must swallow via canonical-first): %v", name, hookErr)
			}
			if elapsed >= fallbackBound {
				t.Fatalf("%s blocked %v under a held write lock; bounded fallback ceiling is <%v (must not stall ~5s)", name, elapsed, fallbackBound)
			}
		})
	}
}
