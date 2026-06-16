package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureAntigravityStatusLine_NullSettings guards against a panic when the
// existing agy settings.json is a literal `null` (unmarshals to a nil map).
func TestEnsureAntigravityStatusLine_NullSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WIPNOTE_ANTIGRAVITY_STATUSLINE", "")
	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Must not panic on the nil map produced by unmarshalling `null`.
	ensureAntigravityStatusLine(false)

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json unreadable after ensure: %v", err)
	}
	if !strings.Contains(string(raw), "statusLine") {
		t.Errorf("statusLine not written into settings.json; got: %s", raw)
	}
}

func TestIsWipnoteStatusLineCommand(t *testing.T) {
	cases := map[string]bool{
		"/home/u/.local/bin/wipnote statusline --cache": true,
		"'/home/u/with space/wipnote' statusline --cache": true,
		"wipnote statusline --cache":                       true,
		// --session is the Claude-managed command shape; the agy launcher only
		// ever writes --cache, so a --session command is not ours to refresh.
		"wipnote statusline --session abc": false,
		"oh-my-posh print primary":         false,
		"":                                 false,
		"some-other-status":                false,
	}
	for cmd, want := range cases {
		if got := isWipnoteStatusLineCommand(cmd); got != want {
			t.Errorf("isWipnoteStatusLineCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestMergeAntigravityStatusLine(t *testing.T) {
	const cmd = "/usr/bin/wipnote statusline --cache"

	t.Run("sets when absent", func(t *testing.T) {
		s := map[string]any{"colorScheme": "dark"}
		if !mergeAntigravityStatusLine(s, cmd) {
			t.Fatal("expected changed=true when statusLine absent")
		}
		sl, ok := s["statusLine"].(map[string]any)
		if !ok {
			t.Fatalf("statusLine not set: %#v", s["statusLine"])
		}
		if sl["command"] != cmd || sl["type"] != "command" {
			t.Errorf("unexpected statusLine: %#v", sl)
		}
		if s["colorScheme"] != "dark" {
			t.Error("unrelated keys must be preserved")
		}
	})

	t.Run("no-op when already current", func(t *testing.T) {
		s := map[string]any{"statusLine": map[string]any{"type": "command", "command": cmd, "padding": 0}}
		if mergeAntigravityStatusLine(s, cmd) {
			t.Error("expected changed=false when statusLine already current")
		}
	})

	t.Run("refreshes wipnote-managed command", func(t *testing.T) {
		s := map[string]any{"statusLine": map[string]any{"type": "command", "command": "/old/path/wipnote statusline --cache"}}
		if !mergeAntigravityStatusLine(s, cmd) {
			t.Fatal("expected changed=true when wipnote-managed path differs")
		}
		sl := s["statusLine"].(map[string]any)
		if sl["command"] != cmd {
			t.Errorf("command not refreshed: %#v", sl["command"])
		}
	})

	t.Run("never clobbers user-authored statusLine", func(t *testing.T) {
		userCmd := "oh-my-posh print primary"
		s := map[string]any{"statusLine": map[string]any{"type": "command", "command": userCmd}}
		if mergeAntigravityStatusLine(s, cmd) {
			t.Error("expected changed=false for user-authored statusLine")
		}
		sl := s["statusLine"].(map[string]any)
		if sl["command"] != userCmd {
			t.Errorf("user statusLine was clobbered: %#v", sl["command"])
		}
	})
}
