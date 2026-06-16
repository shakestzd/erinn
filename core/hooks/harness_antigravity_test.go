package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmitAntigravityResponse_AllowIsEmptyObject guards the fix for agy's strict
// protojson decode: a no-op/allow must be "{}" and must never contain the
// Gemini "continue" field (which agy rejects with `unknown field "continue"`).
func TestEmitAntigravityResponse_AllowIsEmptyObject(t *testing.T) {
	for _, ev := range []string{"PreToolUse", "PostToolUse", "PostInvocation", "Stop", ""} {
		var buf bytes.Buffer
		if err := emitAntigravityResponseForEvent(&buf, ev, &HookResult{Continue: true}); err != nil {
			t.Fatalf("emit(%q): %v", ev, err)
		}
		got := strings.TrimSpace(buf.String())
		if got != "{}" {
			t.Errorf("event %q: expected \"{}\", got %q", ev, got)
		}
		if strings.Contains(got, "continue") {
			t.Errorf("event %q: response must not contain agy-rejected \"continue\" field: %q", ev, got)
		}
	}
}

// TestEmitAntigravityResponse_PreInvocationInjects asserts the orchestrator
// prompt (from WIPNOTE_ANTIGRAVITY_SYSTEM_MD) plus per-prompt additionalContext
// are emitted via injectSteps[].systemMessage in an agy-strict-decodable shape.
func TestEmitAntigravityResponse_PreInvocationInjects(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "sys.md")
	const prompt = "ORCHESTRATOR DIRECTIVE: delegate ALL operations."
	if err := os.WriteFile(promptPath, []byte(prompt+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIPNOTE_ANTIGRAVITY_SYSTEM_MD", promptPath)

	var buf bytes.Buffer
	res := &HookResult{Continue: true, AdditionalContext: "per-prompt CIGS note"}
	if err := emitAntigravityResponseForEvent(&buf, "PreInvocation", res); err != nil {
		t.Fatalf("emit: %v", err)
	}

	// Must strict-decode as an agy hook result: injectSteps[].systemMessage,
	// no unknown fields (mirrors agy's protojson decoder).
	var parsed struct {
		InjectSteps []struct {
			SystemMessage string `json:"systemMessage"`
		} `json:"injectSteps"`
	}
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		t.Fatalf("response not strict-decodable as agy hook result: %v\n%s", err, buf.String())
	}
	if len(parsed.InjectSteps) != 1 {
		t.Fatalf("expected 1 inject step, got %d: %s", len(parsed.InjectSteps), buf.String())
	}
	sm := parsed.InjectSteps[0].SystemMessage
	if !strings.Contains(sm, prompt) {
		t.Errorf("systemMessage missing orchestrator prompt: %q", sm)
	}
	if !strings.Contains(sm, "per-prompt CIGS note") {
		t.Errorf("systemMessage missing additionalContext: %q", sm)
	}
	if strings.Contains(buf.String(), "\"continue\"") {
		t.Errorf("response must not contain agy-rejected \"continue\": %s", buf.String())
	}
}

// TestEmitAntigravityResponse_PreInvocationNoContent: with nothing to inject,
// PreInvocation falls back to the empty-object allow.
func TestEmitAntigravityResponse_PreInvocationNoContent(t *testing.T) {
	t.Setenv("WIPNOTE_ANTIGRAVITY_SYSTEM_MD", "")
	var buf bytes.Buffer
	if err := emitAntigravityResponseForEvent(&buf, "PreInvocation", &HookResult{Continue: true}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "{}" {
		t.Errorf("PreInvocation with no inject content should be {}, got %q", buf.String())
	}
}
