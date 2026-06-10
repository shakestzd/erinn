package pricing_test

import (
	"math"
	"testing"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/pricing"
)

// TestDefaultLoadable ensures the embedded models.json parses cleanly.
func TestDefaultLoadable(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatalf("load default table: %v", err)
	}
	if got := len(tbl.Models()); got < 10 {
		t.Errorf("default table has %d models, expected at least 10", got)
	}
}

// TestClaudeCostEmpiricalMatch verifies our derivation matches Claude
// Code's vendor-reported cost_usd exactly on three empirical data points
// captured from a live `claude -p` OTLP run. Any drift here means the
// embedded rates diverged from reality, which would silently mis-cost
// every non-Claude harness that depends on derived values.
func TestClaudeCostEmpiricalMatch(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// All three rows captured from the empirical TRACEPARENT test:
	// model=claude-haiku-4-5-20251001, dimensions as reported by
	// claude_code.api_request log events.
	cases := []struct {
		name         string
		tokens       otel.TokenCounts
		wantCost     float64
		tolerancePct float64
	}{
		{
			name:     "turn1_api_request",
			tokens:   otel.TokenCounts{Input: 10, Output: 577, CacheRead: 23276, CacheCreation: 2261},
			wantCost: 0.00804885,
			// Empirical data should match to 1% per Phase 0 acceptance criteria.
			tolerancePct: 0.01,
		},
		{
			name:         "turn2_api_request",
			tokens:       otel.TokenCounts{Input: 3, Output: 87, CacheRead: 0, CacheCreation: 16623},
			wantCost:     0.02121675,
			tolerancePct: 0.01,
		},
		{
			name:         "turn3_api_request",
			tokens:       otel.TokenCounts{Input: 5, Output: 101, CacheRead: 16623, CacheCreation: 888},
			wantCost:     0.0032823,
			tolerancePct: 0.01,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, src, ok := tbl.Derive("claude-haiku-4-5-20251001", tc.tokens)
			if !ok {
				t.Fatalf("Derive returned ok=false for known model")
			}
			if src != otel.CostSourceDerived {
				t.Errorf("CostSource=%q, want %q", src, otel.CostSourceDerived)
			}
			diff := math.Abs(got - tc.wantCost)
			tolerance := tc.wantCost * tc.tolerancePct
			if diff > tolerance {
				t.Errorf("Derive(%+v) = %v, want %v (diff %v exceeds %v = %.1f%%)",
					tc.tokens, got, tc.wantCost, diff, tolerance, tc.tolerancePct*100)
			}
		})
	}
}

// TestLookupDateStampFallback ensures dated Anthropic variants resolve to
// their undated canonical when the exact date isn't in the table.
func TestLookupDateStampFallback(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// A future dated variant that we haven't pinned should fall back to
	// the undated canonical. Tests the strip-trailing-date normalization.
	p, ok := tbl.Lookup("claude-sonnet-4-6-99991231")
	if !ok {
		t.Fatal("dated variant did not fall back to canonical")
	}
	base, _ := tbl.Lookup("claude-sonnet-4-6")
	if p.InputCostPerToken != base.InputCostPerToken {
		t.Errorf("fallback returned different rates: %v vs %v", p, base)
	}
}

// TestLookupUnknownModel returns ok=false so callers know to record
// CostSourceUnknown rather than silently producing $0.
func TestLookupUnknownModel(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := tbl.Lookup("not-a-real-model-7"); ok {
		t.Error("unknown model returned ok=true")
	}
	cost, src, ok := tbl.Derive("not-a-real-model-7", otel.TokenCounts{Input: 1, Output: 1})
	if ok {
		t.Error("Derive(unknown) returned ok=true")
	}
	if cost != 0 {
		t.Errorf("Derive(unknown) cost=%v, want 0", cost)
	}
	if src != otel.CostSourceUnknown {
		t.Errorf("Derive(unknown) source=%q, want %q", src, otel.CostSourceUnknown)
	}
}

// TestLoadValidatesRates rejects negative rates — a pricing-update
// round-trip that produces garbage should fail loud, not silently mis-cost.
func TestLoadValidatesRates(t *testing.T) {
	bad := []byte(`{"m":{"provider":"x","input_cost_per_token":-1,"output_cost_per_token":1}}`)
	if _, err := pricing.Load(bad); err == nil {
		t.Error("Load accepted negative rate")
	}
}

// TestDeriveGeminiThoughtTokens verifies Gemini-specific Thought dimension
// is billed at output rate, matching Google's published behavior.
func TestDeriveGeminiThoughtTokens(t *testing.T) {
	tbl, err := pricing.Default()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// 1000 thought tokens at gemini-2.5-pro output rate = 1000 * 0.00001 = 0.01.
	got, _, ok := tbl.Derive("gemini-2.5-pro", otel.TokenCounts{Thought: 1000})
	if !ok {
		t.Fatal("lookup failed")
	}
	if math.Abs(got-0.01) > 1e-9 {
		t.Errorf("thought-only cost = %v, want 0.01", got)
	}
}
