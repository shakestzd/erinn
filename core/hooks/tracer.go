package hooks

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

const traceFile = "/tmp/wipnote-hook-trace.jsonl"

// traceRecord is the JSONL schema written per hook invocation.
type traceRecord struct {
	TS          string            `json:"ts"`
	Subcommand  string            `json:"subcommand"`
	RawPayload  json.RawMessage   `json:"raw_payload"`
	Resolved    traceResolved     `json:"resolved"`
	EnvSnapshot map[string]string `json:"env_snapshot"`
}

type traceResolved struct {
	SessionID       string `json:"session_id"`
	AgentID         string `json:"agent_id"`
	ToolName        string `json:"tool_name"`
	IsSubagent      bool   `json:"is_subagent"`
	ParentSessionID string `json:"parent_session_id"`
	ParentEventID   string `json:"parent_event_id"`
}

// TruncateTraceFile removes the trace file so each new session starts fresh.
// Silently ignores errors.
func TruncateTraceFile() {
	_ = os.Remove(traceFile)
}

// TraceInvocation appends a JSONL line to /tmp/wipnote-hook-trace.jsonl.
// It never causes a hook to fail — all errors are silently swallowed.
func TraceInvocation(subcommand string, rawPayload []byte, parsed *CloudEvent) {
	defer func() { recover() }() //nolint:errcheck

	var raw json.RawMessage
	if len(rawPayload) > 0 {
		raw = json.RawMessage(rawPayload)
	} else {
		raw = json.RawMessage("{}")
	}

	var ev CloudEvent
	if parsed != nil {
		ev = *parsed
	}

	// Build env snapshot: include WIPNOTE_, CLAUDE_, ANTHROPIC_ prefixes.
	// Redact vars whose name contains TOKEN, KEY, SECRET, or PASSWORD.
	redactWords := []string{"TOKEN", "KEY", "SECRET", "PASSWORD"}
	snap := make(map[string]string)
	for _, kv := range os.Environ() {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		name := kv[:idx]
		value := kv[idx+1:]
		upper := strings.ToUpper(name)
		if !strings.HasPrefix(upper, "WIPNOTE_") &&
			!strings.HasPrefix(upper, "CLAUDE_") &&
			!strings.HasPrefix(upper, "ANTHROPIC_") {
			continue
		}
		for _, word := range redactWords {
			if strings.Contains(upper, word) {
				value = "<redacted>"
				break
			}
		}
		snap[name] = value
	}

	// Determine if this is a subagent by checking AgentID.
	isSubagent := ev.AgentID != "" && ev.AgentID != ev.SessionID

	rec := traceRecord{
		TS:         time.Now().UTC().Format(time.RFC3339),
		Subcommand: subcommand,
		RawPayload: raw,
		Resolved: traceResolved{
			SessionID:       ev.SessionID,
			AgentID:         ev.AgentID,
			ToolName:        ev.ToolName,
			IsSubagent:      isSubagent,
			ParentSessionID: os.Getenv("WIPNOTE_PARENT_SESSION"),
			ParentEventID:   os.Getenv("WIPNOTE_PARENT_EVENT"),
		},
		EnvSnapshot: snap,
	}

	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b = append(b, '\n')

	f, err := os.OpenFile(traceFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(b)
}
