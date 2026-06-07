package otel_test

import (
	"testing"

	"github.com/shakestzd/wipnote/core/harness"
	"github.com/shakestzd/wipnote/internal/otel"
)

// TestHarnessIDsMatchOtelConsts bridges otel's Harness constants and the core
// harness registry. It lives here, not in core/harness, because core must not
// import otel — otel may import core/harness (feat-0e3f1b3f). Moved from the
// former internal/harness/registry_test.go cross-check.
func TestHarnessIDsMatchOtelConsts(t *testing.T) {
	tests := []struct {
		otelConst otel.Harness
		harnessID string
	}{
		{otel.HarnessClaude, "claude_code"},
		{otel.HarnessCodex, "codex"},
		{otel.HarnessGemini, "gemini_cli"},
	}

	for _, tt := range tests {
		if string(tt.otelConst) != tt.harnessID {
			t.Errorf("otel const = %q, want %q", string(tt.otelConst), tt.harnessID)
		}
		cfg := harness.Get(tt.harnessID)
		if cfg == nil {
			t.Errorf("harness.Get(%q) returned nil", tt.harnessID)
			continue
		}
		if cfg.ID != string(tt.otelConst) {
			t.Errorf("harness.Get(%q).ID = %q, want %q (otel const)", tt.harnessID, cfg.ID, string(tt.otelConst))
		}
	}
}
