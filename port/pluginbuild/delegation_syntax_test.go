package pluginbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedNonClaudeOutputsAvoidClaudeDelegationSyntax(t *testing.T) {
	repoRoot := repositoryRootForTest(t)
	manifest, err := Load(filepath.Join(repoRoot, "packages", "plugin-core", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	codexOut := filepath.Join(outDir, "codex")
	if err := (codexAdapter{}).Emit(manifest, repoRoot, codexOut); err != nil {
		t.Fatalf("emit codex: %v", err)
	}
	geminiOut := filepath.Join(outDir, "gemini")
	if err := (geminiAdapter{}).Emit(manifest, repoRoot, geminiOut); err != nil {
		t.Fatalf("emit gemini: %v", err)
	}
	activeRoots := []string{
		filepath.Join(codexOut, ".agents", "plugins", "wipnote", "commands"),
		filepath.Join(codexOut, ".agents", "plugins", "wipnote", "skills"),
		filepath.Join(geminiOut, "commands"),
		filepath.Join(geminiOut, "skills"),
		filepath.Join(geminiOut, "agents"),
	}
	for _, root := range activeRoots {
		assertNoClaudeDelegationSyntax(t, root)
	}
	knownRoles := codexKnownAgentRoles(manifest, repoRoot)
	assertNoGeminiColonAgentIDs(t, filepath.Join(geminiOut, "commands"), knownRoles)
	assertNoGeminiColonAgentIDs(t, filepath.Join(geminiOut, "skills"), knownRoles)
	assertNoGeminiColonAgentIDs(t, filepath.Join(geminiOut, "agents"), knownRoles)
}

func repositoryRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// The plugin-core manifest is the canonical source of truth and lives at the
	// repository root (packages/plugin-core/manifest.json) — it did NOT move under
	// port/ when the pluginbuild generator was lifted here (trk-1ea27426). Walk up
	// to find it rather than hardcoding a directory depth, so this stays correct
	// regardless of how deeply nested the test package is.
	manifestPath, err := FindManifest(wd)
	if err != nil {
		t.Fatalf("cannot locate repository root from %s: %v", wd, err)
	}
	// manifestPath = <root>/packages/plugin-core/manifest.json → three dirs up = <root>.
	return filepath.Dir(filepath.Dir(filepath.Dir(manifestPath)))
}

func assertNoGeminiColonAgentIDs(t *testing.T, root string, knownRoles map[string]struct{}) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for role := range knownRoles {
			if strings.Contains(content, "wipnote:"+role) {
				t.Errorf("%s contains Claude-style wipnote:%s agent ID", path, role)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func assertNoClaudeDelegationSyntax(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("expected generated output root %s: %v", root, err)
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, forbidden := range []string{
			"subagent_type=",
			"Task(subagent_type=",
			"Agent(subagent_type=",
			"Task(",
			"Agent(",
		} {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s contains Claude-only delegation syntax %q", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
