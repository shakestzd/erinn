package pluginbuild

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeminiAgentFrontmatterTranslation verifies the core drop/rename/keep rules:
//   - color, skills, initialPrompt are dropped
//   - maxTurns is renamed to max_turns
//   - name, description, model are kept
//   - known tools are translated (Bash → run_shell_command)
func TestGeminiAgentFrontmatterTranslation(t *testing.T) {
	input := `---
name: myagent
description: A test agent
model: opus
color: blue
skills:
  - my-skill
initialPrompt: "hello"
maxTurns: 10
tools:
  - Bash
  - Read
---
# Body content
`
	out, err := translateAgentFrontmatter("myagent.md", []byte(input))
	if err != nil {
		t.Fatalf("translateAgentFrontmatter: %v", err)
	}
	s := string(out)

	// KEEP: name, description; MAP: Claude model aliases to full Gemini model IDs.
	for _, want := range []string{"name: myagent", "description: A test agent", "model: gemini-3.1-pro-preview"} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %q in output:\n%s", want, s)
		}
	}

	// DROP: color, skills, initialPrompt
	for _, notWant := range []string{"color:", "skills:", "initialPrompt:"} {
		if strings.Contains(s, notWant) {
			t.Errorf("expected %q to be dropped from output:\n%s", notWant, s)
		}
	}

	// RENAME: maxTurns → max_turns
	if strings.Contains(s, "maxTurns:") {
		t.Errorf("maxTurns should be renamed to max_turns in output:\n%s", s)
	}
	if !strings.Contains(s, "max_turns: 10") {
		t.Errorf("expected max_turns: 10 in output:\n%s", s)
	}

	// TRANSLATE tools: Bash → run_shell_command, Read → read_file
	if !strings.Contains(s, "run_shell_command") {
		t.Errorf("expected Bash translated to run_shell_command in output:\n%s", s)
	}
	if !strings.Contains(s, "read_file") {
		t.Errorf("expected Read translated to read_file in output:\n%s", s)
	}

	// Body must be preserved verbatim.
	if !strings.Contains(s, "# Body content") {
		t.Errorf("body content lost in output:\n%s", s)
	}
}

