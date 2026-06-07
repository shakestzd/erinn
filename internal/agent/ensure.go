package agent

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/paths"
)

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
	sessionID := ResolveSessionID(projectDir)
	info := Detect()

	// Transient sessions (human CLI) skip the database entirely to avoid
	// polluting the sessions table with ephemeral PID-based IDs.
	if strings.HasPrefix(sessionID, "cli-") {
		return sessionID, nil
	}

	// Hot path: session already registered — single indexed PK lookup.
	var count int
	err := database.QueryRow(
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
	_, err = database.Exec(`
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
