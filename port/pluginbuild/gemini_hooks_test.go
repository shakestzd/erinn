package pluginbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeminiHookEventNameTranslation verifies that geminiEventName overrides the
// Claude event name in the emitted Gemini hooks.json.
func TestGeminiHookEventNameTranslation(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "0.0.0",
		Targets: map[string]Target{
			"gemini": {
				OutDir:       "out",
				ManifestPath: "gemini-extension.json",
				HooksPath:    "hooks/hooks.json",
			},
		},
		Hooks: HookMatrix{Events: []HookEvent{
			{Name: "UserPromptSubmit", Handler: "user-prompt", Targets: []string{"gemini"}, GeminiEventName: "BeforeAgent"},
			{Name: "AfterAgent", Handler: "after-agent", Targets: []string{"gemini"}},
			{Name: "PreToolUse", Handler: "pretooluse", Targets: []string{"gemini"}, GeminiEventName: "BeforeTool"},
			{Name: "PostToolUse", Handler: "posttooluse", Targets: []string{"gemini"}, GeminiEventName: "AfterTool"},
			{Name: "Stop", Handler: "stop", Targets: []string{"gemini"}, GeminiEventName: "SessionEnd", GeminiHandler: "session-end"},
		}},
	}

	outDir := t.TempDir()
	target := m.Targets["gemini"]

	if err := emitGeminiHooks(m, t.TempDir(), outDir, target); err != nil {
		t.Fatalf("emitGeminiHooks: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	s := string(data)

	// Translated Gemini names must appear.
	for _, want := range []string{`"BeforeAgent"`, `"AfterAgent"`, `"BeforeTool"`, `"AfterTool"`, `"SessionEnd"`} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %q in Gemini hooks:\n%s", want, s)
		}
	}
	// Claude event names must not appear (they were all overridden by geminiEventName).
	for _, notWant := range []string{`"UserPromptSubmit"`, `"PreToolUse"`, `"PostToolUse"`, `"Stop"`} {
		if strings.Contains(s, notWant) {
			t.Errorf("Claude event name %q should be translated, not passed through:\n%s", notWant, s)
		}
	}
}

