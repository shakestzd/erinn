package pluginbuild

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestTranslateCodexAgentTOML(t *testing.T) {
	raw := []byte(`---
name: feature-coder
description: Balanced code execution agent
model: sonnet
color: blue
tools:
  - Read
  - Edit
  - Write
maxTurns: 60
skills:
  - agent-context
initialPrompt: "Run wipnote agent-init."
---

# Feature Coder Agent

Implement code changes.
`)

	agent, err := translateCodexAgent("feature-coder.md", raw)
	if err != nil {
		t.Fatalf("translateCodexAgent: %v", err)
	}

	if agent.Name != "wipnote-feature-coder" {
		t.Errorf("name = %q, want wipnote-feature-coder", agent.Name)
	}
	if agent.Description != "Balanced code execution agent" {
		t.Errorf("description = %q", agent.Description)
	}
	if agent.Model != "gpt-5.4" || agent.ModelReasoningEffort != "medium" {
		t.Errorf("Claude model alias should map to Codex model, got model=%q effort=%q", agent.Model, agent.ModelReasoningEffort)
	}
	if agent.SandboxMode != "workspace-write" {
		t.Errorf("sandbox_mode = %q, want workspace-write", agent.SandboxMode)
	}
	if len(agent.NicknameCandidates) != 1 || agent.NicknameCandidates[0] != "feature-coder" {
		t.Errorf("nickname_candidates = %#v", agent.NicknameCandidates)
	}
	if !strings.Contains(agent.DeveloperInstructions, "# Feature Coder Agent") {
		t.Errorf("developer_instructions missing body:\n%s", agent.DeveloperInstructions)
	}
	if !strings.Contains(agent.DeveloperInstructions, "## Initial Prompt\n\nRun wipnote agent-init.") {
		t.Errorf("developer_instructions missing initial prompt:\n%s", agent.DeveloperInstructions)
	}
}

func TestTranslateCodexAgentWarnsOnUnsupportedField(t *testing.T) {
	raw := []byte(`---
name: feature-coder
description: Balanced code execution agent
timeout_mins: 15
tools:
  - Read
---

# Feature Coder Agent
`)

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	agent, err := translateCodexAgent("feature-coder.md", raw)
	if err != nil {
		t.Fatalf("translateCodexAgent: %v", err)
	}
	if agent.Name != "wipnote-feature-coder" {
		t.Fatalf("translated wrong agent: %+v", agent)
	}
	if strings.Contains(agent.DeveloperInstructions, "timeout_mins") {
		t.Fatalf("developer instructions should only contain body content:\n%s", agent.DeveloperInstructions)
	}
	if !strings.Contains(logBuf.String(), `frontmatter field "timeout_mins" is unsupported for codex output`) {
		t.Fatalf("expected codex unsupported-field warning, got %q", logBuf.String())
	}
}

func TestTranslateCodexAgentReaderSandbox(t *testing.T) {
	raw := []byte(`---
name: reader
description: Read-only file retrieval agent
model: haiku
tools:
  - Read
  - Grep
  - Glob
maxTurns: 10
---

# Reader Agent
`)

	agent, err := translateCodexAgent("reader.md", raw)
	if err != nil {
		t.Fatalf("translateCodexAgent: %v", err)
	}
	if agent.SandboxMode != "read-only" {
		t.Errorf("sandbox_mode = %q, want read-only", agent.SandboxMode)
	}
}

func TestTranslateCodexAgentBashSandbox(t *testing.T) {
	raw := []byte(`---
name: test-runner
description: Quality assurance agent that runs builds and tests
model: haiku
tools:
  - Read
  - Grep
  - Glob
  - Bash
maxTurns: 20
---

# Test Runner Agent
`)

	agent, err := translateCodexAgent("test-runner.md", raw)
	if err != nil {
		t.Fatalf("translateCodexAgent: %v", err)
	}
	if agent.SandboxMode != "workspace-write" {
		t.Errorf("sandbox_mode = %q, want workspace-write (Bash writes caches/artifacts)", agent.SandboxMode)
	}
}

func TestMapCodexAgentModelAliases(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		wantModel  string
		wantEffort string
	}{
		{name: "fast", model: "haiku", wantModel: "gpt-5.4-mini", wantEffort: "low"},
		{name: "balanced", model: "sonnet", wantModel: "gpt-5.4", wantEffort: "medium"},
		{name: "deep", model: "opus", wantModel: "gpt-5.5", wantEffort: "high"},
		{name: "native", model: "gpt-5.3-codex", wantModel: "gpt-5.3-codex", wantEffort: ""},
		{name: "inherit", model: "", wantModel: "", wantEffort: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotEffort := mapCodexAgentModel(tt.model)
			if gotModel != tt.wantModel || gotEffort != tt.wantEffort {
				t.Fatalf("mapCodexAgentModel(%q) = (%q, %q), want (%q, %q)", tt.model, gotModel, gotEffort, tt.wantModel, tt.wantEffort)
			}
		})
	}
}

