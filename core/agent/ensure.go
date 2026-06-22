package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/paths"
)

// RouteSessionInsertFn is a package-level seam that callers (e.g. main.go
// persistentPreRunE) may inject to route the cold-path session INSERT through
// the writer daemon instead of a direct writable open. The function receives
// the project root and the fields of the session row; it returns true when the
// daemon accepted and applied (or deduped) the insert. Returning false means
// the caller should fall back to the direct-write path.
//
// The seam is a plain function var (not an interface) so tests can stub it
// without registering a mock type. The zero value (nil) means "no daemon
// routing" — EnsureSessionRouted falls back to EnsureSessionWithTimeout.
var RouteSessionInsertFn func(projectRoot, sessionID, agentID, now, model, projectDir, gitRemoteURL string) bool

// EnsureSession ensures a session row exists in the database for the current
// agent invocation. It is designed to be called on every CLI command via
// PersistentPreRunE, self-healing attribution chains when hooks fail.
//
// Hot path:   session already exists → single SELECT under 1ms (indexed PK).
// Cold path:  session missing → INSERT OR IGNORE + .active-session write.
// Transient:  "cli-*" sessions skip DB entirely (human CLI usage).
//
// On success (non-transient), os.Setenv("WIPNOTE_SESSION_ID", sessionID) is
// called so that downstream EnvSessionID() calls work automatically.
func EnsureSession(database *sql.DB, projectDir string) (string, error) {
	return EnsureSessionWithTimeout(database, projectDir, 0)
}

// EnsureSessionRouted is the daemon-first variant used by persistentPreRunE.
// It uses a split-handle strategy to eliminate write-lock contention on the hot
// path:
//
//  1. Exists-check (SELECT COUNT) runs on readOnlyDB — never acquires a write
//     lock, so the launch chooser renders <1s even when another process holds
//     the writer lock (WAL readers and writers do not block each other).
//
//  2. Cold path (session missing): tries RouteSessionInsertFn first (daemon
//     ENQUEUE-ONLY ack, bounded AsyncEnqueueBudget — bug-d792aee6 finding 1
//     flipped this from applied-ack so it stays <1s under a held external lock).
//     On a true ack the insert is durably queued (applies FIFO; SessionStart's
//     idempotent upsert + reindex are the backstop) and no direct writable
//     handle is opened.
//
//  3. Last-resort fallback: if RouteSessionInsertFn is nil or returns false
//     (daemon unreachable / queue-full / timeout), EnsureSessionWithTimeout is
//     called on writableDB with the caller-supplied timeout. This mirrors the
//     pre-slice-5 behaviour exactly, preserving busy_timeout-restore semantics.
//
// The caller is responsible for opening readOnlyDB (db.OpenReadOnly) and
// writableDB (db.Open / openDB) before calling this function, and for closing
// both handles. projectRoot is the parent of the .wipnote directory.
func EnsureSessionRouted(readOnlyDB, writableDB *sql.DB, projectDir, projectRoot string, timeout time.Duration) (string, error) {
	sessionID := ResolveSessionID(projectDir)
	info := Detect()

	// Transient sessions (human CLI) skip the database entirely to avoid
	// polluting the sessions table with ephemeral PID-based IDs.
	if strings.HasPrefix(sessionID, "cli-") {
		return sessionID, nil
	}

	ctx := context.Background()

	// Hot path: exists-check on the read-only handle — no write lock acquired.
	// A WAL reader never blocks the writer and vice versa, so this is safe even
	// under a concurrently held RESERVED lock.
	var count int
	if err := readOnlyDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&count); err != nil {
		// Read failed (e.g. DB not yet initialised). Fall through to the
		// writable path which will create the schema on first open.
		count = 0
	}
	if count > 0 {
		os.Setenv("WIPNOTE_SESSION_ID", sessionID) //nolint:errcheck
		return sessionID, nil
	}

	// Cold path: session row is missing. Try daemon route first.
	now := time.Now().UTC().Format(time.RFC3339)
	gitRemoteURL := paths.GetGitRemoteURL(projectDir)
	if RouteSessionInsertFn != nil && RouteSessionInsertFn(projectRoot, sessionID, info.ID, now, info.Model, projectDir, gitRemoteURL) {
		// Daemon applied (or deduped) the insert — row is durable.
		writeEnsuredActiveSession(sessionID, projectDir, info.ID)
		os.Setenv("WIPNOTE_SESSION_ID", sessionID) //nolint:errcheck
		return sessionID, nil
	}

	// Last-resort fallback: daemon unreachable or RouteSessionInsertFn nil.
	// Use the writable handle with the caller's timeout, matching pre-slice-5
	// behaviour (busy_timeout-bounded, INSERT OR IGNORE idempotent).
	return EnsureSessionWithTimeout(writableDB, projectDir, timeout)
}