// TestGeminiHookHandlerOverride verifies that geminiHandler overrides the default
// handler for Gemini targets. This is used to route Stop (handler: stop) to
// session-end for Gemini while keeping it as stop for Claude.
func TestGeminiHookHandlerOverride(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "0.0.0",
		Targets: map[string]Target{
			"gemini": {
				OutDir:       "out",
				ManifestPath: "gemini-extension.json",
				HooksPath:    "hooks/hooks.json",
			},
		},
		Hooks: HookMatrix{Events: []HookEvent{
			{Name: "Stop", Handler: "stop", Targets: []string{"gemini"}, GeminiEventName: "SessionEnd", GeminiHandler: "session-end"},
		}},
	}

	outDir := t.TempDir()
	target := m.Targets["gemini"]

	if err := emitGeminiHooks(m, t.TempDir(), outDir, target); err != nil {
		t.Fatalf("emitGeminiHooks: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	s := string(data)

	// Gemini SessionEnd event must invoke session-end handler, not stop.
	if !strings.Contains(s, "wipnote hook session-end") {
		t.Errorf("expected SessionEnd to invoke 'wipnote hook session-end' for Gemini:\n%s", s)
	}
	if strings.Contains(s, "wipnote hook stop") {
		t.Errorf("Gemini SessionEnd should not invoke 'wipnote hook stop' (that's Claude's):\n%s", s)
	}
}

// TestGeminiHookExtensionPathVar verifies that $GEMINI_EXTENSION_DIR in hook
// commands is replaced with ${extensionPath} in the emitted Gemini hooks.json.
func TestGeminiHookExtensionPathVar(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "0.0.0",
		Targets: map[string]Target{
			"gemini": {
				OutDir:       "out",
				ManifestPath: "gemini-extension.json",
				HooksPath:    "hooks/hooks.json",
			},
		},
		Hooks: HookMatrix{Events: []HookEvent{
			{
				Name:    "SessionStart",
				Command: "$GEMINI_EXTENSION_DIR/bin/wipnote hook session-start",
				Targets: []string{"gemini"},
			},
		}},
	}

	outDir := t.TempDir()
	target := m.Targets["gemini"]

	if err := emitGeminiHooks(m, t.TempDir(), outDir, target); err != nil {
		t.Fatalf("emitGeminiHooks: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	s := string(data)

	// Old variable must not appear.
	if strings.Contains(s, "$GEMINI_EXTENSION_DIR") {
		t.Errorf("$GEMINI_EXTENSION_DIR should be replaced with ${extensionPath}:\n%s", s)
	}
	// New variable must appear.
	if !strings.Contains(s, "${extensionPath}") {
		t.Errorf("expected ${extensionPath} in emitted command:\n%s", s)
	}
}

// TestGeminiHookMatcherWildcard verifies that empty matchers are replaced with
// "*" in the Gemini output, since Gemini requires explicit wildcards.
func TestGeminiHookMatcherWildcard(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "0.0.0",
		Targets: map[string]Target{
			"gemini": {
				OutDir:       "out",
				ManifestPath: "gemini-extension.json",
				HooksPath:    "hooks/hooks.json",
			},
		},
		Hooks: HookMatrix{Events: []HookEvent{
			{Name: "SessionStart", Handler: "session-start", Matcher: "", Targets: []string{"gemini"}},
		}},
	}

	outDir := t.TempDir()
	target := m.Targets["gemini"]

	if err := emitGeminiHooks(m, t.TempDir(), outDir, target); err != nil {
		t.Fatalf("emitGeminiHooks: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	s := string(data)

	// Empty matcher must be replaced with "*".
	if !strings.Contains(s, `"matcher": "*"`) {
		t.Errorf(`expected "matcher": "*" in output (empty matcher should default to wildcard):\n%s`, s)
	}
}

// TestGeminiAdapterEmitsHooksFromFixture exercises the Gemini hooks sub-emitter
// against the fixture manifest. It asserts that:
//   - `hooks/hooks.json` is written with the SessionStart event and its mapped
//     `wipnote hook session-start` command.
//   - Codex-only events (TaskStarted) do not leak into the Gemini output.
//   - Claude-only matcher variants (SessionStart + `session-resume` / matcher
//     "resume") do not leak into the Gemini output.
func TestGeminiAdapterEmitsHooksFromFixture(t *testing.T) {
	repoRoot := t.TempDir()
	seedAssets(t, repoRoot)
	outDir := filepath.Join(repoRoot, "packages", "gemini-extension")
	seedAssets(t, repoRoot)
	// Phase 1's sub-emitter copies repo-root GEMINI.md; seed a placeholder so
	// the full Emit chain doesn't fail before Phase 3's hook assertion runs.
	if err := os.WriteFile(filepath.Join(repoRoot, "GEMINI.md"), []byte("# ctx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := fixtureManifest()
	// Tag the SessionStart/UserPromptSubmit/Stop fixture events for Gemini to
	// mirror the live manifest without requiring fixtureManifest() edits.
	for i := range m.Hooks.Events {
		e := &m.Hooks.Events[i]
		switch {
		case e.Name == "SessionStart" && e.Handler == "session-start":
			e.Targets = append(e.Targets, "gemini")
		case e.Name == "UserPromptSubmit" && e.Handler == "user-prompt":
			e.Targets = append(e.Targets, "gemini")
			e.GeminiEventName = "BeforeAgent"
		case e.Name == "AfterAgent" && e.Handler == "after-agent":
			e.Targets = append(e.Targets, "gemini")
		case e.Name == "Stop" && e.Handler == "stop":
			e.Targets = append(e.Targets, "gemini")
			e.GeminiEventName = "SessionEnd"
			e.GeminiHandler = "session-end"
		}
	}

	if err := (geminiAdapter{}).Emit(m, repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	hooksRaw, err := os.ReadFile(filepath.Join(outDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	s := string(hooksRaw)

	for _, want := range []string{`"SessionStart"`, `"wipnote hook session-start"`} {
		if !strings.Contains(s, want) {
			t.Errorf("gemini hooks missing %q:\n%s", want, s)
		}
	}
	// Codex-only event must not leak through.
	if strings.Contains(s, `"TaskStarted"`) {
		t.Errorf("gemini hooks should not contain Codex-only TaskStarted:\n%s", s)
	}
	// Claude-only matcher variant (resume) must not leak through — the fixture's
	// matcher:"resume" entry is claude-only and must be filtered out.
	if strings.Contains(s, `"resume"`) {
		t.Errorf("gemini hooks should not contain Claude-only matcher %q:\n%s", "resume", s)
	}
}

// TestGeminiParityFromLiveManifest loads the real manifest and confirms the
// five conservative events Phase 3 tagged for Gemini appear in the emitted
// hooks.json. This guards against manifest drift: if anyone drops "gemini"
// from a targets list, this test fails loudly.
func TestGeminiParityFromLiveManifest(t *testing.T) {
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
	if err := (geminiAdapter{}).Emit(m, repoRoot, outDir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	hooksBytes, err := os.ReadFile(filepath.Join(outDir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	hooks := string(hooksBytes)

	// Gemini event names are translated from Claude conventions via geminiEventName
	// in the manifest. Check that translated names appear and Claude-only names
	// do not leak through.
	for _, want := range []string{
		`"SessionStart"`, // SessionStart → SessionStart (unchanged, no geminiEventName set)
		`"BeforeAgent"`,  // UserPromptSubmit → BeforeAgent
		`"AfterAgent"`,   // Gemini final response capture
		`"BeforeTool"`,   // PreToolUse → BeforeTool
		`"AfterTool"`,    // PostToolUse → AfterTool
		`"SessionEnd"`,   // Stop → SessionEnd
	} {
		if !strings.Contains(hooks, want) {
			t.Errorf("gemini hooks missing %s", want)
		}
	}
	// Claude event names must not appear raw in the Gemini output (they are
	// translated to Gemini equivalents).
	for _, notWant := range []string{
		`"UserPromptSubmit"`,
		`"PreToolUse"`,
		`"PostToolUse"`,
		`"Stop"`,
		`"TaskStarted"`,
		`"TurnAborted"`,
		`"TaskComplete"`,
		`"ExitPlanMode"`,
	} {
		if strings.Contains(hooks, notWant) {
			t.Errorf("gemini hooks contains disallowed %s", notWant)
		}
	}
}
