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
