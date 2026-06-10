package adapter_test

// TestAdapterConformance verifies that every adapter (Claude, Codex, Gemini)
// correctly populates the three canonical fields — Harness, SessionID, and
// CanonicalName — across metric, log, and span paths.
//
// Canonical attribute mapping by harness:
//
//	Claude  service.name=claude-code   session.id        → SessionID
//	Codex   service.name=codex-cli     conversation.id   → SessionID
//	Gemini  service.name=gemini-cli    session.id        → SessionID
//	PromptID: Claude prompt.id | Codex synthesized from conversation.id+seq | Gemini gen_ai.prompt_id

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/adapter"
)

// adapterCase parameterises one adapter under test.
type adapterCase struct {
	name       string
	adapter    adapter.Adapter
	harness    otel.Harness
	res        adapter.OTLPResource
	sessionKey string // the signal-level attr key that holds the session ID
	sessionVal string // the session ID value we inject and expect back
}

func conformanceCases() []adapterCase {
	return []adapterCase{
		{
			name:    "Claude",
			adapter: adapter.NewClaudeAdapter(),
			harness: otel.HarnessClaude,
			res: adapter.OTLPResource{Attrs: map[string]any{
				"service.name":    "claude-code",
				"service.version": "2.1.42",
			}},
			sessionKey: "session.id",
			sessionVal: "claude-session-abc",
		},
		{
			name:    "Codex",
			adapter: adapter.NewCodexAdapter(),
			harness: otel.HarnessCodex,
			res: adapter.OTLPResource{Attrs: map[string]any{
				"service.name":    "codex-cli",
				"service.version": "0.1.0",
			}},
			sessionKey: "conversation.id",
			sessionVal: "codex-conv-xyz",
		},
		{
			name:    "Gemini",
			adapter: adapter.NewGeminiAdapter(),
			harness: otel.HarnessGemini,
			res: adapter.OTLPResource{Attrs: map[string]any{
				"service.name":    "gemini-cli",
				"service.version": "0.1.0",
			}},
			sessionKey: "session.id",
			sessionVal: "gemini-session-def",
		},
	}
}

// TestAdapterConformance_Identify checks that each adapter correctly
// identifies its own resource and rejects foreign ones.
func TestAdapterConformance_Identify(t *testing.T) {
	for _, tc := range conformanceCases() {
		tc := tc
		t.Run(tc.name+"/Identify", func(t *testing.T) {
			if !tc.adapter.Identify(tc.res) {
				t.Errorf("%s: Identify returned false for own resource", tc.name)
			}
		})
	}

	// cross-rejection: each adapter must not claim another's resource
	cases := conformanceCases()
	for i, tc := range cases {
		tc := tc
		for j, other := range cases {
			if i == j {
				continue
			}
			other := other
			t.Run(tc.name+"/RejectsForeign/"+other.name, func(t *testing.T) {
				if tc.adapter.Identify(other.res) {
					t.Errorf("%s incorrectly claimed %s resource", tc.name, other.name)
				}
			})
		}
	}
}

func TestCodexAdapterIdentify_DefaultServiceName(t *testing.T) {
	codex := adapter.NewCodexAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{
		"service.name": "codex_cli_rs",
	}}
	if !codex.Identify(res) {
		t.Fatalf("Codex adapter did not identify Codex's default service.name")
	}
}

// TestAdapterConformance_Metric checks the metric path populates Harness,
// SessionID, and CanonicalName.
func TestAdapterConformance_Metric(t *testing.T) {
	ts := time.Unix(0, 1_735_000_000_000_000_000)
	scope := adapter.OTLPScope{Name: "conformance.test"}

	for _, tc := range conformanceCases() {
		tc := tc
		t.Run(tc.name+"/Metric", func(t *testing.T) {
			m := adapter.OTLPMetric{
				Name:      "test.metric",
				Kind:      adapter.MetricKindCounter,
				Timestamp: ts,
				Value:     1,
				Attrs: map[string]any{
					tc.sessionKey: tc.sessionVal,
				},
			}
			sigs := tc.adapter.ConvertMetric(tc.res, scope, m)
			if len(sigs) == 0 {
				t.Fatalf("%s: ConvertMetric returned 0 signals", tc.name)
			}
			assertCanonicalFields(t, tc.name+"/metric", sigs[0], tc.harness, tc.sessionVal)
		})
	}
}

