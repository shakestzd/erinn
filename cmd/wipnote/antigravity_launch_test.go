package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestWriteAntigravitySystemPrompt verifies the staged orchestrator prompt is
// non-empty and uses agy's run_command, never the Gemini run_shell_command.
func TestWriteAntigravitySystemPrompt(t *testing.T) {
	path, err := writeAntigravitySystemPrompt()
	if err != nil {
		t.Fatalf("writeAntigravitySystemPrompt: %v", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged prompt: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "run_shell_command") {
		t.Error("antigravity orchestrator prompt must use run_command, not run_shell_command")
	}
	if len(strings.TrimSpace(s)) < 100 {
		t.Errorf("orchestrator prompt suspiciously short (%d bytes)", len(strings.TrimSpace(s)))
	}
}

// TestBuildAntigravityArgs verifies that --continue and --resume map to the
// correct agy launch flags (verified live against agy v1.0.8: resume-by-id is
// --conversation <id>, and there is no --resume flag; -c/--continue resumes the
// most recent conversation).
func TestBuildAntigravityArgs(t *testing.T) {
	tests := []struct {
		name      string
		continue_ bool
		resumeID  string
		extraArgs []string
		want      []string
	}{
		{
			name: "no resume/continue — extra args only",
			want: nil,
		},
		{
			name:      "extra args passthrough",
			extraArgs: []string{"--model", "gemini-3.5-flash"},
			want:      []string{"--model", "gemini-3.5-flash"},
		},
		{
			name:      "continue maps to --continue",
			continue_: true,
			want:      []string{"--continue"},
		},
		{
			name:     "resume id maps to --conversation <id>",
			resumeID: "abc-123",
			want:     []string{"--conversation", "abc-123"},
		},
		{
			name:      "resume id takes precedence over continue",
			continue_: true,
			resumeID:  "abc-123",
			want:      []string{"--conversation", "abc-123"},
		},
		{
			name:      "continue with extra args",
			continue_: true,
			extraArgs: []string{"--sandbox"},
			want:      []string{"--continue", "--sandbox"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildAntigravityArgs(tc.continue_, tc.resumeID, tc.extraArgs)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildAntigravityArgs(%v, %q, %v) = %v, want %v",
					tc.continue_, tc.resumeID, tc.extraArgs, got, tc.want)
			}
		})
	}
}
