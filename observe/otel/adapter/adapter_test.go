package adapter

import (
	"math"
	"testing"

	"github.com/shakestzd/wipnote/observe/otel"
)

// fakeAdapter is a test double that claims any resource whose service.name
// equals wantService.
type fakeAdapter struct {
	name        otel.Harness
	wantService string
}

func (f *fakeAdapter) Name() otel.Harness { return f.name }
func (f *fakeAdapter) Identify(res OTLPResource) bool {
	return AttrString(res.Attrs, "service.name") == f.wantService
}
func (f *fakeAdapter) ConvertMetric(res OTLPResource, scope OTLPScope, m OTLPMetric) []otel.UnifiedSignal {
	return nil
}
func (f *fakeAdapter) ConvertLog(res OTLPResource, scope OTLPScope, l OTLPLog) []otel.UnifiedSignal {
	return nil
}
func (f *fakeAdapter) ConvertSpan(res OTLPResource, scope OTLPScope, s OTLPSpan) []otel.UnifiedSignal {
	return nil
}

func TestRegistry_ResolveFirstMatch(t *testing.T) {
	r := NewRegistry()
	claude := &fakeAdapter{name: otel.HarnessClaude, wantService: "claude-code"}
	codex := &fakeAdapter{name: otel.HarnessCodex, wantService: "codex"}
	r.Register(claude)
	r.Register(codex)

	got := r.Resolve(OTLPResource{Attrs: map[string]any{"service.name": "claude-code"}})
	if got != claude {
		t.Errorf("expected claude adapter, got %#v", got)
	}

	got = r.Resolve(OTLPResource{Attrs: map[string]any{"service.name": "codex"}})
	if got != codex {
		t.Errorf("expected codex adapter, got %#v", got)
	}

	got = r.Resolve(OTLPResource{Attrs: map[string]any{"service.name": "unknown"}})
	if got != nil {
		t.Errorf("expected nil for unknown service, got %#v", got)
	}
}

func TestAttrString(t *testing.T) {
	attrs := map[string]any{"s": "hello", "n": 42}
	if got := AttrString(attrs, "s"); got != "hello" {
		t.Errorf("AttrString(s)=%q, want hello", got)
	}
	if got := AttrString(attrs, "n"); got != "" {
		t.Errorf("AttrString(n)=%q, want empty (wrong type)", got)
	}
	if got := AttrString(attrs, "missing"); got != "" {
		t.Errorf("AttrString(missing)=%q, want empty", got)
	}
}

func TestAttrInt64(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]any
		key   string
		want  int64
	}{
		{"int", map[string]any{"k": 42}, "k", 42},
		{"int32", map[string]any{"k": int32(42)}, "k", 42},
		{"int64", map[string]any{"k": int64(42)}, "k", 42},
		{"float64", map[string]any{"k": float64(42.9)}, "k", 42}, // truncation
		{"string_digits", map[string]any{"k": "1420"}, "k", 1420},
		{"string_nondigit", map[string]any{"k": "abc"}, "k", 0},
		{"missing", map[string]any{}, "k", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AttrInt64(tc.attrs, tc.key); got != tc.want {
				t.Errorf("AttrInt64=%d, want %d", got, tc.want)
			}
		})
	}
}

func TestAttrFloat64(t *testing.T) {
	// Exact empirical value from a captured Claude Code cost_usd attribute.
	// Claude emits cost as a string-typed OTLP attribute; AttrFloat64 must
	// round-trip it without strconv pulling in extra allocs.
	attrs := map[string]any{"cost_usd": "0.00804885"}
	got := AttrFloat64(attrs, "cost_usd")
	if math.Abs(got-0.00804885) > 1e-10 {
		t.Errorf("AttrFloat64=%v, want 0.00804885", got)
	}
}
