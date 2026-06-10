package indexer

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
)

// signalLine mirrors the on-disk JSON representation written by ndjson.Sink.
type signalLine struct {
	Kind      string `json:"kind"`
	Harness   string `json:"harness"`
	TS        string `json:"ts"`
	SignalID  string `json:"signal_id"`
	SessionID string `json:"session_id"`
	PromptID  string `json:"prompt_id,omitempty"`

	CanonicalName string `json:"canonical,omitempty"`
	NativeName    string `json:"native,omitempty"`

	TraceID    string `json:"trace_id,omitempty"`
	SpanID     string `json:"span_id,omitempty"`
	ParentSpan string `json:"parent_span,omitempty"`

	ToolName       string `json:"tool_name,omitempty"`
	ToolUseID      string `json:"tool_use_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Decision       string `json:"decision,omitempty"`
	DecisionSource string `json:"decision_source,omitempty"`

	TokensInput         int64 `json:"tokens_input,omitempty"`
	TokensOutput        int64 `json:"tokens_output,omitempty"`
	TokensCacheRead     int64 `json:"tokens_cache_read,omitempty"`
	TokensCacheCreation int64 `json:"tokens_cache_creation,omitempty"`
	TokensThought       int64 `json:"tokens_thought,omitempty"`
	TokensTool          int64 `json:"tokens_tool,omitempty"`
	TokensReasoning     int64 `json:"tokens_reasoning,omitempty"`

	CostUSD    float64 `json:"cost_usd,omitempty"`
	CostSource string  `json:"cost_source,omitempty"`

	DurationMs int64  `json:"duration_ms,omitempty"`
	Success    *bool  `json:"success,omitempty"`
	ErrorMsg   string `json:"error_msg,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`

	ResourceAttrs map[string]any `json:"resource_attrs,omitempty"`
	Attrs         map[string]any `json:"attrs,omitempty"`
}

// parsedSignal bundles a decoded signal with its resource attributes.
type parsedSignal struct {
	Signal        otel.UnifiedSignal
	ResourceAttrs map[string]any
}

// parseLine decodes one NDJSON line into a parsedSignal.
//
// Returns:
//   - (nil, nil) for lines that should be skipped (collector_start, unknown kind).
//   - (nil, err) for malformed JSON.
//   - (*parsedSignal, nil) on success.
func parseLine(data []byte) (*parsedSignal, error) {
	var sl signalLine
	if err := json.Unmarshal(data, &sl); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}

	kind := toKind(sl.Kind)
	if kind == "" {
		return nil, nil
	}

	ts, err := time.Parse(time.RFC3339Nano, sl.TS)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, sl.TS)
		if err != nil {
			return nil, fmt.Errorf("parse ts %q: %w", sl.TS, err)
		}
	}

	return &parsedSignal{
		Signal: otel.UnifiedSignal{
			Kind:           kind,
			Harness:        otel.Harness(sl.Harness),
			Timestamp:      ts,
			SignalID:       sl.SignalID,
			SessionID:      sl.SessionID,
			PromptID:       sl.PromptID,
			CanonicalName:  sl.CanonicalName,
			NativeName:     sl.NativeName,
			TraceID:        sl.TraceID,
			SpanID:         sl.SpanID,
			ParentSpan:     sl.ParentSpan,
			ToolName:       sl.ToolName,
			ToolUseID:      sl.ToolUseID,
			Model:          sl.Model,
			Decision:       sl.Decision,
			DecisionSource: sl.DecisionSource,
			Tokens: otel.TokenCounts{
				Input:         sl.TokensInput,
				Output:        sl.TokensOutput,
				CacheRead:     sl.TokensCacheRead,
				CacheCreation: sl.TokensCacheCreation,
				Thought:       sl.TokensThought,
				Tool:          sl.TokensTool,
				Reasoning:     sl.TokensReasoning,
			},
			CostUSD:    sl.CostUSD,
			CostSource: otel.CostSource(sl.CostSource),
			DurationMs: sl.DurationMs,
			Success:    sl.Success,
			ErrorMsg:   sl.ErrorMsg,
			Attempt:    sl.Attempt,
			StatusCode: sl.StatusCode,
			RawAttrs:   sl.Attrs,
		},
		ResourceAttrs: sl.ResourceAttrs,
	}, nil
}

// toKind maps a raw kind string to otel.Kind.
// Returns "" for non-signal kinds (collector_start, session_start, empty, unknown).
func toKind(s string) otel.Kind {
	switch s {
	case "span":
		return otel.KindSpan
	case "metric":
		return otel.KindMetric
	case "log":
		return otel.KindLog
	default:
		return ""
	}
}
