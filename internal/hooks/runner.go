// Package hooks implements Claude Code hook handlers for HtmlGraph.
//
// Each handler reads a CloudEvent JSON payload from stdin and writes a
// HookResult JSON to stdout. The Go binary replaces the Python hook scripts,
// eliminating the ~500ms uv cold-start per invocation.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shakestzd/htmlgraph/internal/agent"
	"github.com/shakestzd/htmlgraph/internal/paths"
	"github.com/shakestzd/htmlgraph/internal/storage"
)

// CloudEvent is the JSON payload Claude Code sends to every hook via stdin.
// Only the fields HtmlGraph actually uses are decoded; the rest are ignored.
type CloudEvent struct {
	// Top-level fields common to all hook types
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"` // "default", "plan", "auto", "bypassPermissions"

	// UserPromptSubmit
	Prompt string `json:"prompt"`

	// PreToolUse / PostToolUse
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	ToolUseID string         `json:"tool_use_id"`

	// PostToolUse result
	ToolResult map[string]any `json:"tool_result"`

	// SubagentStart / SubagentStop
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`

	// WorktreeCreate / WorktreeRemove
	WorktreePath string `json:"worktree_path"`

	// Stop / SubagentStop
	StopReason           string `json:"stop_reason"`
	LastAssistantMessage string `json:"last_assistant_message"`

	// SessionStart / SessionEnd / Stop — common session fields
	TranscriptPath string `json:"transcript_path"`
	Source         string `json:"source"`   // startup, resume, clear, compact
	Model          string `json:"model"`

	// SessionEnd
	Reason   string `json:"reason"`    // prompt_input_exit, interrupt, etc.
	ExitCode int    `json:"exit_code"`

	// TaskCreated / TaskCompleted
	TaskID   string         `json:"task_id"`
	TaskData map[string]any `json:"task"`

	// Agent Teams — teammate metadata (experimental, gracefully empty when not in a team)
	TeammateName    string `json:"teammate_name"`
	TeamName        string `json:"team_name"`
	IdleReason      string `json:"idle_reason"`
	TaskSubject     string `json:"task_subject"`
	TaskDescription string `json:"task_description"`
}

// HookResult is the JSON written to stdout to control Claude Code behaviour.
// Fields are omitted when empty to keep the payload minimal.
type HookResult struct {
	Continue          bool   `json:"continue,omitempty"`
	Decision          string `json:"decision,omitempty"`           // "allow" | "deny" | "block"
	Reason            string `json:"reason,omitempty"`
	Message           string `json:"message,omitempty"`            // shown on stderr
	AdditionalContext string `json:"additionalContext,omitempty"`  // injected into conversation
}

// ReadRawStdin reads all bytes from stdin without parsing. This is used by the
// harness-routing layer in runHookNamed to inspect the raw payload before
// choosing a dialect-specific parser.
func ReadRawStdin() ([]byte, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}
	return data, nil
}

// ReadInput reads and parses a CloudEvent from stdin.
func ReadInput() (*CloudEvent, error) {
	ev, _, err := ReadInputRaw()
	return ev, err
}

// ReadInputRaw reads stdin and returns both the raw bytes and the parsed
// CloudEvent. Use this when you need to preserve the original payload
// (e.g., for tracing or forwarding).
func ReadInputRaw() (*CloudEvent, []byte, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, nil, fmt.Errorf("reading stdin: %w", err)
	}
	if len(data) == 0 {
		return &CloudEvent{}, data, nil
	}
	var ev CloudEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, data, fmt.Errorf("parsing CloudEvent: %w", err)
	}
	return &ev, data, nil
}

// WriteResult encodes result as JSON to stdout.
func WriteResult(result *HookResult) error {
	return json.NewEncoder(os.Stdout).Encode(result)
}

// Allow writes an empty JSON object to allow the tool to proceed.
// NOTE: We intentionally return {} instead of {"decision":"allow"} because
// Claude Code v2.1.x displays a spurious "hook error" label in the TUI
// when a PreToolUse hook returns {"decision":"allow"}. An empty object
// is treated as "no opinion" which defaults to allow without the error label.
func Allow() error {
	return Empty()
}

// Continue writes a continue:true response (used by non-blocking hooks).
func Continue() error {
	return WriteResult(&HookResult{Continue: true})
}

// Empty writes an empty JSON object (hook has no opinion).
func Empty() error {
	_, err := fmt.Fprintln(os.Stdout, "{}")
	return err
}

// ResolveProjectDir finds the project directory containing .htmlgraph/.
// Delegates to paths.ResolveProjectDir with the CloudEvent CWD and a
// walk-up limit of defaultProjectDirWalkLevels (matching the previous hook behaviour).
// sessionID enables session-scoped hint lookup; pass "" when no event is available.
func ResolveProjectDir(cwd, sessionID string) string {
	dir, _ := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   cwd,
		WalkLevels: defaultProjectDirWalkLevels,
		SessionID:  sessionID,
	})
	return dir
}

// IsHtmlGraphProject returns true when the project directory has a .htmlgraph/ dir.
func IsHtmlGraphProject(projectDir string) bool {
	_, err := os.Stat(filepath.Join(projectDir, ".htmlgraph"))
	return err == nil
}

// DBPath returns the canonical SQLite path for the given project directory.
// Delegates to storage.CanonicalDBPath so the DB always lives in the host
// OS cache dir (never inside the project tree), ensuring WAL/SHM mmap
// works regardless of the project filesystem (virtiofs, NFS, FUSE, etc.).
//
// Returns an error when os.UserCacheDir() fails. There is intentionally no
// silent fallback to a project-local path: a fallback caused bug-62f14f8c
// where the indexer wrote to ~/.cache/htmlgraph/<hash>/htmlgraph.db while
// the YOLO PreToolUse gate read .htmlgraph/.db/htmlgraph.db, leaving the
// gate's view of agent_events permanently stale. Callers must propagate
// the error (typically by skipping the hook with the configured fallback).
func DBPath(projectDir string) (string, error) {
	return storage.CanonicalDBPath(projectDir)
}

// NormaliseSessionID extracts a UUID from a path-style session_id that Claude
// Code sometimes provides for subagent sessions. Delegates to agent package.
// Kept here as a package-level alias so existing hooks callers are unchanged.
func NormaliseSessionID(raw string) string {
	return agent.NormaliseSessionID(raw)
}

// EnvSessionID returns the current session ID using a three-step fallback:
//  1. CloudEvent session_id (always correct for hook invocations)
//  2. HTMLGRAPH_SESSION_ID env var (for CLI commands without a CloudEvent)
//  3. .htmlgraph/.active-session file (last resort for edge cases)
func EnvSessionID(eventSessionID string) string {
	// CloudEvent session_id is always correct for this hook invocation.
	// It takes priority over the env var, which can be overwritten by a
	// concurrent subagent's writeEnvVars call.
	if sid := agent.NormaliseSessionID(eventSessionID); sid != "" {
		return sid
	}
	// Env var fallback — used by CLI commands that don't have a CloudEvent.
	if v := os.Getenv("HTMLGRAPH_SESSION_ID"); v != "" {
		return v
	}
	// Last resort: .active-session file.
	cwd, _ := os.Getwd()
	projectDir := ResolveProjectDir(cwd, "")
	if projectDir != "" {
		if as := ReadActiveSession(projectDir); as != nil && as.SessionID != "" {
			return as.SessionID
		}
	}
	return ""
}

// resolveSessionIDWithHarness resolves the session ID using harness-aware logic.
// All harnesses prefer the CloudEvent.SessionID from the payload and avoid env
// var fallback when the payload carries a session_id. This prevents
// HTMLGRAPH_SESSION_ID leaking from a parent Claude orchestrator shell into
// Task-spawned subagent hook invocations (bug fixed for Claude here; Codex and
// Gemini already had this protection).
func resolveSessionIDWithHarness(event *CloudEvent) string {
	// For all harnesses: if the payload carries a session_id, trust it and
	// don't fall back to env vars which may have leaked from parent Claude shell.
	if sid := agent.NormaliseSessionID(event.SessionID); sid != "" {
		return sid
	}

	// Payload session_id is absent — only allowed for CLI commands that call
	// SessionStart without a real CloudEvent (no hook invocation).
	// Codex/Gemini subagents never reach here (their payloads always carry
	// session_id); Claude CLI commands may.
	if event.AgentID == "codex" || event.AgentID == "gemini" {
		// Harness-specific session_id is missing (unusual); don't leak env.
		return ""
	}

	// Claude only: fall back to env → file for CLI commands without a payload.
	return EnvSessionID("")
}
