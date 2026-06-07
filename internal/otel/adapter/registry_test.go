package adapter_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/harness"
	"github.com/shakestzd/wipnote/internal/otel"
	"github.com/shakestzd/wipnote/internal/otel/adapter"
)

// TestAdapterIdentify_Gemini_FromRegistry verifies that GeminiAdapter.Identify
// returns true for every service.name listed in the registry and false for
// unknown names.
func TestAdapterIdentify_Gemini_FromRegistry(t *testing.T) {
	cfg := harness.Get(string(otel.HarnessGemini))
	if cfg == nil {
		t.Fatal("harness.Get(HarnessGemini) returned nil; registry not initialized")
	}

	a := adapter.NewGeminiAdapter()

	for _, svc := range cfg.ServiceNames {
		svc := svc
		t.Run("matches/"+svc, func(t *testing.T) {
			res := adapter.OTLPResource{Attrs: map[string]any{"service.name": svc}}
			if !a.Identify(res) {
				t.Errorf("GeminiAdapter.Identify returned false for registry service name %q", svc)
			}
		})
	}

	t.Run("rejects unknown", func(t *testing.T) {
		res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "not-a-real-service"}}
		if a.Identify(res) {
			t.Error("GeminiAdapter.Identify returned true for unknown service name")
		}
	})
}

// TestAdapterIdentify_Claude_FromRegistry verifies that ClaudeAdapter.Identify
// returns true for every service.name listed in the registry.
func TestAdapterIdentify_Claude_FromRegistry(t *testing.T) {
	cfg := harness.Get(string(otel.HarnessClaude))
	if cfg == nil {
		t.Fatal("harness.Get(HarnessClaude) returned nil; registry not initialized")
	}

	a := adapter.NewClaudeAdapter()

	for _, svc := range cfg.ServiceNames {
		svc := svc
		t.Run("matches/"+svc, func(t *testing.T) {
			res := adapter.OTLPResource{Attrs: map[string]any{"service.name": svc}}
			if !a.Identify(res) {
				t.Errorf("ClaudeAdapter.Identify returned false for registry service name %q", svc)
			}
		})
	}

	t.Run("rejects unknown", func(t *testing.T) {
		res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "not-a-real-service"}}
		if a.Identify(res) {
			t.Error("ClaudeAdapter.Identify returned true for unknown service name")
		}
	})
}

// TestAdapterIdentify_Codex_FromRegistry verifies that CodexAdapter.Identify
// returns true for every service.name listed in the registry (both codex-cli
// and codex_cli_rs).
func TestAdapterIdentify_Codex_FromRegistry(t *testing.T) {
	cfg := harness.Get(string(otel.HarnessCodex))
	if cfg == nil {
		t.Fatal("harness.Get(HarnessCodex) returned nil; registry not initialized")
	}

	a := adapter.NewCodexAdapter()

	for _, svc := range cfg.ServiceNames {
		svc := svc
		t.Run("matches/"+svc, func(t *testing.T) {
			res := adapter.OTLPResource{Attrs: map[string]any{"service.name": svc}}
			if !a.Identify(res) {
				t.Errorf("CodexAdapter.Identify returned false for registry service name %q", svc)
			}
		})
	}

	t.Run("rejects unknown", func(t *testing.T) {
		res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "not-a-real-service"}}
		if a.Identify(res) {
			t.Error("CodexAdapter.Identify returned true for unknown service name")
		}
	})
}

// TestAdapterSessionID_FromRegistry_Gemini verifies that the Gemini adapter
// resolves the session ID using the registry's SessionAttr value ("session.id").
func TestAdapterSessionID_FromRegistry_Gemini(t *testing.T) {
	cfg := harness.Get(string(otel.HarnessGemini))
	if cfg == nil {
		t.Fatal("harness.Get(HarnessGemini) returned nil")
	}

	a := adapter.NewGeminiAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "gemini-cli"}}
	scope := adapter.OTLPScope{Name: "test"}
	ts := time.Unix(0, 1_735_000_000_000_000_000)

	wantSession := "gemini-registry-session-id"
	m := adapter.OTLPMetric{
		Name:      "gemini_cli.token.usage",
		Kind:      adapter.MetricKindCounter,
		Timestamp: ts,
		Value:     100,
		Attrs: map[string]any{
			cfg.SessionAttr: wantSession,
		},
	}

	sigs := a.ConvertMetric(res, scope, m)
	if len(sigs) == 0 {
		t.Fatal("ConvertMetric returned 0 signals")
	}
	if sigs[0].SessionID != wantSession {
		t.Errorf("SessionID = %q, want %q (from registry SessionAttr=%q)", sigs[0].SessionID, wantSession, cfg.SessionAttr)
	}
}

// TestAdapterSessionID_FromRegistry_Claude verifies that the Claude adapter
// resolves the session ID using the registry's SessionAttr ("session.id").
func TestAdapterSessionID_FromRegistry_Claude(t *testing.T) {
	cfg := harness.Get(string(otel.HarnessClaude))
	if cfg == nil {
		t.Fatal("harness.Get(HarnessClaude) returned nil")
	}

	a := adapter.NewClaudeAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "claude-code"}}
	scope := adapter.OTLPScope{Name: "test"}
	ts := time.Unix(0, 1_735_000_000_000_000_000)

	wantSession := "claude-registry-session-id"
	m := adapter.OTLPMetric{
		Name:      "claude_code.session.count",
		Kind:      adapter.MetricKindCounter,
		Timestamp: ts,
		Value:     1,
		Attrs: map[string]any{
			cfg.SessionAttr: wantSession,
		},
	}

	sigs := a.ConvertMetric(res, scope, m)
	if len(sigs) == 0 {
		t.Fatal("ConvertMetric returned 0 signals")
	}
	if sigs[0].SessionID != wantSession {
		t.Errorf("SessionID = %q, want %q (from registry SessionAttr=%q)", sigs[0].SessionID, wantSession, cfg.SessionAttr)
	}
}

// TestAdapterSessionID_FromRegistry_Codex verifies that the Codex adapter
// resolves the session ID using the registry's SessionAttr ("conversation.id").
func TestAdapterSessionID_FromRegistry_Codex(t *testing.T) {
	cfg := harness.Get(string(otel.HarnessCodex))
	if cfg == nil {
		t.Fatal("harness.Get(HarnessCodex) returned nil")
	}

	a := adapter.NewCodexAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "codex-cli"}}
	scope := adapter.OTLPScope{Name: "test"}
	ts := time.Unix(0, 1_735_000_000_000_000_000)

	wantSession := "codex-registry-conv-id"
	m := adapter.OTLPMetric{
		Name:      "codex.session.count",
		Kind:      adapter.MetricKindCounter,
		Timestamp: ts,
		Value:     1,
		Attrs: map[string]any{
			cfg.SessionAttr: wantSession,
		},
	}

	sigs := a.ConvertMetric(res, scope, m)
	if len(sigs) == 0 {
		t.Fatal("ConvertMetric returned 0 signals")
	}
	if sigs[0].SessionID != wantSession {
		t.Errorf("SessionID = %q, want %q (from registry SessionAttr=%q)", sigs[0].SessionID, wantSession, cfg.SessionAttr)
	}
}
