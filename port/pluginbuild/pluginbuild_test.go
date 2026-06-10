package pluginbuild

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureManifest is a minimal, self-contained manifest used to exercise both
// adapters without depending on the live packages/plugin-core/manifest.json.
func fixtureManifest() *Manifest {
	return &Manifest{
		Name:        "wipnote",
		Version:     "0.0.0-test",
		Description: "test plugin",
		Author:      Author{Name: "Tester", Email: "t@example.com"},
		Homepage:    "https://example.com",
		Repository:  "https://example.com/repo",
		License:     "MIT",
		Category:    "Dev",
		Keywords:    []string{"test"},
		Targets: map[string]Target{
			"claude": {OutDir: "plugin", ManifestPath: ".claude-plugin/plugin.json", HooksPath: "hooks/hooks.json"},
			"codex": {
				OutDir:                 "packages/codex-marketplace",
				ManifestPath:           ".codex-plugin/plugin.json",
				HooksPath:              "hooks.json",
				MCPPath:                ".mcp.json",
				MarketplaceName:        "wipnote",
				MarketplaceDisplayName: "wipnote",
				MarketplaceCategory:    "Dev",
				PluginSubdir:           ".agents/plugins/wipnote",
			},
			"gemini": {OutDir: "packages/gemini-extension", ManifestPath: "gemini-extension.json", HooksPath: "hooks/hooks.json", ContextFile: "GEMINI.md", CommandNamespace: "wipnote"},
		},
		AssetSources: AssetSources{
			Commands: "plugin/commands",
			Agents:   "plugin/agents",
		},
		Hooks: HookMatrix{Events: []HookEvent{
			{Name: "SessionStart", Handler: "session-start", Targets: []string{"claude", "codex"}},
			{Name: "UserPromptSubmit", Handler: "user-prompt", Targets: []string{"claude", "codex"}},
			{Name: "Stop", Handler: "stop", Targets: []string{"claude"}},
			{Name: "TaskStarted", Handler: "task-started", Targets: []string{"codex"}},
			{Name: "TaskComplete", Handler: "stop", Targets: []string{"codex"}, Timeout: 90},
			{Name: "TaskComplete", Handler: "session-end", Targets: []string{"codex"}, Timeout: 90},
			{Name: "SessionStart", Command: "date", Timeout: 2, Targets: []string{"claude"}, Matcher: "resume"},
		}},
	}
}

