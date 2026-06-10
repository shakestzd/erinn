package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeGeminiHooks(t *testing.T) {
	bundledRaw := []byte(`{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "wipnote hook session-start"
          }
        ]
      }
    ],
    "BeforeAgent": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "wipnote hook user-prompt"
          }
        ]
      }
    ]
  }
}`)

	existingRaw := []byte(`{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "my-custom-hook"
          }
        ]
      }
    ]
  }
}`)

	merged, err := mergeGeminiHooks(existingRaw, bundledRaw)
	if err != nil {
		t.Fatalf("mergeGeminiHooks failed: %v", err)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(merged, &result); err != nil {
		t.Fatalf("unmarshal merged failed: %v", err)
	}

	var hooks map[string][]geminiHookGroup
	if err := json.Unmarshal(result["hooks"], &hooks); err != nil {
		t.Fatalf("unmarshal hooks failed: %v", err)
	}

	// Check SessionStart merge
	sessionStart := hooks["SessionStart"]
	if len(sessionStart) != 1 {
		t.Errorf("expected 1 SessionStart group, got %d", len(sessionStart))
	} else {
		group := sessionStart[0]
		if group.Matcher != "*" {
			t.Errorf("expected matcher *, got %s", group.Matcher)
		}
		if len(group.Hooks) != 2 {
			t.Errorf("expected 2 hooks in SessionStart, got %d", len(group.Hooks))
		}
		foundCustom := false
		foundWipnote := false
		for _, h := range group.Hooks {
			if h.Command == "my-custom-hook" {
				foundCustom = true
			}
			if h.Command == "wipnote hook session-start" {
				foundWipnote = true
			}
		}
		if !foundCustom {
			t.Errorf("missing my-custom-hook")
		}
		if !foundWipnote {
			t.Errorf("missing wipnote hook session-start")
		}
	}

	// Check BeforeAgent merge (new event)
	beforeAgent := hooks["BeforeAgent"]
	if len(beforeAgent) != 1 {
		t.Errorf("expected 1 BeforeAgent group, got %d", len(beforeAgent))
	} else {
		group := beforeAgent[0]
		if len(group.Hooks) != 1 {
			t.Errorf("expected 1 hook in BeforeAgent, got %d", len(group.Hooks))
		}
		if group.Hooks[0].Command != "wipnote hook user-prompt" {
			t.Errorf("expected wipnote hook user-prompt, got %s", group.Hooks[0].Command)
		}
	}
}

func TestInstallGeminiHooksDryRun(t *testing.T) {
	// This test just ensures the dry-run path doesn't crash.
	err := installGeminiHooks("", true)
	if err != nil {
		t.Fatalf("installGeminiHooks dry-run failed: %v", err)
	}
}

func TestGeminiSettingsJSONPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	path := geminiSettingsJSONPath("")
	expected := filepath.Join(home, ".gemini", "settings.json")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}

	path = geminiSettingsJSONPath("wipnote")
	expected = filepath.Join(home, ".gemini", "extensions", "wipnote", "settings.json")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

// canonicalHooksJSON is a minimal valid hooks.json in the same format as
// port/packages/gemini-extension/hooks/hooks.json, used by installGeminiHooksAt tests.
const canonicalHooksJSON = `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "wipnote hook session-start"
          }
        ]
      }
    ],
    "BeforeAgent": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "wipnote hook user-prompt"
          }
        ]
      }
    ]
  }
}`

// writeHooksFile writes content to <dir>/hooks/hooks.json and returns its path.
func writeHooksFile(t *testing.T, dir, content string) string {
	t.Helper()
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("MkdirAll hooks dir: %v", err)
	}
	hooksPath := filepath.Join(hooksDir, "hooks.json")
	if err := os.WriteFile(hooksPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile hooks.json: %v", err)
	}
	return hooksPath
}

// parseSettingsHooks reads settingsPath and returns the hooks section.
func parseSettingsHooks(t *testing.T, settingsPath string) map[string][]geminiHookGroup {
	t.Helper()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile settings: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	var hooks map[string][]geminiHookGroup
	if err := json.Unmarshal(top["hooks"], &hooks); err != nil {
		t.Fatalf("unmarshal hooks: %v", err)
	}
	return hooks
}

// TestInstallGeminiHooksAt_EmptySettings verifies that when settings.json does
// not exist, installGeminiHooksAt creates it with the wipnote hooks installed.
func TestInstallGeminiHooksAt_EmptySettings(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hooksPath := writeHooksFile(t, dir, canonicalHooksJSON)

	if err := installGeminiHooksAt(settingsPath, hooksPath); err != nil {
		t.Fatalf("installGeminiHooksAt: %v", err)
	}

	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	hooks := parseSettingsHooks(t, settingsPath)
	sessionStart, ok := hooks["SessionStart"]
	if !ok || len(sessionStart) == 0 {
		t.Fatal("expected SessionStart hook after install, got none")
	}
	found := false
	for _, g := range sessionStart {
		for _, h := range g.Hooks {
			if h.Command == "wipnote hook session-start" {
				found = true
			}
		}
	}
	if !found {
		t.Error("wipnote hook session-start not installed into empty settings")
	}
}

