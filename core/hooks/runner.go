// Package hooks implements Claude Code hook handlers for wipnote.
//
// Most handlers read a CloudEvent JSON payload from stdin and write a
// HookResult JSON to stdout. Replacement hooks such as WorktreeCreate have
// dedicated command wiring for their raw stdout contracts. The Go binary
// replaces the Python hook scripts, eliminating the ~500ms uv cold-start per
// invocation.
package hooks

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/agent"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/storage"
)

// Runner bundles the optional dependencies needed by in-process hook
// invocations (e.g. `wipnote claude` / `wipnote yolo` embedding the hook
// dispatch loop without spawning subprocesses).
//
// SLICE-7 CONTRACT (plan-ae0c37b2 / feat-33c26c74):
//
//	Subprocess hooks construct a zero-value Runner: Queue is nil, so any
//	derived-index op falls back to the synchronous DB path. In-process
//	callers pass a *writequeue.Queue obtained from the slice-6 writer
//	service so their derived-index updates serialize through the queue
//	worker (the single SQLite writer per project DB).
//
// All fields are optional. A nil Runner is the canonical-only-mode default
// for legacy callers and tests.
type Runner struct {
	// Queue is the slice-6 write queue. Optional. When non-nil, hook
	// handlers route their derived-index updates through it via
	// SubmitDerivedOp; when nil, ops run synchronously against the DB
	// handle (subprocess hooks) or are skipped (canonical-only paths).
	Queue *writequeue.Queue

	// DB is the writable SQLite handle that handlers use for synchronous
	// queries/updates when the queue is unavailable. Optional.
	DB *sql.DB
}

// NewRunner constructs a Runner with the given optional dependencies.
// Both arguments are nil-safe and individually optional; the most common
// in-process configuration is (queue, db) where db is the read pool shared
// with the dashboard.
func NewRunner(queue *writequeue.Queue, database *sql.DB) *Runner {
	return &Runner{Queue: queue, DB: database}
}

// CloudEvent is the JSON payload Claude Code sends to every hook via stdin.
// Only the fields wipnote actually uses are decoded; the rest are ignored.
type CloudEvent struct {
	// Top-level fields common to all hook types
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"` // "default", "plan", "auto", "bypassPermissions"
	Timestamp      string `json:"timestamp"`

	// UserPromptSubmit
	Prompt string `json:"prompt"`

	// Harness turn correlation / final response fields.
	TurnID         string `json:"turn_id"`
	PromptResponse string `json:"prompt_response"`

	// AfterModel Gemini-specific fields (populated by parseGeminiEvent for AfterModel events).
	LLMRequest  map[string]any `json:"llm_request,omitempty"`
	LLMResponse map[string]any `json:"llm_response,omitempty"`

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
	WorktreeName     string `json:"worktree_name"`
	WorktreeBasePath string `json:"worktree_base_path"`
	WorktreePath     string `json:"worktree_path"`

	// Stop / SubagentStop
	StopReason           string `json:"stop_reason"`
	LastAssistantMessage string `json:"last_assistant_message"`

	// SessionStart / SessionEnd / Stop — common session fields
	TranscriptPath string `json:"transcript_path"`
	Source         string `json:"source"` // startup, resume, clear, compact
	Model          string `json:"model"`

	// SessionEnd
	Reason   string `json:"reason"` // prompt_input_exit, interrupt, etc.
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

	// PermissionRequest — controlling event. CC asks the hook whether to allow
	// or deny a tool/permission request. The tool being gated is carried in
	// ToolName/ToolInput (shared with PreToolUse). These classifier fields let
	// the handler make a conservative, low-risk-only auto-allow decision.
	//   permission_category: CC's own categorisation of the request
	//     (e.g. "read", "write", "execute", "network", …).
	//   risk_level: CC's risk assessment (e.g. "low", "medium", "high").
	PermissionCategory string `json:"permission_category"`
	RiskLevel          string `json:"risk_level"`

	// HookEventName carries the harness-native hook_event_name from the incoming
	// payload, populated by parseCodexEvent, parseGeminiEvent, and
	// parseAntigravityEvent. It is absent (empty string) for Claude payloads,
	// which do not include a top-level hook_event_name that handlers need to
	// echo back. hookEventNameForResponse echoes this field when present so
	// that Gemini/Antigravity receive their own native event names (BeforeAgent,
	// BeforeTool, AfterTool) rather than Claude canonical names.
	HookEventName string `json:"hook_event_name,omitempty"`

	// PreCompact — controlling event. CC is about to compact the conversation
	// context. compaction_trigger distinguishes a user-initiated compaction
	// ("manual") from an automatic one fired on context pressure ("auto").
	// context_stats carries CC's pre-compaction context accounting (token
	// counts, message counts, etc.); shape is harness-defined so it is decoded
	// as a free-form map and recorded verbatim for observability.
	CompactionTrigger string         `json:"compaction_trigger"`
	ContextStats      map[string]any `json:"context_stats"`

	// InstructionsLoaded — observational event. CC has loaded an instruction
	// file (CLAUDE.md, AGENTS.md, a memory file, a glob-matched ruleset, …).
	//   file_path:   absolute path of the loaded instruction file.
	//   load_reason: why CC loaded it (e.g. "startup", "import", "memory").
	//   memory_type: classification of the memory source (e.g. "project",
	//                "user", "local").
	//   globs:       glob patterns that selected the file (when applicable).
	FilePath   string   `json:"file_path"`
	LoadReason string   `json:"load_reason"`
	MemoryType string   `json:"memory_type"`
	Globs      []string `json:"globs"`
}

