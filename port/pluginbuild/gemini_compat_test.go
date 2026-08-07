package pluginbuild

import (
	"strings"
	"testing"
)

// These tests cover the Gemini-CLI-derived translation helpers retained in
// gemini_compat.go for the Antigravity adapter after the Gemini harness target
// was retired (feat-02f25a24). The gemini adapter itself and its emitter tests
// were removed with the target.

func TestToGeminiCommandTOMLWrapsBody(t *testing.T) {
	got, err := toGeminiCommandTOML("# hello\nbody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "prompt = '''") {
		t.Errorf("missing literal triple-quote prompt opener:\n%s", got)
	}
	if !strings.Contains(got, "body") {
		t.Errorf("missing body content:\n%s", got)
	}
	if !strings.HasSuffix(got, "'''\n") {
		t.Errorf("missing literal triple-quote close:\n%s", got)
	}
}

func TestToGeminiCommandTOMLPreservesBackslashes(t *testing.T) {
	// Backslashes, \n sequences, \uXXXX escapes and line-continuation backslashes
	// must all pass through byte-for-byte — the TOML literal string must NOT
	// interpret them.
	body := "run cmd \\\ncontinued\n\\n literal newline escape\n\\ue0b6 unicode escape"
	got, err := toGeminiCommandTOML(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, body) {
		t.Errorf("body not preserved byte-for-byte:\nwant body=%q\ngot toml=%q", body, got)
	}
}

func TestToGeminiCommandTOMLRejectsTripleTick(t *testing.T) {
	// A literal ''' in the body cannot appear inside a TOML multiline literal
	// string — the helper must return an error rather than emit unparseable TOML.
	body := "before\n'''\nafter"
	_, err := toGeminiCommandTOML(body)
	if err == nil {
		t.Error("expected error when body contains ''', got nil")
	}
}

func TestMapGeminiAgentModelAliases(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "fast", model: "haiku", want: "gemini-2.5-flash-lite"},
		{name: "balanced", model: "sonnet", want: "gemini-3-flash-preview"},
		{name: "deep", model: "opus", want: "gemini-3.1-pro-preview"},
		{name: "native", model: "gemini-3-flash-preview", want: "gemini-3-flash-preview"},
		{name: "inherit", model: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mapGeminiAgentModel(tt.model); got != tt.want {
				t.Fatalf("mapGeminiAgentModel(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}
