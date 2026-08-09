package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes a .wipnote/config.json with the given body under dir.
func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	wp := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wp, 0755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wp, "config.json"), []byte(body), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func TestReadLaunchIsolationMode(t *testing.T) {
	cases := []struct {
		name string
		body string
		want LaunchIsolationMode
	}{
		{"enforce", `{"launch_isolation":"enforce"}`, LaunchIsolationEnforce},
		{"auto", `{"launch_isolation":"auto"}`, LaunchIsolationAuto},
		{"warn-only-explicit", `{"launch_isolation":"warn-only"}`, LaunchIsolationWarnOnly},
		{"unknown-value-defaults-warn", `{"launch_isolation":"bogus"}`, LaunchIsolationWarnOnly},
		{"key-absent-defaults-warn", `{"block_task_completion_on_quality_failure":true}`, LaunchIsolationWarnOnly},
		{"empty-object-defaults-warn", `{}`, LaunchIsolationWarnOnly},
		{"malformed-json-defaults-warn", `{not json`, LaunchIsolationWarnOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.body)
			if got := readLaunchIsolationMode(dir); got != tc.want {
				t.Errorf("readLaunchIsolationMode = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadLaunchIsolationMode_NoConfigFile verifies backward compatibility: a repo
// with NO config.json behaves exactly as before (warn-only default).
func TestReadLaunchIsolationMode_NoConfigFile(t *testing.T) {
	dir := t.TempDir() // no .wipnote/config.json
	if got := readLaunchIsolationMode(dir); got != LaunchIsolationWarnOnly {
		t.Errorf("missing config: want warn-only default, got %q", got)
	}
}

func TestResolveIsolationFlags(t *testing.T) {
	cases := []struct {
		name        string
		body        string // empty => no config file written
		env         string // value for WIPNOTE_ENFORCE_ISOLATION ("" => unset)
		wantEnforce bool
		wantAuto    bool
	}{
		{"no-config-no-env", "", "", false, false},
		{"warn-only-no-env", `{"launch_isolation":"warn-only"}`, "", false, false},
		{"enforce-config", `{"launch_isolation":"enforce"}`, "", true, false},
		{"auto-config", `{"launch_isolation":"auto"}`, "", true, true},
		{"env-only-implies-enforce", "", "true", true, false},
		{"env-ors-with-warn-only", `{"launch_isolation":"warn-only"}`, "true", true, false},
		{"auto-config-with-env-stays-auto", `{"launch_isolation":"auto"}`, "true", true, true},
		{"env-false-is-noop", `{"launch_isolation":"warn-only"}`, "false", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.body != "" {
				writeConfig(t, dir, tc.body)
			}
			if tc.env == "" {
				t.Setenv("WIPNOTE_ENFORCE_ISOLATION", "")
				os.Unsetenv("WIPNOTE_ENFORCE_ISOLATION")
			} else {
				t.Setenv("WIPNOTE_ENFORCE_ISOLATION", tc.env)
			}
			enforce, auto := resolveIsolationFlags(dir)
			if enforce != tc.wantEnforce {
				t.Errorf("enforce = %v, want %v", enforce, tc.wantEnforce)
			}
			if auto != tc.wantAuto {
				t.Errorf("autoWorktree = %v, want %v", auto, tc.wantAuto)
			}
		})
	}
}

func TestAdhocBranchName(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 30, 45, 0, time.UTC)
	got := adhocBranchName(now)
	want := "adhoc-20260616-123045"
	if got != want {
		t.Errorf("adhocBranchName = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "adhoc-") {
		t.Errorf("ad-hoc name must be prefixed adhoc-, got %q", got)
	}
}
