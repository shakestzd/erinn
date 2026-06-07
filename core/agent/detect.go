// Package agent provides agent identity detection and session ID resolution
// for the wipnote CLI and hook subsystems.
//
// It is intentionally dependency-free (no internal imports) so that both the
// hook handlers and CLI commands can import it without creating import cycles.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Info describes the detected calling agent.
type Info struct {
	// ID is the agent identifier: "claude-code", "human", or a custom value
	// from WIPNOTE_AGENT_ID.
	ID string

	// Model is the model name from CLAUDE_MODEL, or empty if not set.
	Model string
}

// uuidPattern matches RFC 4122 UUID format (8-4-4-4-12).
// Pre-compiled at module load to avoid repeated compilation.
var uuidPattern = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)

// Detect returns the identity of the calling agent using env var priority:
//  1. WIPNOTE_AGENT_ID  — explicit override (e.g. "codex", "copilot")
//  2. CLAUDE_CODE=1 / CLAUDECODE=1 — running inside Claude Code → "claude-code"
//  3. fallback            — "human" (CLI user, not an AI agent)
//
// Model is always read from CLAUDE_MODEL (empty string if unset).
func Detect() Info {
	id := detectID()
	return Info{
		ID:    id,
		Model: os.Getenv("CLAUDE_MODEL"),
	}
}

func detectID() string {
	if v := os.Getenv("WIPNOTE_AGENT_ID"); v != "" {
		return v
	}
	// Claude Code 2.x sets CLAUDECODE=1 (no underscore); older builds set
	// CLAUDE_CODE=1. Accept either so agent_id never silently falls back to
	// "human" — that fallback collides with the agent_events CHECK constraint
	// `NOT (event_type='tool_call' AND agent_id='human' AND tool_name != 'UserQuery')`,
	// which silently rejects every Read/Bash/Edit insert and blinds the
	// research-first yolo guard.
	if os.Getenv("CLAUDE_CODE") != "" || os.Getenv("CLAUDECODE") != "" {
		return "claude-code"
	}
	return "human"
}

// ResolveSessionID returns the current session ID using a four-step fallback:
//  1. WIPNOTE_SESSION_ID env var (set by writeEnvVars via CLAUDE_ENV_FILE)
//  2. CLAUDE_SESSION_ID env var (normalised — Claude Code path-style IDs)
//  3. .wipnote/.active-session file in projectDir
//  4. Generated "cli-<pid>-<unix>" for plain CLI invocations
func ResolveSessionID(projectDir string) string {
	if v := os.Getenv("WIPNOTE_SESSION_ID"); v != "" {
		return v
	}
	if v := os.Getenv("CLAUDE_SESSION_ID"); v != "" {
		return NormaliseSessionID(v)
	}
	if projectDir != "" {
		if sid := readActiveSessionID(projectDir); sid != "" {
			return sid
		}
	}
	return fmt.Sprintf("cli-%d-%d", os.Getpid(), time.Now().Unix())
}

// NormaliseSessionID extracts a UUID from a path-style session_id that Claude
// Code sometimes provides for subagent sessions, e.g.:
//
//	/mock/claude-501/-Users-testuser-/550e8400-e29b-41d4-a716-446655440000
//
// If no UUID is found, or the input has no slash, the original string is
// returned unchanged.
func NormaliseSessionID(raw string) string {
	if raw == "" || !containsSlash(raw) {
		return raw
	}
	if m := uuidPattern.FindString(raw); m != "" {
		return m
	}
	return raw
}

func containsSlash(s string) bool {
	for _, c := range s {
		if c == '/' {
			return true
		}
	}
	return false
}

// activeSessionFile is the minimal JSON shape read from .wipnote/.active-session.
// We duplicate just the fields we need here to avoid importing internal/hooks
// (which would create an import cycle).
type activeSessionFile struct {
	SessionID string `json:"session_id"`
}

// readActiveSessionID reads the session_id field from .wipnote/.active-session.
// Returns empty string when the file doesn't exist or can't be parsed.
func readActiveSessionID(projectDir string) string {
	path := filepath.Join(projectDir, ".wipnote", ".active-session")
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f activeSessionFile
	if err := json.Unmarshal(b, &f); err != nil {
		return ""
	}
	return f.SessionID
}
