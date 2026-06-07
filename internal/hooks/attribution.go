package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/paths"
)

// agentTraceFormatVersion is the pinned Agent Trace RFC version.
// Increment when breaking changes are made to the record format.
const agentTraceFormatVersion = "0.1.0"

// agentTraceRecord represents an Agent Trace contributor record written to the
// temp queue so subagent sessions can claim their parent linkage at session-start.
// Aligned with the Agent Trace RFC for cross-tool interoperability (Cursor,
// Cloudflare, Vercel, Google Jules, Git AI). format_version is included for
// forward compatibility as the RFC evolves.
type agentTraceRecord struct {
	FormatVersion   string    `json:"format_version"`             // "0.1.0" — pinned RFC version
	TraceID         string    `json:"trace_id"`                   // Session/trace identifier
	SpanID          string    `json:"span_id,omitempty"`          // This contribution's span ID
	ParentSpanID    string    `json:"parent_span_id,omitempty"`   // Parent span (delegation chain)
	ContributorID   string    `json:"contributor_id,omitempty"`   // Agent identifier (e.g. "claude-code")
	ContributorType string    `json:"contributor_type,omitempty"` // "ai_agent" | "human" | "tool"
	ToolName        string    `json:"tool_name,omitempty"`        // Tool used (e.g. "Write", "Edit")
	SessionID       string    `json:"session_id,omitempty"`       // wipnote session ID
	Timestamp       time.Time `json:"timestamp"`                  // When the contribution occurred
	Claimed         bool      `json:"claimed"`                    // Whether this entry has been claimed
}

// timestampUnix returns the record's timestamp as a Unix epoch float for
// age comparisons, matching the format previously stored as float64.
func (r *agentTraceRecord) timestampUnix() float64 {
	return float64(r.Timestamp.UnixNano()) / 1e9
}

// writeTraceparent writes an Agent Trace record to the temp queue directory.
// Mirrors the Python write_traceparent_queue() helper in pretooluse.py.
func writeTraceparent(parentSessionID, parentEventID string) {
	queueDir := filepath.Join(os.TempDir(), "wipnote-traceparent")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		return
	}

	record := agentTraceRecord{
		FormatVersion: agentTraceFormatVersion,
		TraceID:       parentSessionID,
		ParentSpanID:  parentEventID,
		Timestamp:     time.Now().UTC(),
		Claimed:       false,
	}

	data, err := json.Marshal(record)
	if err != nil {
		return
	}

	filename := fmt.Sprintf("tp-%s.json", uuid.New().String()[:8])
	path := filepath.Join(queueDir, filename)
	_ = os.WriteFile(path, data, 0o644)
}

// legacyTraceparent is the old format used before Agent Trace adoption.
// Used for backward-compatible reading of pre-existing queue files.
type legacyTraceparent struct {
	TraceID      string  `json:"trace_id"`
	ParentSpanID string  `json:"parent_span_id"`
	Timestamp    float64 `json:"timestamp"`
	Claimed      bool    `json:"claimed"`
}

// parseTraceparentFile reads a queue file, handling both the new Agent Trace
// format and the legacy format for backward compatibility.
func parseTraceparentFile(data []byte) (*agentTraceRecord, bool) {
	// Try new format first (presence of format_version distinguishes it).
	var rec agentTraceRecord
	if err := json.Unmarshal(data, &rec); err == nil && rec.FormatVersion != "" {
		return &rec, true
	}
	// Fall back to legacy format.
	var legacy legacyTraceparent
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, false
	}
	return &agentTraceRecord{
		FormatVersion: "",
		TraceID:       legacy.TraceID,
		ParentSpanID:  legacy.ParentSpanID,
		Timestamp:     time.Unix(0, int64(legacy.Timestamp*1e9)).UTC(),
		Claimed:       legacy.Claimed,
	}, true
}

// claimTraceparent reads and claims the most recent unclaimed traceparent
// from the temp queue. Returns nil if nothing is available or entries are stale.
// Mirrors claim_traceparent() in session-start.py.
func claimTraceparent() *agentTraceRecord {
	queueDir := filepath.Join(os.TempDir(), "wipnote-traceparent")
	entries, err := filepath.Glob(filepath.Join(queueDir, "tp-*.json"))
	if err != nil || len(entries) == 0 {
		return nil
	}

	now := float64(time.Now().UnixNano()) / 1e9
	var best *agentTraceRecord
	var bestPath string

	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		record, ok := parseTraceparentFile(data)
		if !ok {
			continue
		}
		age := now - record.timestampUnix()
		if record.Claimed || age > 30 {
			if age > 300 {
				// Clean up stale entries older than 5 minutes.
				_ = os.Remove(path)
			}
			continue
		}
		// Prefer the most recent unclaimed entry.
		if best == nil || record.timestampUnix() > best.timestampUnix() {
			best = record
			bestPath = path
		}
	}

	if best == nil || bestPath == "" {
		return nil
	}

	// Claim it atomically by rewriting with claimed=true.
	best.Claimed = true
	if data, err := json.Marshal(best); err == nil {
		_ = os.WriteFile(bestPath, data, 0o644)
	}
	return best
}

