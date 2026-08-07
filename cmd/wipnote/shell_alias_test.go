package main

import (
	"strings"
	"testing"
)

func TestShellAliasSnippet_ContainsExpectedAliases(t *testing.T) {
	out := shellAliasSnippet()
	for _, want := range []string{
		"alias claude='wipnote claude'",
		"alias codex='wipnote codex'",
		"alias agy='wipnote antigravity'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("shellAliasSnippet missing %q. Got:\n%s", want, out)
		}
	}
}

func TestShellAliasCmd_Help(t *testing.T) {
	cmd := shellAliasCmd()
	if cmd.Use != "shell-alias" {
		t.Errorf("Use = %q, want %q", cmd.Use, "shell-alias")
	}
	if cmd.Short == "" {
		t.Error("Short is empty")
	}
}
