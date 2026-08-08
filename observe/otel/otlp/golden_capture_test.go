package otlp_test

import (
	"os"
	"testing"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/adapter"
	"github.com/shakestzd/wipnote/observe/otel/otlp"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// TestGoldenCapture_ClaudeAdapter is bug-735806ff's answer to the drift-
// detection gap the Weaver investigation ruled out: run wipnote's OWN
// decode + adapter pipeline against a REAL, redacted OTLP/HTTP JSON capture
// (testdata/golden_capture/ — see PROVENANCE.md there for what produced it,
// when, and how to refresh it) instead of the Go-constructed synthetic spans
// every other adapter test uses.
//
// This is the test that would have caught bug-5652a5ba directly: a real
// capture carries a real tool_use_id on every tool-family span and log
// record, so an adapter conversion that forgets to promote it produces a
// visible, specific assertion failure here — a hand-built Go literal test
// fixture can omit a field forever without anyone noticing, which is
// exactly what happened to that column for its entire lifetime.
//
// Each assertion names the field it's checking, per the design intent: a
// future harness release that renames or drops an attribute this test
// depends on must fail loudly and specifically, not as a generic "test
// failed."
func TestGoldenCapture_ClaudeAdapter(t *testing.T) {
	signals := decodeGoldenCapture(t)

	byCanonicalKind := func(canonical string, kind otel.Kind) []otel.UnifiedSignal {
		var out []otel.UnifiedSignal
		for _, s := range signals {
			if s.CanonicalName == canonical && s.Kind == kind {
				out = append(out, s)
			}
		}
		return out
	}
	one := func(t *testing.T, canonical string, kind otel.Kind) otel.UnifiedSignal {
		t.Helper()
		matches := byCanonicalKind(canonical, kind)
		if len(matches) != 1 {
			t.Fatalf("canonical=%s kind=%s: got %d signals, want exactly 1 (capture may have changed shape — see PROVENANCE.md)",
				canonical, kind, len(matches))
		}
		return matches[0]
	}

	// claude_code.tool.execution span: the beta success source bug-a0143c2c
	// is about. Must carry ToolUseID (bug-5652a5ba) and a real Success.
	toolExec := one(t, otel.CanonicalToolExecution, otel.KindSpan)
	if toolExec.ToolUseID == "" {
		t.Error("tool.execution span: ToolUseID is empty, want a real tool_use_id (bug-5652a5ba regression)")
	}
	if toolExec.Success == nil {
		t.Error("tool.execution span: Success is nil, want true")
	} else if !*toolExec.Success {
		t.Error("tool.execution span: Success = false, want true (this capture's Read call succeeded)")
	}
	if toolExec.DurationMs <= 0 {
		t.Errorf("tool.execution span: DurationMs = %d, want > 0", toolExec.DurationMs)
	}

	// claude_code.llm_request span: the ONE beta source with no non-beta
	// alternative (bug-a0143c2c) — highest-risk input, must still resolve.
	llmReq := one(t, otel.CanonicalAPIRequest, otel.KindSpan)
	if llmReq.Model == "" {
		t.Error("llm_request span: Model is empty, want a real model name")
	}
	if llmReq.Success == nil || !*llmReq.Success {
		t.Errorf("llm_request span: Success = %v, want true", llmReq.Success)
	}
	if llmReq.Tokens.Input == 0 && llmReq.Tokens.Output == 0 {
		t.Error("llm_request span: Tokens.Input and Tokens.Output are both 0, want the real captured token counts")
	}

	// claude_code.tool_result log: the STABLE, non-beta source for tool
	// outcomes (bug-a0143c2c). Must share ToolUseID with the span above —
	// this is the real cross-record join the cross-validation logic in
	// ClaudeAdapter.CrossValidateToolOutcomes depends on being possible.
	toolResult := one(t, otel.CanonicalToolResult, otel.KindLog)
	if toolResult.ToolUseID == "" {
		t.Error("tool_result log: ToolUseID is empty, want a real tool_use_id (bug-5652a5ba regression)")
	} else if toolResult.ToolUseID != toolExec.ToolUseID {
		t.Errorf("tool_result log ToolUseID (%q) does not match tool.execution span ToolUseID (%q) — "+
			"they describe the same real tool call in this capture and must correlate",
			toolResult.ToolUseID, toolExec.ToolUseID)
	}
	if toolResult.Success == nil || !*toolResult.Success {
		t.Errorf("tool_result log: Success = %v, want true", toolResult.Success)
	}

	// claude_code.tool_decision log: also carries ToolUseID (the second
	// carrier bug-5652a5ba's fix had to add after the first pass missed it).
	toolDecision := one(t, otel.CanonicalToolDecision, otel.KindLog)
	if toolDecision.ToolUseID == "" {
		t.Error("tool_decision log: ToolUseID is empty, want a real tool_use_id (bug-5652a5ba regression)")
	}
	if toolDecision.Decision != "accept" {
		t.Errorf("tool_decision log: Decision = %q, want %q", toolDecision.Decision, "accept")
	}
	if toolDecision.DecisionSource != otel.DecisionSourceConfig {
		t.Errorf("tool_decision log: DecisionSource = %q, want %q", toolDecision.DecisionSource, otel.DecisionSourceConfig)
	}

	// claude_code.api_request log: vendor-reported cost, and the structural
	// success inference bug-53941397 asked to be marked explicitly.
	apiRequest := one(t, otel.CanonicalAPIRequest, otel.KindLog)
	if apiRequest.CostUSD <= 0 {
		t.Errorf("api_request log: CostUSD = %v, want > 0", apiRequest.CostUSD)
	}
	if apiRequest.CostSource != otel.CostSourceVendor {
		t.Errorf("api_request log: CostSource = %q, want %q", apiRequest.CostSource, otel.CostSourceVendor)
	}
	if apiRequest.Success == nil || !*apiRequest.Success {
		t.Errorf("api_request log: Success = %v, want true (structural inference — reaching this event implies success)", apiRequest.Success)
	}
	if got, _ := apiRequest.RawAttrs[adapter.SuccessSourceAttrKey].(string); got != adapter.SuccessSourceStructural {
		t.Errorf("api_request log: %s = %q, want %q (bug-53941397)", adapter.SuccessSourceAttrKey, got, adapter.SuccessSourceStructural)
	}

	// claude_code.token.usage metric: the harness fans a single Sum metric
	// out into one data point per dimension; each must route to the right
	// TokenCounts field.
	tokenMetrics := byCanonicalKind(otel.CanonicalTokenUsage, otel.KindMetric)
	var gotInput, gotOutput, gotCacheRead, gotCacheCreation, gotCost bool
	for _, m := range tokenMetrics {
		switch {
		case m.Tokens.Input > 0:
			gotInput = true
		case m.Tokens.Output > 0:
			gotOutput = true
		case m.Tokens.CacheRead > 0:
			gotCacheRead = true
		case m.Tokens.CacheCreation > 0:
			gotCacheCreation = true
		case m.CostUSD > 0:
			gotCost = true
		}
	}
	for name, ok := range map[string]bool{
		"Tokens.Input":         gotInput,
		"Tokens.Output":        gotOutput,
		"Tokens.CacheRead":     gotCacheRead,
		"Tokens.CacheCreation": gotCacheCreation,
		"CostUSD":              gotCost,
	} {
		if !ok {
			t.Errorf("token/cost usage metrics: no data point routed to %s, want one from the captured claude_code.token.usage/cost.usage metrics", name)
		}
	}
}

// decodeGoldenCapture reads the three testdata files and runs them through
// wipnote's real decode + adapter pipeline, mirroring the ConvertAll loop in
// observe/otel/receiver/http.go and observe/otel/convert/convert.go without
// importing either (this test only needs decode+convert, not the SignalID
// derivation or batching those packages add on top).
func decodeGoldenCapture(t *testing.T) []otel.UnifiedSignal {
	t.Helper()
	a := adapter.NewClaudeAdapter()
	var signals []otel.UnifiedSignal

	var traces tracepb.TracesData
	unmarshalGolden(t, "testdata/golden_capture/traces.json", &traces)
	for _, d := range otlp.DecodeTraces(traces.GetResourceSpans()) {
		for _, ss := range d.Spans {
			signals = append(signals, a.ConvertSpan(d.Resource, ss.Scope, ss.Span)...)
		}
	}

	var logs logspb.LogsData
	unmarshalGolden(t, "testdata/golden_capture/logs.json", &logs)
	for _, d := range otlp.DecodeLogs(logs.GetResourceLogs()) {
		for _, sl := range d.Logs {
			signals = append(signals, a.ConvertLog(d.Resource, sl.Scope, sl.Log)...)
		}
	}

	var metrics metricspb.MetricsData
	unmarshalGolden(t, "testdata/golden_capture/metrics.json", &metrics)
	for _, d := range otlp.DecodeMetrics(metrics.GetResourceMetrics()) {
		for _, sm := range d.Metrics {
			signals = append(signals, a.ConvertMetric(d.Resource, sm.Scope, sm.Metric)...)
		}
	}

	if len(signals) == 0 {
		t.Fatal("decoded zero signals from the golden capture — testdata files may be empty or malformed")
	}
	return signals
}

// unmarshalGolden parses one OTLP/HTTP JSON testdata file into out, mirroring
// the same protojson call cmd/wipnote/otel_collect_handler.go's unmarshalOTLP
// uses for the application/json content-type path — this fixture was
// captured over exactly that wire format (see PROVENANCE.md).
func unmarshalGolden(t *testing.T, path string, out proto.Message) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(body, out); err != nil {
		t.Fatalf("parse %s as OTLP/HTTP JSON: %v", path, err)
	}
}