// writeSubagentEnvVars writes WIPNOTE_PARENT_EVENT, WIPNOTE_AGENT_ID,
// WIPNOTE_AGENT_TYPE, WIPNOTE_CONTRIBUTOR_TYPE, and OTEL_RESOURCE_ATTRIBUTES
// to CLAUDE_ENV_FILE so the subagent's hooks know their parent delegation,
// agent identity, and Agent Trace contributor classification.
//
// OTEL_RESOURCE_ATTRIBUTES is merged with any existing value so the subagent's
// OTel SDK emits wipnote.agent_id on every span — the OTLP receiver uses this
// to look up pending_subagent_starts and synthesize a placeholder row.
//
// When CLAUDE_ENV_FILE is unset (worktree subagents), falls back to a
// session-scoped temp-file hint so the subagent's hook processes can still
// resolve the project directory via paths.ReadSessionHint.
func writeSubagentEnvVars(parentEventID, agentID, agentType, projectDir, sessionID string) {
	// Always persist subagent identity to the per-subagent hint file so that
	// PreToolUse/PostToolUse hook subprocesses can resolve agent_id and
	// parent_event_id even when CLAUDE_ENV_FILE is unset.
	writeSubagentIdentityHint(sessionID, agentID, parentEventID)

	envFile := os.Getenv("CLAUDE_ENV_FILE")
	if envFile == "" {
		// CLAUDE_ENV_FILE is unset in worktree subagents. Parent linkage is
		// handled by the traceparent queue (writeTraceparent is called by the
		// SubagentStart handler before this function). Write the project dir
		// to a session-scoped temp file so downstream hook processes can still
		// resolve .wipnote/ when their EventCWD is a temp directory.
		debugLog(projectDir, "[wipnote] CLAUDE_ENV_FILE unset — writing session-scoped project dir hint (agent=%s session=%s)", agentType, sessionID)
		writeSessionProjectDirHint(sessionID, projectDir)
		return
	}
	f, err := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		debugLog(projectDir, "[wipnote] failed to open CLAUDE_ENV_FILE %s: %v", envFile, err)
		return
	}
	defer f.Close()

	lines := fmt.Sprintf(
		"export WIPNOTE_PARENT_EVENT=%s\nexport WIPNOTE_AGENT_ID=%s\nexport WIPNOTE_AGENT_TYPE=%s\nexport WIPNOTE_CONTRIBUTOR_TYPE=ai_agent\n",
		parentEventID, agentID, agentType,
	)
	// Also propagate the project directory so subagent hook invocations can
	// resolve .wipnote/ even when their EventCWD is a temp dir.
	if projectDir != "" {
		lines += "export WIPNOTE_PROJECT_DIR=" + projectDir + "\n"
	}
	// Merge wipnote.agent_id into OTEL_RESOURCE_ATTRIBUTES so the subagent's
	// OTel SDK emits this attribute on every span. Merge with any existing value
	// so we don't clobber other resource attributes set by the harness or user.
	if agentID != "" {
		otelRA := mergeOTELResourceAttrs(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), "wipnote.agent_id="+agentID)
		lines += "export OTEL_RESOURCE_ATTRIBUTES=" + otelRA + "\n"
	}
	f.WriteString(lines)
}

// mergeOTELResourceAttrs merges a new key=value pair into an existing
// OTEL_RESOURCE_ATTRIBUTES value. The OTel spec uses comma-separated
// key=value pairs. If existing already contains the key, the new value wins
// (appended; OTel SDKs use last-wins semantics). If existing is empty, the
// new pair is returned as-is.
func mergeOTELResourceAttrs(existing, newPair string) string {
	if existing == "" {
		return newPair
	}
	return existing + "," + newPair
}

// writeSessionProjectDirHint persists projectDir to a session-scoped temp file
// so that future hook processes (running in subagent temp dirs) can read it via
// paths.ReadSessionHint when WIPNOTE_PROJECT_DIR is not in their env.
func writeSessionProjectDirHint(sessionID, projectDir string) {
	paths.WriteSessionHint(sessionID, projectDir)
}

// writeSubagentIdentityHint persists the subagent's agent_id and
// parent_event_id to a per-subagent hint file. This is the fallback path
// used when CLAUDE_ENV_FILE is unset, ensuring PreToolUse/PostToolUse hook
// subprocesses can always resolve their attribution context.
func writeSubagentIdentityHint(sessionID, agentID, parentEventID string) {
	paths.WriteSubagentHint(sessionID, agentID, parentEventID)
}

// ApplyTraceparent reads a traceparent from the queue and exports env vars
// for parent session / parent event linkage. Called during session-start.
func ApplyTraceparent() (parentSession, parentEvent string) {
	tp := claimTraceparent()
	if tp == nil {
		return "", ""
	}
	if tp.TraceID != "" {
		os.Setenv("WIPNOTE_PARENT_SESSION", tp.TraceID)
	}
	if tp.ParentSpanID != "" {
		os.Setenv("WIPNOTE_PARENT_EVENT", tp.ParentSpanID)
	}
	return tp.TraceID, tp.ParentSpanID
}