func TestClaudeAdapterEmitsManifestAndHooks(t *testing.T) {
	repoRoot := t.TempDir()
	seedAssets(t, repoRoot)
	outDir := filepath.Join(repoRoot, "plugin")

	if err := (claudeAdapter{}).Emit(fixtureManifest(), repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var plug claudePluginJSON
	readJSON(t, filepath.Join(outDir, ".claude-plugin", "plugin.json"), &plug)
	if plug.Name != "wipnote" || plug.Version != "0.0.0-test" {
		t.Fatalf("claude manifest wrong: %+v", plug)
	}

	hooksRaw, err := os.ReadFile(filepath.Join(outDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	// Hooks assertions: Claude-only events present, Codex-only absent.
	s := string(hooksRaw)
	for _, want := range []string{`"SessionStart"`, `"Stop"`, `"UserPromptSubmit"`, `"wipnote hook session-start"`, `"wipnote hook stop"`} {
		if !contains(s, want) {
			t.Errorf("claude hooks missing %q:\n%s", want, s)
		}
	}
	for _, notWant := range []string{`"TaskStarted"`} {
		if contains(s, notWant) {
			t.Errorf("claude hooks should not contain Codex-only %q", notWant)
		}
	}
	// Asset copy
	if _, err := os.Stat(filepath.Join(outDir, "commands", "hello.md")); err != nil {
		t.Errorf("expected copied command: %v", err)
	}
}

func TestClaudeAdapterStripsUnsupportedAgentFrontmatter(t *testing.T) {
	repoRoot := t.TempDir()
	seedAssets(t, repoRoot)
	agentDir := filepath.Join(repoRoot, "plugin", "agents")
	if err := os.WriteFile(filepath.Join(agentDir, "researcher.md"), []byte(`---
name: researcher
description: Research agent
model: sonnet
color: blue
tools:
  - Read
memory: project
timeout_mins: 12
---

# Researcher Agent
`), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(repoRoot, "claude-out")
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	if err := (claudeAdapter{}).Emit(fixtureManifest(), repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "agents", "researcher.md"))
	if err != nil {
		t.Fatalf("read emitted agent: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "timeout_mins:") {
		t.Fatalf("claude output should not include timeout_mins:\n%s", s)
	}
	for _, want := range []string{"name: researcher", "color: blue", "memory: project", "# Researcher Agent"} {
		if !strings.Contains(s, want) {
			t.Fatalf("claude output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(logBuf.String(), `frontmatter field "color" is unsupported`) {
		t.Fatalf("claude should support color field, but got warning: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), `frontmatter field "timeout_mins" is unsupported for claude output`) {
		t.Fatalf("expected claude unsupported-field warning, got %q", logBuf.String())
	}
}

func TestCodexAdapterEmitsManifestHooksAndMCP(t *testing.T) {
	repoRoot := t.TempDir()
	seedAssets(t, repoRoot)
	outDir := filepath.Join(repoRoot, "packages", "codex-marketplace")
	pluginDir := filepath.Join(outDir, ".agents", "plugins", "wipnote")

	if err := (codexAdapter{}).Emit(fixtureManifest(), repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var plug codexPluginJSON
	readJSON(t, filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), &plug)
	if plug.Interface.DisplayName != "wipnote" {
		t.Errorf("codex interface.displayName: %+v", plug.Interface)
	}
	if plug.Author.Email != "t@example.com" {
		t.Errorf("codex author.email: %+v", plug.Author)
	}
	// Codex manifest must also carry license, keywords, and interface.category
	// so Codex's install surface can render without a second lookup.
	if plug.License != "MIT" {
		t.Errorf("codex license: %q", plug.License)
	}
	if len(plug.Keywords) == 0 || plug.Keywords[0] != "test" {
		t.Errorf("codex keywords: %+v", plug.Keywords)
	}
	if plug.Interface.Category != "Dev" {
		t.Errorf("codex interface.category: %q", plug.Interface.Category)
	}
	if plug.Interface.ShortDescription == "" || plug.Interface.DeveloperName != "Tester" {
		t.Errorf("codex interface missing short/developer: %+v", plug.Interface)
	}

	hooksRaw, err := os.ReadFile(filepath.Join(pluginDir, "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	s := string(hooksRaw)
	// Codex-only events present, Claude-only absent.
	for _, want := range []string{
		`"SessionStart"`, `"UserPromptSubmit"`, `"TaskStarted"`, `"wipnote hook task-started"`,
		`"TaskComplete"`, `"wipnote hook stop"`, `"wipnote hook session-end"`,
	} {
		if !contains(s, want) {
			t.Errorf("codex hooks missing %q:\n%s", want, s)
		}
	}
	if contains(s, `"Stop"`) {
		t.Errorf("codex hooks should not contain Claude-only Stop event")
	}

	// .mcp.json stub written under plugin subdir.
	if _, err := os.Stat(filepath.Join(pluginDir, ".mcp.json")); err != nil {
		t.Errorf("expected .mcp.json stub: %v", err)
	}
}

func TestGeminiAdapterEmitsSkeleton(t *testing.T) {
	repoRoot := t.TempDir()
	seedAssets(t, repoRoot)
	// Phase 1 copies the repo-root context file when target.ContextFile is set.
	// The skeleton test seeds a placeholder GEMINI.md so Emit succeeds; Phase 2
	// and Phase 3 still populate commands/ and hooks/ respectively.
	if err := os.WriteFile(filepath.Join(repoRoot, "GEMINI.md"), []byte("# ctx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(repoRoot, "packages", "gemini-extension")

	if err := (geminiAdapter{}).Emit(fixtureManifest(), repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Manifest: required name/version/description plus contextFileName derived from target.ContextFile.
	var manifest geminiExtensionJSON
	readJSON(t, filepath.Join(outDir, "gemini-extension.json"), &manifest)
	if manifest.Name != "wipnote" || manifest.Version != "0.0.0-test" {
		t.Fatalf("gemini manifest wrong: %+v", manifest)
	}
	if manifest.ContextFileName != "GEMINI.md" {
		t.Errorf("gemini contextFileName: %q", manifest.ContextFileName)
	}

	// All four skeleton dirs exist after Emit(). Populated content is asserted
	// by the phase-specific tests: gemini_assets_test.go (agents, skills,
	// GEMINI.md), gemini_commands_test.go (commands/), gemini_hooks_test.go
	// (hooks/hooks.json). This test only guards the minimum-contract invariants
	// — the manifest and the skeleton layout — so it doesn't churn whenever a
	// phase adds populated state.
	for _, dir := range []string{"commands", "agents", "skills", "hooks"} {
		info, err := os.Stat(filepath.Join(outDir, dir))
		if err != nil {
			t.Errorf("expected dir %q: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", dir)
		}
	}
}

func TestLoadAndValidateRejectsBadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	os.WriteFile(path, []byte(`{"name":"","version":"1","targets":{"x":{"outDir":"a","manifestPath":"b","hooksPath":"c"}}}`), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error on empty name")
	}
}

func TestFindManifestWalksUp(t *testing.T) {
	root := t.TempDir()
	corePath := filepath.Join(root, "packages", "plugin-core")
	if err := os.MkdirAll(corePath, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(corePath, "manifest.json")
	writeFixtureManifest(t, manifestPath)

	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := FindManifest(deep)
	if err != nil {
		t.Fatalf("FindManifest: %v", err)
	}
	if found != manifestPath {
		t.Errorf("found=%q want=%q", found, manifestPath)
	}
}

func TestHookEventAppliesTo(t *testing.T) {
	e := HookEvent{Targets: []string{"claude", "codex"}}
	if !e.AppliesTo("claude") || !e.AppliesTo("codex") || e.AppliesTo("gemini") {
		t.Fatalf("AppliesTo mismatch")
	}
}

// --- helpers ---

func seedAssets(t *testing.T, repoRoot string) {
	t.Helper()
	cmdDir := filepath.Join(repoRoot, "plugin", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "hello.md"), []byte("# hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agDir := filepath.Join(repoRoot, "plugin", "agents")
	if err := os.MkdirAll(agDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agDir, "x.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureManifest(t *testing.T, path string) {
	t.Helper()
	data, err := json.MarshalIndent(fixtureManifest(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestCodexAdapterRemovesStaleFiles seeds a fake stale file under an owned
// subtree, runs Emit, and asserts the stale file is removed.
func TestCodexAdapterRemovesStaleFiles(t *testing.T) {
	repoRoot := t.TempDir()
	seedAssets(t, repoRoot)
	outDir := filepath.Join(repoRoot, "packages", "codex-marketplace")

	// Seed a stale file inside .agents/ (the owned subtree) that will not be
	// reproduced by Emit.
	staleFile := filepath.Join(outDir, ".agents", "plugins", "wipnote", "commands", "stale-removed.md")
	if err := os.MkdirAll(filepath.Dir(staleFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleFile, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := (codexAdapter{}).Emit(fixtureManifest(), repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Errorf("expected stale file to be removed; stat error=%v", err)
	}
}

// TestGeminiAdapterRemovesStaleFiles seeds a fake stale file under an owned
// subtree, runs Emit, and asserts the stale file is removed.
func TestGeminiAdapterRemovesStaleFiles(t *testing.T) {
	repoRoot := t.TempDir()
	seedAssets(t, repoRoot)
	// Gemini Phase 1 requires GEMINI.md at repo root when ContextFile is set.
	if err := os.WriteFile(filepath.Join(repoRoot, "GEMINI.md"), []byte("# ctx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(repoRoot, "packages", "gemini-extension")

	// Seed a stale TOML under commands/ that will not be reproduced by Emit
	// (the fixture manifest's commands dir contains hello.md → hello.toml,
	// but not stale-removed.toml).
	staleFile := filepath.Join(outDir, "commands", "wipnote", "stale-removed.toml")
	if err := os.MkdirAll(filepath.Dir(staleFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleFile, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := (geminiAdapter{}).Emit(fixtureManifest(), repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Errorf("expected stale file to be removed; stat error=%v", err)
	}
}