func TestCodexAdapterEmitsAgentsAsTOML(t *testing.T) {
	repoRoot := t.TempDir()
	seedAssets(t, repoRoot)
	agentDir := filepath.Join(repoRoot, "plugin", "agents")
	if err := os.WriteFile(filepath.Join(agentDir, "feature-coder.md"), []byte(`---
name: feature-coder
description: Balanced code execution agent
tools:
  - Read
  - Edit
---

# Feature Coder Agent
`), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(repoRoot, "packages", "codex-marketplace")
	if err := (codexAdapter{}).Emit(fixtureManifest(), repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	pluginDir := filepath.Join(outDir, ".agents", "plugins", "wipnote")
	if _, err := os.Stat(filepath.Join(pluginDir, "agents", "feature-coder.md")); !os.IsNotExist(err) {
		t.Errorf("Codex agents must not be copied as markdown; stat err=%v", err)
	}

	data, err := os.ReadFile(filepath.Join(pluginDir, "agents", "wipnote-feature-coder.toml"))
	if err != nil {
		t.Fatalf("read emitted agent TOML: %v", err)
	}
	agent, err := parseCodexAgentTOML(data)
	if err != nil {
		t.Fatalf("parse emitted agent TOML: %v", err)
	}
	if agent.Name != "wipnote-feature-coder" || agent.DeveloperInstructions == "" {
		t.Errorf("emitted agent mismatch: %+v", agent)
	}
}

func parseCodexAgentTOML(raw []byte) (codexAgentTOML, error) {
	dec := toml.NewDecoder(bytes.NewReader(raw))
	var agent codexAgentTOML
	if err := dec.Decode(&agent); err != nil {
		return codexAgentTOML{}, err
	}
	return agent, nil
}

// TestCodexRewriteAgentIDs checks the colon-to-hyphen translation for Codex
// assets. Only known roles should be translated; unknown ones are left alone.
func TestCodexRewriteAgentIDs(t *testing.T) {
	knownRoles := map[string]struct{}{
		"patch-coder":     {},
		"feature-coder":   {},
		"architect-coder": {},
		"test-runner":     {},
		"researcher":      {},
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single known role",
			input: `Task(subagent_type="wipnote:patch-coder")`,
			want:  `Task(subagent_type="wipnote-patch-coder")`,
		},
		{
			name:  "multiple known roles",
			input: `wipnote:patch-coder and wipnote:feature-coder and wipnote:architect-coder`,
			want:  `wipnote-patch-coder and wipnote-feature-coder and wipnote-architect-coder`,
		},
		{
			name:  "unknown role left unchanged",
			input: `wipnote:unknown-role should stay`,
			want:  `wipnote:unknown-role should stay`,
		},
		{
			name:  "mixed known and unknown",
			input: `wipnote:patch-coder ok, wipnote:ghost not translated`,
			want:  `wipnote-patch-coder ok, wipnote:ghost not translated`,
		},
		{
			name:  "no wipnote prefix passthrough",
			input: `nothing to translate here`,
			want:  `nothing to translate here`,
		},
		{
			name:  "researcher and test-runner",
			input: `wipnote:researcher or wipnote:test-runner`,
			want:  `wipnote-researcher or wipnote-test-runner`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexRewriteAgentIDs(tt.input, knownRoles)
			if got != tt.want {
				t.Errorf("codexRewriteAgentIDs(%q) =\n  %q\nwant\n  %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRewriteDelegationSyntax(t *testing.T) {
	input := strings.Join([]string{
		"- Simple → `Task(subagent_type=\"wipnote:patch-coder\")`",
		"Agent(",
		"    subagent_type=\"wipnote:feature-coder\",",
		"    description=\"Feature A\",",
		")",
		"Task(subagent_type=\"wipnote:feature-coder\",",
		"    description=\"Feature B\",",
		"    prompt=\"\"\"",
		"Implement the tiny fixture.",
		"Keep it reversible.",
		"\"\"\",",
		")",
		"Agent(",
		"    description=task.subject,",
		"    subagent_type=task.metadata.agent,",
		"    isolation=\"worktree\",",
		"    prompt=build_prompt(task)",
		")",
		"- Unknown → `Task(subagent_type=\"wipnote:gemini-spawner\")`",
		"TaskCreate(",
		"    subject=\"keep this ordinary call\",",
		")",
	}, "\n")
	wantCodex := strings.Join([]string{
		"- Simple → `call spawn_agent with agent_type \"wipnote-patch-coder\"`",
		"Call spawn_agent with:",
		"Call spawn_agent with agent_type \"wipnote-feature-coder\" and message containing: description=\"Feature A\".",
		"Call spawn_agent with:",
		"Call spawn_agent with agent_type \"wipnote-feature-coder\" and message containing: description=\"Feature B\"; prompt=Implement the tiny fixture.\nKeep it reversible.",
		"Call spawn_agent with:",
		"Call spawn_agent with: description=task.subject; agent_type=task.metadata.agent; isolation=\"worktree\"; prompt=build_prompt(task).",
		"- Unknown → `use the gemini-spawner workflow described here`",
		"TaskCreate(",
		"    subject=\"keep this ordinary call\",",
		")",
	}, "\n")
	knownRoles := map[string]struct{}{
		"patch-coder":   {},
		"feature-coder": {},
	}
	if got := rewriteCodexDelegationSyntax(codexRewriteAgentIDs(input, knownRoles), knownRoles); got != wantCodex {
		t.Fatalf("codex rewrite mismatch\nwant:\n%s\n\ngot:\n%s", wantCodex, got)
	}

	wantGemini := strings.Join([]string{
		"- Simple → `use @patch-coder`",
		"Use Gemini agent invocation with:",
		"    agent=\"@feature-coder\",",
		"    description=\"Feature A\",",
		"Use Gemini agent invocation with:",
		"    agent=\"@feature-coder\",",
		"    description=\"Feature B\",",
		"    message=\"\"\"",
		"Implement the tiny fixture.",
		"Keep it reversible.",
		"\"\"\",",
		"Use Gemini agent invocation with:",
		"    description=task.subject,",
		"    agent=task.metadata.agent,",
		"    isolation=\"worktree\",",
		"    message=build_prompt(task)",
		"- Unknown → `use the gemini-spawner workflow described here`",
		"TaskCreate(",
		"    subject=\"keep this ordinary call\",",
		")",
	}, "\n")
	if got := rewriteGeminiAgentIDs(rewriteGeminiDelegationSyntax(input, knownRoles), knownRoles); got != wantGemini {
		t.Fatalf("gemini rewrite mismatch\nwant:\n%s\n\ngot:\n%s", wantGemini, got)
	}
}

// TestCodexNoColonFormAgentIDsInOutputTree is a generated-output test that emits
// the Codex marketplace tree from the live manifest and asserts that no
// wipnote:<role> strings appear in the output for any declared agent role. This
// catches cases where a shared source asset references a Claude-style agent ID
// that must be translated for Codex.
func TestCodexNoColonFormAgentIDsInOutputTree(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	manifestPath, err := FindManifest(cwd)
	if err != nil {
		t.Fatalf("FindManifest: %v", err)
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(manifestPath)))

	m, err := Load(manifestPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	outDir := t.TempDir()
	if err := (codexAdapter{}).Emit(m, repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Collect declared roles from the agents source directory.
	knownRoles := codexKnownAgentRoles(m, repoRoot)
	if len(knownRoles) == 0 {
		t.Skip("no agent roles declared — nothing to check")
	}

	// Walk the emitted Codex tree and verify no colon-form wipnote:<role> exists
	// for any declared role in any text file.
	violations := []string{}
	if err := filepath.WalkDir(filepath.Join(outDir, ".agents"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// Skip binary files.
		probe := data
		if len(probe) > 512 {
			probe = probe[:512]
		}
		if bytes.IndexByte(probe, 0) >= 0 {
			return nil
		}
		content := string(data)
		for role := range knownRoles {
			colonForm := "wipnote:" + role
			if strings.Contains(content, colonForm) {
				rel, _ := filepath.Rel(outDir, path)
				violations = append(violations, rel+": contains "+colonForm)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("walk emitted tree: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("colon-form agent IDs found in Codex output tree (should be hyphen-form):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func TestCodexExecuteSkillUsesTargetOverride(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	manifestPath, err := FindManifest(cwd)
	if err != nil {
		t.Fatalf("FindManifest: %v", err)
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(manifestPath)))

	m, err := Load(manifestPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	outDir := t.TempDir()
	if err := (codexAdapter{}).Emit(m, repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	skillPath := filepath.Join(outDir, ".agents", "plugins", "wipnote", "skills", "execute", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read execute skill: %v", err)
	}
	content := string(data)

	for _, banned := range []string{
		"SendMessage",
		"TaskList(",
		"TaskCreate(",
		"TaskUpdate(",
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS",
		".claude/worktrees",
	} {
		if strings.Contains(content, banned) {
			t.Errorf("Codex execute skill contains banned Claude-only term %q", banned)
		}
	}

	for _, required := range []string{
		"spawn_agent",
		"wait_agent",
		"send_input",
		"close_agent",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("Codex execute skill missing required lifecycle primitive %q", required)
		}
	}
}
