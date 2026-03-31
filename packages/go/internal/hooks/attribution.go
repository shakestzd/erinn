package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
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
	FormatVersion   string    `json:"format_version"`           // "0.1.0" — pinned RFC version
	TraceID         string    `json:"trace_id"`                 // Session/trace identifier
	SpanID          string    `json:"span_id,omitempty"`        // This contribution's span ID
	ParentSpanID    string    `json:"parent_span_id,omitempty"` // Parent span (delegation chain)
	ContributorID   string    `json:"contributor_id,omitempty"` // Agent identifier (e.g. "claude-code")
	ContributorType string    `json:"contributor_type,omitempty"` // "ai_agent" | "human" | "tool"
	ToolName        string    `json:"tool_name,omitempty"`      // Tool used (e.g. "Write", "Edit")
	SessionID       string    `json:"session_id,omitempty"`     // HtmlGraph session ID
	Timestamp       time.Time `json:"timestamp"`                // When the contribution occurred
	Claimed         bool      `json:"claimed"`                  // Whether this entry has been claimed
}

// timestampUnix returns the record's timestamp as a Unix epoch float for
// age comparisons, matching the format previously stored as float64.
func (r *agentTraceRecord) timestampUnix() float64 {
	return float64(r.Timestamp.UnixNano()) / 1e9
}

// writeTraceparent writes an Agent Trace record to the temp queue directory.
// Mirrors the Python write_traceparent_queue() helper in pretooluse.py.
func writeTraceparent(parentSessionID, parentEventID string) {
	queueDir := filepath.Join(os.TempDir(), "htmlgraph-traceparent")
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
	queueDir := filepath.Join(os.TempDir(), "htmlgraph-traceparent")
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

// writeSubagentEnvVars writes HTMLGRAPH_PARENT_EVENT, HTMLGRAPH_AGENT_ID,
// HTMLGRAPH_AGENT_TYPE, and HTMLGRAPH_CONTRIBUTOR_TYPE to CLAUDE_ENV_FILE so
// the subagent's hooks know their parent delegation, agent identity, and
// Agent Trace contributor classification.
// When CLAUDE_ENV_FILE is unset (worktree subagents), falls back gracefully:
// the traceparent queue covers parent linkage.
func writeSubagentEnvVars(parentEventID, agentID, agentType, projectDir string) {
	envFile := os.Getenv("CLAUDE_ENV_FILE")
	if envFile == "" {
		// CLAUDE_ENV_FILE is unset in worktree subagents. Parent linkage is
		// handled by the traceparent queue (writeTraceparent is called by the
		// SubagentStart handler before this function). The .active-session file
		// covers session ID propagation for downstream hooks.
		debugLog(projectDir, "[htmlgraph] CLAUDE_ENV_FILE unset — subagent env vars not written (agent=%s), traceparent queue covers parent linkage", agentType)
		return
	}
	f, err := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		debugLog(projectDir, "[htmlgraph] failed to open CLAUDE_ENV_FILE %s: %v", envFile, err)
		return
	}
	defer f.Close()

	lines := fmt.Sprintf(
		"export HTMLGRAPH_PARENT_EVENT=%s\nexport HTMLGRAPH_AGENT_ID=%s\nexport HTMLGRAPH_AGENT_TYPE=%s\nexport HTMLGRAPH_CONTRIBUTOR_TYPE=ai_agent\n",
		parentEventID, agentID, agentType,
	)
	f.WriteString(lines)
}

// ApplyTraceparent reads a traceparent from the queue and exports env vars
// for parent session / parent event linkage. Called during session-start.
func ApplyTraceparent() (parentSession, parentEvent string) {
	tp := claimTraceparent()
	if tp == nil {
		return "", ""
	}
	if tp.TraceID != "" {
		os.Setenv("HTMLGRAPH_PARENT_SESSION", tp.TraceID)
	}
	if tp.ParentSpanID != "" {
		os.Setenv("HTMLGRAPH_PARENT_EVENT", tp.ParentSpanID)
	}
	return tp.TraceID, tp.ParentSpanID
}
