package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/sessionledger"
)

// RouteSessionInsertFn is retained as a compatibility seam for older callers.
// Production session identity is now written to the canonical session ledger.
var RouteSessionInsertFn func(projectRoot, sessionID, agentID, now, model, projectDir, gitRemoteURL string) bool

// EnsureSession records the current session in the canonical session ledger.
func EnsureSession(_ any, projectDir string) (string, error) {
	return EnsureSessionWithTimeout(nil, projectDir, 0)
}

// EnsureSessionRouted keeps the old split-handle call shape while ignoring the
// obsolete database handles. The ledger write is idempotent and durable.
func EnsureSessionRouted(_ any, _ any, projectDir, _ string, timeout time.Duration) (string, error) {
	return EnsureSessionWithTimeout(nil, projectDir, timeout)
}

// EnsureSessionWithTimeout records the current session in sessions-ledger.html
// and writes .active-session for hook consumers. timeout is accepted for
// compatibility with old database-bound callers.
func EnsureSessionWithTimeout(_ any, projectDir string, _ time.Duration) (string, error) {
	sessionID := ResolveSessionID(projectDir)
	info := Detect()

	if strings.HasPrefix(sessionID, "cli-") {
		return sessionID, nil
	}

	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	record := sessionledger.Record{
		SessionID:  sessionID,
		Harness:    info.ID,
		ProjectDir: paths.NormalizeProjectDir(projectDir),
		StartedAt:  time.Now().UTC(),
	}
	if _, err := sessionledger.NewStore(wipnoteDir).Open(record); err != nil {
		return sessionID, err
	}

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