// HookResult is the JSON written to stdout to control Claude Code behaviour.
// Fields are omitted when empty to keep the payload minimal.
type HookResult struct {
	Continue          bool   `json:"continue,omitempty"`
	Decision          string `json:"decision,omitempty"` // "allow" | "deny" | "block"
	Reason            string `json:"reason,omitempty"`
	Message           string `json:"message,omitempty"`           // shown on stderr
	AdditionalContext string `json:"additionalContext,omitempty"` // injected into conversation

	// HookSpecificOutput carries the newer per-event structured response shape
	// Claude Code introduced for controlling events (verified against
	// https://code.claude.com/docs/en/hooks, 2026-05-29). It is a pointer so
	// the field is omitted entirely (omitempty) for every existing emitter that
	// does not set it — preserving the historical empty-object "{}" = allow
	// contract. Today only PermissionRequest populates it (decision.behavior =
	// "allow" | "deny"); other controlling events continue to use the top-level
	// Decision/Reason fields.
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput is Claude Code's per-event structured response envelope.
// Only the fields wipnote emits are modelled.
//
// For PermissionRequest, CC reads hookSpecificOutput.decision.behavior to
// decide the permission outcome:
//   - behavior == "allow": the request is auto-approved without a user prompt.
//   - behavior == "deny":  the request is auto-denied.
//   - decision absent (nil): CC falls through to its normal permission prompt.
//
// wipnote NEVER auto-denies (that could block legitimate work); it only ever
// emits behavior == "allow" for a tightly-scoped, reviewable allowlist of
// read-only operations, and otherwise omits HookSpecificOutput entirely so CC
// prompts as usual.
type HookSpecificOutput struct {
	// HookEventName echoes the event name back to the harness for structured hook
	// responses such as PermissionRequest, PreToolUse, and BeforeAgent.
	HookEventName string `json:"hookEventName,omitempty"`
	// Decision carries the Claude Code PermissionRequest behavior verdict. Nil ⇒ no opinion (prompt).
	Decision *PermissionDecision `json:"decision,omitempty"`
	// AdditionalContext is model-visible context appended by Codex/Gemini-style hooks.
	AdditionalContext string `json:"additionalContext,omitempty"`
	// PermissionDecision is Codex's PreToolUse permission verdict.
	PermissionDecision string `json:"permissionDecision,omitempty"`
	// PermissionDecisionReason is Codex's PreToolUse denial reason.
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// PermissionDecision is the decision body inside HookSpecificOutput for the
// PermissionRequest event. Behavior is "allow" or "deny" per the CC contract.
type PermissionDecision struct {
	Behavior string `json:"behavior,omitempty"`
	// Message is an optional human-readable explanation surfaced by CC.
	Message string `json:"message,omitempty"`
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

// ResolveProjectDir finds the project directory containing .wipnote/.
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

// IswipnoteProject returns true when the project directory has a .wipnote/ dir.
func IswipnoteProject(projectDir string) bool {
	_, err := os.Stat(filepath.Join(projectDir, ".wipnote"))
	return err == nil
}

// DBPath returns the canonical SQLite path for the given project directory.
// Delegates to storage.CanonicalDBPath so the DB always lives in the host
// OS cache dir (never inside the project tree), ensuring WAL/SHM mmap
// works regardless of the project filesystem (virtiofs, NFS, FUSE, etc.).
//
// Returns an error when os.UserCacheDir() fails. There is intentionally no
// silent fallback to a project-local path: a fallback caused bug-62f14f8c
// where the indexer wrote to ~/.cache/wipnote/<hash>/wipnote.db while
// the YOLO PreToolUse gate read .wipnote/.db/wipnote.db, leaving the
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

// harnessNativeEnvSessionID returns the live session/thread ID stamped by the
// current harness launcher. WIPNOTE_HARNESS is set to the harness name by the
// wipnote launcher (codex, gemini, antigravity). When present, the harness-
// native ID is preferred over WIPNOTE_SESSION_ID because the latter may be
// inherited (stale) from a parent Claude orchestrator shell (issue #144).
// Returns "" when no harness-native ID is found or when running under Claude
// (where WIPNOTE_SESSION_ID is always current via writeEnvVars).
func harnessNativeEnvSessionID() string {
	harness := strings.ToLower(strings.TrimSpace(os.Getenv("WIPNOTE_HARNESS")))
	switch harness {
	case "codex":
		return strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
	case "gemini":
		return strings.TrimSpace(os.Getenv("GEMINI_SESSION_ID"))
	case "antigravity":
		return strings.TrimSpace(os.Getenv("ANTIGRAVITY_SESSION_ID"))
	}
	return ""
}

// EnvSessionID returns the current session ID using a four-step fallback:
//  1. CloudEvent session_id (always correct for hook invocations)
//  2. Harness-native live ID (CODEX_THREAD_ID / GEMINI_SESSION_ID /
//     ANTIGRAVITY_SESSION_ID), preferred over WIPNOTE_SESSION_ID when a
//     non-Claude harness launcher is detected via WIPNOTE_HARNESS, because
//     WIPNOTE_SESSION_ID may carry a stale ID inherited from a parent Claude
//     orchestrator shell (issue #144).
//  3. WIPNOTE_SESSION_ID env var (for CLI commands without a CloudEvent)
//  4. .wipnote/.active-session file (last resort for edge cases)
func EnvSessionID(eventSessionID string) string {
	// CloudEvent session_id is always correct for this hook invocation.
	// It takes priority over the env var, which can be overwritten by a
	// concurrent subagent's writeEnvVars call.
	if sid := agent.NormaliseSessionID(eventSessionID); sid != "" {
		return sid
	}
	// Prefer harness-native live ID over a (possibly stale) WIPNOTE_SESSION_ID.
	if v := harnessNativeEnvSessionID(); v != "" {
		return v
	}
	// Env var fallback — used by CLI commands that don't have a CloudEvent.
	if v := os.Getenv("WIPNOTE_SESSION_ID"); v != "" {
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
// WIPNOTE_SESSION_ID leaking from a parent Claude orchestrator shell into
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
