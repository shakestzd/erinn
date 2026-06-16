package main

import "testing"

func TestIsWipnoteStatusLineCommand(t *testing.T) {
	cases := map[string]bool{
		"/home/u/.local/bin/wipnote statusline --cache": true,
		"wipnote statusline --cache":                     true,
		"wipnote statusline --session abc":               true,
		"oh-my-posh print primary":                       false,
		"":                                               false,
		"some-other-status":                              false,
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