// TestGeminiAgentUnknownToolDropped verifies that tools not in the
// claudeToGeminiTool map are silently dropped with a log warning, and that
// the remaining known tools are still translated.
func TestGeminiAgentUnknownToolDropped(t *testing.T) {
	input := `---
name: myagent
tools:
  - Bash
  - UnknownTool
  - Glob
---
body
`
	// Capture log output to confirm the warning is emitted.
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	out, err := translateAgentFrontmatter("myagent.md", []byte(input))
	if err != nil {
		t.Fatalf("translateAgentFrontmatter: %v", err)
	}
	s := string(out)

	// Known tools must still be translated.
	if !strings.Contains(s, "run_shell_command") {
		t.Errorf("expected Bash translated to run_shell_command:\n%s", s)
	}
	if !strings.Contains(s, "glob") {
		t.Errorf("expected Glob translated to glob:\n%s", s)
	}

	// Unknown tool must not appear.
	if strings.Contains(s, "UnknownTool") {
		t.Errorf("UnknownTool should have been dropped:\n%s", s)
	}

	// A warning must have been logged.
	if !strings.Contains(logBuf.String(), "UnknownTool") {
		t.Errorf("expected log warning for UnknownTool; log output: %q", logBuf.String())
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

// TestGeminiAgentAllToolsDroppedFallsBackToWildcard verifies that when all
// tools in the source are unknown (and thus dropped), the emitter falls back
// to ["*"] so the agent is not left with no capabilities.
func TestGeminiAgentAllToolsDroppedFallsBackToWildcard(t *testing.T) {
	input := `---
name: myagent
tools:
  - UnknownA
  - UnknownB
---
body
`
	out, err := translateAgentFrontmatter("myagent.md", []byte(input))
	if err != nil {
		t.Fatalf("translateAgentFrontmatter: %v", err)
	}
	s := string(out)

	// Must fall back to wildcard.
	if !strings.Contains(s, `- '*'`) && !strings.Contains(s, "- '*'") && !strings.Contains(s, `- "*"`) && !strings.Contains(s, "- *") {
		// yaml.Marshal renders ["*"] as "- '*'\n" (single-quoted)
		if !strings.Contains(s, "'*'") && !strings.Contains(s, `"*"`) {
			t.Errorf("expected wildcard '*' in tools after all tools dropped:\n%s", s)
		}
	}

	// Unknown tools must not appear.
	if strings.Contains(s, "UnknownA") || strings.Contains(s, "UnknownB") {
		t.Errorf("unknown tools should have been dropped:\n%s", s)
	}
}

// TestGeminiAgentNoFrontmatterPassesThrough verifies that agent files without
// YAML frontmatter are passed through unchanged (some agents may be bare markdown).
func TestGeminiAgentNoFrontmatterPassesThrough(t *testing.T) {
	input := "# A bare agent\nno frontmatter here\n"
	out, err := translateAgentFrontmatter("bare.md", []byte(input))
	if err != nil {
		t.Fatalf("translateAgentFrontmatter: %v", err)
	}
	if string(out) != input {
		t.Errorf("no-frontmatter file should pass through unchanged:\ngot: %q\nwant: %q", string(out), input)
	}
}

// TestGeminiAgentWebToolsTranslated verifies that WebSearch and WebFetch are
// correctly translated to their Gemini equivalents.
func TestGeminiAgentWebToolsTranslated(t *testing.T) {
	input := `---
name: researchagent
description: Agent with web tools
tools:
  - WebSearch
  - WebFetch
  - Read
---
# Research Agent
`
	out, err := translateAgentFrontmatter("researchagent.md", []byte(input))
	if err != nil {
		t.Fatalf("translateAgentFrontmatter: %v", err)
	}
	s := string(out)

	// WebSearch must be translated to google_web_search.
	if !strings.Contains(s, "google_web_search") {
		t.Errorf("expected WebSearch translated to google_web_search:\n%s", s)
	}

	// WebFetch must be translated to web_fetch.
	if !strings.Contains(s, "web_fetch") {
		t.Errorf("expected WebFetch translated to web_fetch:\n%s", s)
	}

	// Read must still be translated.
	if !strings.Contains(s, "read_file") {
		t.Errorf("expected Read translated to read_file:\n%s", s)
	}

	// Original tool names must not appear.
	if strings.Contains(s, "WebSearch") || strings.Contains(s, "WebFetch") {
		t.Errorf("original tool names should have been translated:\n%s", s)
	}
}

func TestGeminiAgentWarnsOnUnknownFrontmatterField(t *testing.T) {
	input := `---
name: myagent
description: A test agent
permissionMode: bypassPermissions
tools:
  - Read
---
# Body content
`
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	out, err := translateAgentFrontmatter("myagent.md", []byte(input))
	if err != nil {
		t.Fatalf("translateAgentFrontmatter: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "permissionMode:") {
		t.Fatalf("unknown frontmatter field should be dropped:\n%s", s)
	}
	if !strings.Contains(logBuf.String(), `frontmatter field "permissionMode" is not recognized in shared source; omitting from gemini output`) {
		t.Fatalf("expected gemini unknown-field warning, got %q", logBuf.String())
	}
}

// TestGeminiAgentEmitterWritesTranslatedFiles is an integration test that
// exercises emitGeminiAgents end-to-end: seeds a source agent, runs the
// emitter, and confirms the translated output is written to the destination.
func TestGeminiAgentEmitterWritesTranslatedFiles(t *testing.T) {
	repoRoot := t.TempDir()

	// Seed a source agent with Claude frontmatter.
	srcDir := filepath.Join(repoRoot, "plugin", "agents")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentContent := `---
name: testagent
description: Integration test agent
model: opus
color: green
maxTurns: 5
tools:
  - Read
  - Write
---
# Test Agent body
`
	if err := os.WriteFile(filepath.Join(srcDir, "testagent.md"), []byte(agentContent), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Name:    "test",
		Version: "0.0.0-test",
		AssetSources: AssetSources{
			Agents: "plugin/agents",
		},
	}
	target := Target{}
	outDir := filepath.Join(repoRoot, "out")

	if err := emitGeminiAgents(m, repoRoot, outDir, target); err != nil {
		t.Fatalf("emitGeminiAgents: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "agents", "testagent.md"))
	if err != nil {
		t.Fatalf("read emitted agent: %v", err)
	}
	s := string(data)

	// KEEP: name, description, model
	if !strings.Contains(s, "name: testagent") {
		t.Errorf("name missing: %s", s)
	}
	if !strings.Contains(s, "max_turns: 5") {
		t.Errorf("max_turns missing (maxTurns not renamed?): %s", s)
	}

	// DROP: color
	if strings.Contains(s, "color:") {
		t.Errorf("color should be dropped: %s", s)
	}

	// Tools translated.
	if !strings.Contains(s, "read_file") || !strings.Contains(s, "write_file") {
		t.Errorf("tools not translated: %s", s)
	}
}
