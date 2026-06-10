package pluginbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	// interpret them. This exercises the core bug fix: TOML basic strings would
	// have rewritten these sequences, but literal strings do not.
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
	// string — it would prematurely terminate the string. The helper must return
	// an error rather than silently produce unparseable TOML.
	body := "before\n'''\nafter"
	_, err := toGeminiCommandTOML(body)
	if err == nil {
		t.Error("expected error when body contains ''', got nil")
	}
}

func TestGeminiAdapterEmitsCommandsTOML(t *testing.T) {
	repoRoot := t.TempDir()
	cmdDir := filepath.Join(repoRoot, "plugin", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "hello.md"), []byte("# hello\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Phase 1's sub-emitter copies repo-root GEMINI.md into the extension tree,
	// so Emit() fails if it's absent. Seed a placeholder for this integration test.
	if err := os.WriteFile(filepath.Join(repoRoot, "GEMINI.md"), []byte("# ctx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(repoRoot, "packages", "gemini-extension")
	if err := (geminiAdapter{}).Emit(fixtureManifest(), repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	tomlPath := filepath.Join(outDir, "commands", "wipnote", "hello.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read emitted toml: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "prompt = '''") {
		t.Errorf("emitted toml missing literal prompt opener:\n%s", s)
	}
	if !strings.Contains(s, "# hello") || !strings.Contains(s, "body") {
		t.Errorf("emitted toml missing markdown body:\n%s", s)
	}
}

// TestGeminiAdapterCommandParity asserts that every .md in plugin/commands/
// produces exactly one .toml under commands/<namespace>/ in the emitted tree.
// This guards against silent drops (e.g. filter bugs) when new commands are
// added to plugin/commands/.
func TestGeminiAdapterCommandParity(t *testing.T) {
	manifestPath, err := FindManifest(".")
	if err != nil {
		t.Skipf("no live manifest (pre-integration test): %v", err)
	}
	m, err := Load(manifestPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(manifestPath)))

	srcDir := filepath.Join(repoRoot, m.AssetSources.Commands)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("read live commands dir: %v", err)
	}
	var wantCount int
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			wantCount++
		}
	}
	if wantCount == 0 {
		t.Fatalf("expected plugin/commands/*.md to exist, found 0")
	}

	outDir := t.TempDir()
	target := m.Targets["gemini"]
	if err := emitGeminiCommands(m, repoRoot, outDir, target); err != nil {
		t.Fatalf("emitGeminiCommands: %v", err)
	}

	dstDir := filepath.Join(outDir, "commands", target.CommandNamespace)
	out, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatalf("read emitted toml dir: %v", err)
	}
	var gotCount int
	for _, e := range out {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
			gotCount++
		}
	}
	if gotCount != wantCount {
		t.Errorf("parity mismatch: %d .md in source, %d .toml emitted", wantCount, gotCount)
	}
}
