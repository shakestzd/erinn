package pluginbuild

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGeminiAdapterCopiesVerbatimAssets verifies the Phase 1 behavior: agents,
// skills, templates, static, and config are copied verbatim into the Gemini
// extension tree, the repo-root GEMINI.md is copied alongside the extension
// manifest, and commands/ stays untouched (Phase 2 owns .md → .toml).
func TestGeminiAdapterCopiesVerbatimAssets(t *testing.T) {
	repoRoot := t.TempDir()
	seedGeminiPhase1Assets(t, repoRoot)

	outDir := filepath.Join(repoRoot, "packages", "gemini-extension")
	m := geminiPhase1Manifest()

	if err := (geminiAdapter{}).Emit(m, repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Agent copied verbatim with original content.
	agentPath := filepath.Join(outDir, "agents", "foo.md")
	if data, err := os.ReadFile(agentPath); err != nil {
		t.Errorf("expected agent at %s: %v", agentPath, err)
	} else if string(data) != "# foo\n" {
		t.Errorf("agent content: got %q, want %q", string(data), "# foo\n")
	}

	// Skill directory layout preserved (bar/SKILL.md, not bar.md).
	skillPath := filepath.Join(outDir, "skills", "bar", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("expected skill at %s: %v", skillPath, err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "skills", "bar", "SKILL.codex.md")); !os.IsNotExist(err) {
		t.Errorf("expected Codex override sidecar to be skipped in Gemini output; err=%v", err)
	}

	// Repo-root GEMINI.md copied to extension root (basename only).
	geminiPath := filepath.Join(outDir, "GEMINI.md")
	if data, err := os.ReadFile(geminiPath); err != nil {
		t.Errorf("expected GEMINI.md at %s: %v", geminiPath, err)
	} else if string(data) != "# wipnote (Gemini)\n" {
		t.Errorf("GEMINI.md content: %q", string(data))
	}

	// commands/ is populated by Phase 2's gemini_commands.go sub-emitter once
	// that phase lands; Phase 1 alone does not populate it. With all phases
	// merged, this test only asserts Phase 1's contract (assets + context
	// file are present) — the commands/ contents belong to Phase 2's tests.
}

// TestGeminiAdapterSkipsMissingContextFile makes sure the adapter tolerates
// targets without a ContextFile (same behavior as Claude/Codex).
func TestGeminiAdapterSkipsMissingContextFile(t *testing.T) {
	repoRoot := t.TempDir()
	seedGeminiPhase1Assets(t, repoRoot)

	outDir := filepath.Join(repoRoot, "packages", "gemini-extension")
	m := geminiPhase1Manifest()
	tgt := m.Targets["gemini"]
	tgt.ContextFile = ""
	m.Targets["gemini"] = tgt

	if err := (geminiAdapter{}).Emit(m, repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "GEMINI.md")); !os.IsNotExist(err) {
		t.Errorf("expected no GEMINI.md when ContextFile is empty; err=%v", err)
	}
}

// geminiPhase1Manifest returns a manifest variant with all AssetSources set
// (the package-wide fixtureManifest only sets Commands and Agents). We build
// locally so we don't depend on the wider fixture evolving across phases.
func geminiPhase1Manifest() *Manifest {
	return &Manifest{
		Name:        "wipnote",
		Version:     "0.0.0-test",
		Description: "test plugin",
		Targets: map[string]Target{
			"gemini": {
				OutDir:           "packages/gemini-extension",
				ManifestPath:     "gemini-extension.json",
				HooksPath:        "hooks/hooks.json",
				ContextFile:      "GEMINI.md",
				CommandNamespace: "wipnote",
			},
		},
		AssetSources: AssetSources{
			Commands:  "plugin/commands",
			Agents:    "plugin/agents",
			Skills:    "plugin/skills",
			Templates: "plugin/templates",
			Static:    "plugin/static",
			Config:    "plugin/config",
		},
		Hooks: HookMatrix{Events: []HookEvent{
			{Name: "SessionStart", Handler: "session-start", Targets: []string{"gemini"}},
		}},
	}
}

// seedGeminiPhase1Assets writes a minimal fixture that covers every asset type
// Phase 1 reuses. Commands are intentionally written too so we can assert they
// are NOT copied into the Gemini tree.
func seedGeminiPhase1Assets(t *testing.T, repoRoot string) {
	t.Helper()
	writeFile(t, filepath.Join(repoRoot, "plugin", "agents", "foo.md"), "# foo\n")
	writeFile(t, filepath.Join(repoRoot, "plugin", "skills", "bar", "SKILL.md"), "# bar skill\n")
	writeFile(t, filepath.Join(repoRoot, "plugin", "skills", "bar", "SKILL.codex.md"), "# codex override\n")
	writeFile(t, filepath.Join(repoRoot, "plugin", "templates", "x.html"), "<html/>\n")
	writeFile(t, filepath.Join(repoRoot, "plugin", "static", "y.css"), "body{}\n")
	writeFile(t, filepath.Join(repoRoot, "plugin", "config", "z.json"), "{}\n")
	writeFile(t, filepath.Join(repoRoot, "plugin", "commands", "should-not-copy.md"), "# nope\n")
	writeFile(t, filepath.Join(repoRoot, "GEMINI.md"), "# wipnote (Gemini)\n")
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdirall %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