// TestInstallGeminiHooksAt_UnrelatedKeysPreserved verifies that existing
// settings keys unrelated to "hooks" are preserved after the merge.
func TestInstallGeminiHooksAt_UnrelatedKeysPreserved(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hooksPath := writeHooksFile(t, dir, canonicalHooksJSON)

	existing := `{"theme":"dark","fontSize":14}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := installGeminiHooksAt(settingsPath, hooksPath); err != nil {
		t.Fatalf("installGeminiHooksAt: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := merged["theme"]; !ok {
		t.Error("theme key was lost after merge")
	}
	if _, ok := merged["fontSize"]; !ok {
		t.Error("fontSize key was lost after merge")
	}
	if _, ok := merged["hooks"]; !ok {
		t.Error("hooks key missing after merge")
	}
}

// TestInstallGeminiHooksAt_NonWipnoteHookPreserved verifies that when an existing
// non-wipnote hook is on the same event, both entries are kept (append, not replace).
func TestInstallGeminiHooksAt_NonWipnoteHookPreserved(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hooksPath := writeHooksFile(t, dir, canonicalHooksJSON)

	existing := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "*",
        "hooks": [
          {"type": "command", "command": "my-other-tool hook init"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := installGeminiHooksAt(settingsPath, hooksPath); err != nil {
		t.Fatalf("installGeminiHooksAt: %v", err)
	}

	hooks := parseSettingsHooks(t, settingsPath)
	sessionStart := hooks["SessionStart"]
	if len(sessionStart) == 0 {
		t.Fatal("no SessionStart groups after merge")
	}
	foundCustom, foundWipnote := false, false
	for _, g := range sessionStart {
		for _, h := range g.Hooks {
			if h.Command == "my-other-tool hook init" {
				foundCustom = true
			}
			if h.Command == "wipnote hook session-start" {
				foundWipnote = true
			}
		}
	}
	if !foundCustom {
		t.Error("non-wipnote hook was removed (should have been preserved)")
	}
	if !foundWipnote {
		t.Error("wipnote hook was not added alongside the non-wipnote hook")
	}
}

// TestInstallGeminiHooksAt_StaleWipnoteHookReplaced verifies that an existing
// old wipnote hook (command prefix "wipnote ") is replaced, not duplicated.
func TestInstallGeminiHooksAt_StaleWipnoteHookReplaced(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hooksPath := writeHooksFile(t, dir, canonicalHooksJSON)

	// Simulate an older wipnote hook with a different command on the same event.
	existing := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "*",
        "hooks": [
          {"type": "command", "command": "wipnote hook old-session-start"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := installGeminiHooksAt(settingsPath, hooksPath); err != nil {
		t.Fatalf("installGeminiHooksAt: %v", err)
	}

	hooks := parseSettingsHooks(t, settingsPath)
	sessionStart := hooks["SessionStart"]
	if len(sessionStart) == 0 {
		t.Fatal("no SessionStart groups after merge")
	}

	var allCommands []string
	for _, g := range sessionStart {
		for _, h := range g.Hooks {
			allCommands = append(allCommands, h.Command)
		}
	}

	foundOld := false
	foundNew := false
	for _, cmd := range allCommands {
		if cmd == "wipnote hook old-session-start" {
			foundOld = true
		}
		if cmd == "wipnote hook session-start" {
			foundNew = true
		}
	}
	if foundOld {
		t.Errorf("stale wipnote hook was kept; commands: %v", allCommands)
	}
	if !foundNew {
		t.Errorf("new wipnote hook was not installed; commands: %v", allCommands)
	}
}

// TestInstallGeminiHooksAt_Idempotent verifies that re-running installGeminiHooksAt
// when the current wipnote hooks are already present does not duplicate them.
func TestInstallGeminiHooksAt_Idempotent(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hooksPath := writeHooksFile(t, dir, canonicalHooksJSON)

	// Run once to install.
	if err := installGeminiHooksAt(settingsPath, hooksPath); err != nil {
		t.Fatalf("first installGeminiHooksAt: %v", err)
	}

	data1, _ := os.ReadFile(settingsPath)

	// Run again — should be idempotent.
	if err := installGeminiHooksAt(settingsPath, hooksPath); err != nil {
		t.Fatalf("second installGeminiHooksAt: %v", err)
	}

	data2, _ := os.ReadFile(settingsPath)

	// The output JSON content should be the same.
	if string(data1) != string(data2) {
		t.Errorf("second install changed settings.json (not idempotent):\nbefore: %s\nafter:  %s", data1, data2)
	}

	// Confirm entries are not duplicated.
	hooks := parseSettingsHooks(t, settingsPath)
	sessionStart := hooks["SessionStart"]
	var wipnoteCount int
	for _, g := range sessionStart {
		for _, h := range g.Hooks {
			if h.Command == "wipnote hook session-start" {
				wipnoteCount++
			}
		}
	}
	if wipnoteCount != 1 {
		t.Errorf("expected exactly 1 wipnote hook session-start, got %d", wipnoteCount)
	}
}

// TestInstallGeminiHooksAt_MalformedJSON verifies that a malformed settings.json
// causes a clear error and no write occurs.
func TestInstallGeminiHooksAt_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	hooksPath := writeHooksFile(t, dir, canonicalHooksJSON)

	// Write malformed JSON to settings.
	if err := os.WriteFile(settingsPath, []byte(`{not valid json`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	originalContent := `{not valid json`

	err := installGeminiHooksAt(settingsPath, hooksPath)
	if err == nil {
		t.Fatal("expected error for malformed settings.json, got nil")
	}
	if !strings.Contains(err.Error(), "malformed JSON") {
		t.Errorf("expected 'malformed JSON' in error, got: %v", err)
	}

	// Verify the file was not modified.
	data, _ := os.ReadFile(settingsPath)
	if string(data) != originalContent {
		t.Errorf("malformed settings.json was overwritten (should not be touched):\ngot: %s", data)
	}
}
