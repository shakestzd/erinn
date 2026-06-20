package main

import (
	"os"
	"strings"
	"testing"
)

func TestWithHarnessEnv_ReplacesInheritedParentHarness(t *testing.T) {
	for _, tc := range []struct {
		name    string
		harness string
	}{
		{"codex", harnessCodex},
		{"gemini", harnessGemini},
		{"antigravity", harnessAntigravity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := withHarnessEnv([]string{
				"PATH=/bin",
				harnessEnvKey + "=" + harnessClaude,
			}, tc.harness)

			assertEnvContains(t, env, harnessEnvKey, tc.harness)
			if got := countEnvKey(env, harnessEnvKey); got != 1 {
				t.Fatalf("%s count = %d, want 1 in %#v", harnessEnvKey, got, env)
			}
		})
	}
}

func TestNonClaudeLaunchersStampHarnessEnv(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"codex.go", "withHarnessEnv(env, harnessCodex)"},
		{"gemini_launch.go", "withHarnessEnv(env, harnessGemini)"},
		{"antigravity_launch.go", "withHarnessEnv(env, harnessAntigravity)"},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			if !strings.Contains(string(data), tc.want) {
				t.Fatalf("%s must contain %q", tc.file, tc.want)
			}
		})
	}
}

func countEnvKey(env []string, key string) int {
	prefix := key + "="
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			count++
		}
	}
	return count
}
