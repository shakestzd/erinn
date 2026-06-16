package main

import "testing"

func TestAntigravityInitAlias(t *testing.T) {
	if !isAntigravityInitAlias([]string{"init"}) {
		t.Fatal("init positional arg should be treated as --init alias")
	}

	tests := []struct {
		name string
		args []string
	}{
		{name: "nil", args: nil},
		{name: "empty", args: []string{}},
		{name: "other", args: []string{"status"}},
		{name: "extra", args: []string{"init", "extra"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if isAntigravityInitAlias(tt.args) {
				t.Fatalf("isAntigravityInitAlias(%v) = true, want false", tt.args)
			}
		})
	}
}

func TestAntigravityCmd_HasTmuxFlag(t *testing.T) {
	cmd := antigravityCmd()
	f := cmd.Flags().Lookup("tmux")
	if f == nil {
		t.Fatal("antigravity command is missing the --tmux flag")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--tmux flag type = %q, want bool", f.Value.Type())
	}
}
