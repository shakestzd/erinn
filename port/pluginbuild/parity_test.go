package pluginbuild

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeParityFromLiveManifest guards the Claude plugin port against
// regressions in the shared manifest. It loads the real
// packages/plugin-core/manifest.json, emits a Claude plugin tree into a
// tempdir, and asserts the invariants the wipnote plugin must satisfy:
// manifest name/version, the three workhorse hook events, and at least one
// command/agent/skill copied from the asset sources. The test is
// self-contained: it does not shell out, hit the network, or depend on the
// wipnote binary being installed.
func TestClaudeParityFromLiveManifest(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	manifestPath, err := FindManifest(cwd)
	if err != nil {
		t.Fatalf("FindManifest: %v", err)
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(manifestPath))) // .../packages/plugin-core/manifest.json → repo root

	m, err := Load(manifestPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	outDir := t.TempDir()
	if err := (claudeAdapter{}).Emit(m, repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// manifest name/version
	var plug claudePluginJSON
	manifestBytes, err := os.ReadFile(filepath.Join(outDir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	if err := json.Unmarshal(manifestBytes, &plug); err != nil {
		t.Fatalf("unmarshal plugin.json: %v", err)
	}
	if plug.Name != m.Name {
		t.Errorf("plugin.json name=%q want %q", plug.Name, m.Name)
	}
	if plug.Version != m.Version {
		t.Errorf("plugin.json version=%q want %q", plug.Version, m.Version)
	}

	// hooks.json carries the three workhorse events every Claude session uses.
	hooksBytes, err := os.ReadFile(filepath.Join(outDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	hooks := string(hooksBytes)
	for _, want := range []string{`"SessionStart"`, `"PreToolUse"`, `"PostToolUse"`} {
		if !strings.Contains(hooks, want) {
			t.Errorf("hooks.json missing %s", want)
		}
	}

	// Codex-only events must not leak into the Claude output.
	for _, notWant := range []string{`"TaskStarted"`, `"TurnAborted"`} {
		if strings.Contains(hooks, notWant) {
			t.Errorf("hooks.json contains codex-only event %s", notWant)
		}
	}

	// At least one command, one agent, and one skill copied over.
	assertHasMarkdown(t, filepath.Join(outDir, "commands"), "commands")
	assertHasMarkdown(t, filepath.Join(outDir, "agents"), "agents")
	assertHasSkill(t, filepath.Join(outDir, "skills"))

	executeSkill, err := os.ReadFile(filepath.Join(outDir, "skills", "execute", "SKILL.md"))
	if err != nil {
		t.Fatalf("read Claude execute skill: %v", err)
	}
	if !strings.Contains(string(executeSkill), "SendMessage") {
		t.Errorf("Claude execute skill lost SendMessage preflight content")
	}
}

func assertHasMarkdown(t *testing.T, dir, label string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", label, err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			// Guard against the same-path bug that silently truncated assets: any
			// .md file must be non-empty to count as successfully copied.
			info, err := e.Info()
			if err != nil {
				t.Fatalf("stat %s/%s: %v", label, e.Name(), err)
			}
			if info.Size() > 0 {
				return
			}
		}
	}
	t.Errorf("no non-empty .md under %s", dir)
}

func assertHasSkill(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read skills: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skill := filepath.Join(dir, e.Name(), "SKILL.md")
		info, err := os.Stat(skill)
		if err == nil && info.Size() > 0 {
			return
		}
	}
	t.Errorf("no non-empty SKILL.md under %s", dir)
}