// EnsureSessionWithTimeout is EnsureSession with a wall-clock bound on the
// SQLite operations. When timeout > 0 the SELECT/INSERT run under a context
// deadline, so a write lock held by another wipnote process cannot stall the
// caller for the full SQLite busy_timeout (5s). On deadline-exceeded the
// cold-path INSERT is abandoned and the error is returned; because the row
// write is best-effort and idempotent (INSERT OR IGNORE), a later uncontended
// call (or a hook) re-creates it. The launcher's persistentPreRunE uses a short
// timeout so the interactive launch chooser is never gated on lock contention
// (bug-504095f2). timeout == 0 preserves the original unbounded behavior for
// the writer daemon and other correctness-sensitive callers.
func EnsureSessionWithTimeout(database *sql.DB, projectDir string, timeout time.Duration) (string, error) {
	sessionID := ResolveSessionID(projectDir)
	info := Detect()

	// Transient sessions (human CLI) skip the database entirely to avoid
	// polluting the sessions table with ephemeral PID-based IDs.
	if strings.HasPrefix(sessionID, "cli-") {
		return sessionID, nil
	}

	ctx := context.Background()

	// dbq is the handle used for the SELECT/INSERT below — the shared pool by
	// default. When a timeout is requested we acquire a dedicated connection and
	// lower its busy_timeout so a contended write lock fails fast (~timeout)
	// rather than stalling for the connection-default busy_timeout (5s).
	// IMPORTANT: modernc.org/sqlite does NOT abort an in-progress busy-wait on
	// context cancellation, so the busy_timeout PRAGMA — not a ctx deadline — is
	// what actually bounds the wait. busy_timeout is per-connection, so we must
	// run the write on the same dedicated conn that received the lowered pragma.
	var dbq interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	} = database
	if timeout > 0 {
		conn, cerr := database.Conn(ctx)
		if cerr != nil {
			return sessionID, cerr
		}
		defer conn.Close()
		// Capture the connection's current busy_timeout so we can restore it
		// before conn.Close() returns this physical connection to the *sql.DB
		// pool. PRAGMA busy_timeout is per-connection state that persists in the
		// pool; without restoration a later op reusing this connection would
		// inherit the short timeout and fail under normal contention.
		var prevBusyMS int64
		if qerr := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&prevBusyMS); qerr != nil {
			return sessionID, qerr
		}
		// Restore runs before conn.Close() (defers are LIFO) and uses a fresh
		// context so it fires even when ctx already hit its deadline.
		defer conn.ExecContext(context.Background(), //nolint:errcheck
			fmt.Sprintf("PRAGMA busy_timeout=%d", prevBusyMS))
		ms := max(timeout.Milliseconds(), 1)
		if _, perr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", ms)); perr != nil {
			return sessionID, perr
		}
		dbq = conn
	}

	// Hot path: session already registered — single indexed PK lookup.
	var count int
	err := dbq.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&count)
	if err != nil {
		return sessionID, err
	}
	if count > 0 {
		os.Setenv("WIPNOTE_SESSION_ID", sessionID) //nolint:errcheck
		return sessionID, nil
	}

	// Cold path: insert a minimal session row so attribution hooks can
	// reference it immediately. INSERT OR IGNORE makes this idempotent.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = dbq.ExecContext(ctx, `
		INSERT OR IGNORE INTO sessions
			(session_id, agent_assigned, created_at, status, model, project_dir, git_remote_url)
		VALUES (?, ?, ?, 'active', ?, ?, ?)`,
		sessionID,
		info.ID,
		now,
		nullableAgentStr(info.Model),
		nullableAgentStr(projectDir),
		nullableAgentStr(paths.GetGitRemoteURL(projectDir)),
	)
	if err != nil {
		return sessionID, err
	}

	// Write .active-session so hook handlers can find the session ID without
	// relying on CLAUDE_ENV_FILE (which may be unset in worktrees / dev mode).
	// NOTE: We duplicate this write locally because internal/agent cannot import
	// internal/hooks (import cycle). Only the fields consumed by readActiveSessionID
	// are required; we populate the full struct for forward-compatibility.
	writeEnsuredActiveSession(sessionID, projectDir, info.ID)

	os.Setenv("WIPNOTE_SESSION_ID", sessionID) //nolint:errcheck
	return sessionID, nil
}

// ensuredActiveSession is the JSON shape written to .wipnote/.active-session
// by EnsureSession. It mirrors hooks.ActiveSessionData to keep the format
// consistent without creating an import dependency.
type ensuredActiveSession struct {
	SessionID     string  `json:"session_id"`
	ParentSession string  `json:"parent_session,omitempty"`
	ParentAgent   string  `json:"parent_agent,omitempty"`
	NestingDepth  int     `json:"nesting_depth"`
	ProjectDir    string  `json:"project_dir,omitempty"`
	GitRemoteURL  string  `json:"git_remote_url,omitempty"`
	Timestamp     float64 `json:"timestamp"`
}

// writeEnsuredActiveSession writes minimal session context to
// .wipnote/.active-session. Errors are silently ignored — this is a
// best-effort propagation mechanism; hook handlers fall back gracefully.
func writeEnsuredActiveSession(sessionID, projectDir, agentID string) {
	if projectDir == "" {
		return
	}
	data := ensuredActiveSession{
		SessionID:     sessionID,
		ParentSession: sessionID,
		ParentAgent:   agentID,
		NestingDepth:  0,
		ProjectDir:    projectDir,
		GitRemoteURL:  paths.GetGitRemoteURL(projectDir),
		Timestamp:     float64(time.Now().UnixNano()) / 1e9,
	}
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	path := filepath.Join(projectDir, ".wipnote", ".active-session")
	_ = os.WriteFile(path, b, 0o644)
}

// nullableAgentStr returns the string value for use in SQL parameters.
// Empty strings are passed through — SQLite will store them as empty TEXT.
// We don't use sql.NullString here to keep the INSERT simple and readable.
func nullableAgentStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