// TestAdapterConformance_Log checks the log path populates Harness,
// SessionID, and CanonicalName.
func TestAdapterConformance_Log(t *testing.T) {
	ts := time.Unix(0, 1_735_000_000_000_000_000)
	scope := adapter.OTLPScope{Name: "conformance.test"}

	for _, tc := range conformanceCases() {
		tc := tc
		t.Run(tc.name+"/Log", func(t *testing.T) {
			l := adapter.OTLPLog{
				Name:      "test.event",
				Timestamp: ts,
				Attrs: map[string]any{
					tc.sessionKey: tc.sessionVal,
				},
			}
			sigs := tc.adapter.ConvertLog(tc.res, scope, l)
			if len(sigs) == 0 {
				t.Fatalf("%s: ConvertLog returned 0 signals", tc.name)
			}
			assertCanonicalFields(t, tc.name+"/log", sigs[0], tc.harness, tc.sessionVal)
		})
	}
}

// TestAdapterConformance_Span checks the span path populates Harness,
// SessionID, and CanonicalName.
func TestAdapterConformance_Span(t *testing.T) {
	ts := time.Unix(0, 1_735_000_000_000_000_000)
	scope := adapter.OTLPScope{Name: "conformance.test"}

	for _, tc := range conformanceCases() {
		tc := tc
		t.Run(tc.name+"/Span", func(t *testing.T) {
			s := adapter.OTLPSpan{
				Name:      "test.span",
				TraceID:   "a4e28f48fbdb6644a92b208f2145aee1",
				SpanID:    "7d7f9ea011223344",
				StartTime: ts,
				EndTime:   ts.Add(100 * time.Millisecond),
				Attrs: map[string]any{
					tc.sessionKey: tc.sessionVal,
				},
			}
			sigs := tc.adapter.ConvertSpan(tc.res, scope, s)
			if len(sigs) == 0 {
				t.Fatalf("%s: ConvertSpan returned 0 signals", tc.name)
			}
			assertCanonicalFields(t, tc.name+"/span", sigs[0], tc.harness, tc.sessionVal)
		})
	}
}

// TestAdapterConformance_SessionIDResourceFallback verifies that every
// adapter falls back to the resource-level session attribute when the
// signal-level attribute is absent.
func TestAdapterConformance_SessionIDResourceFallback(t *testing.T) {
	ts := time.Unix(0, 1_735_000_000_000_000_000)
	scope := adapter.OTLPScope{Name: "conformance.test"}

	for _, tc := range conformanceCases() {
		tc := tc
		t.Run(tc.name+"/SessionFallback", func(t *testing.T) {
			// Resource carries the session key; signal attrs are empty.
			res := adapter.OTLPResource{Attrs: make(map[string]any, len(tc.res.Attrs)+1)}
			for k, v := range tc.res.Attrs {
				res.Attrs[k] = v
			}
			res.Attrs[tc.sessionKey] = tc.sessionVal + "-from-resource"

			m := adapter.OTLPMetric{
				Name:      "test.metric",
				Kind:      adapter.MetricKindCounter,
				Timestamp: ts,
				Value:     1,
				Attrs:     map[string]any{}, // no session key here
			}
			sigs := tc.adapter.ConvertMetric(res, scope, m)
			if len(sigs) == 0 {
				t.Fatalf("%s: ConvertMetric returned 0 signals", tc.name)
			}
			want := tc.sessionVal + "-from-resource"
			if sigs[0].SessionID != want {
				t.Errorf("%s: SessionID = %q, want %q (resource fallback)", tc.name, sigs[0].SessionID, want)
			}
		})
	}
}

// assertCanonicalFields is the shared assertion for Harness, SessionID,
// and CanonicalName on every signal produced by any adapter.
func assertCanonicalFields(t *testing.T, label string, sig otel.UnifiedSignal, wantHarness otel.Harness, wantSession string) {
	t.Helper()
	if sig.Harness != wantHarness {
		t.Errorf("%s: Harness = %q, want %q", label, sig.Harness, wantHarness)
	}
	if sig.SessionID != wantSession {
		t.Errorf("%s: SessionID = %q, want %q", label, sig.SessionID, wantSession)
	}
	if sig.CanonicalName == "" {
		t.Errorf("%s: CanonicalName is empty", label)
	}
}
