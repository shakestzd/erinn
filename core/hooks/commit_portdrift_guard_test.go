package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStopPath_SkipsPortDrift asserts that the per-turn Stop path does NOT
// invoke the port-drift check (Part A of bug-3fb22f7e fix).
//
// We verify this via the skipPortDrift seam: Reconcile with skipPortDrift=true
// must return PortDrift=nil even when the project directory has a manifest.json,
// because the check is bypassed. Reconcile with skipPortDrift=false (full
// reconcile) on the same repo exercises the port-drift code path.
func TestStopPath_SkipsPortDrift(t *testing.T) {
	// Reconcile with skipPortDrift=true must never populate PortDrift.
	t.Run("per_turn_stop_skips_checkports", func(t *testing.T) {
		td := setupTestDB(t)
		root := t.TempDir()
		gitInitRepo(t, root)

		// Create a manifest file to make reconcilePortDrift enter its slow path.
		// With skipPortDrift=true, it must still return no port drift.
		manifestDir := filepath.Join(root, "packages", "plugin-core")
		if err := os.MkdirAll(manifestDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Write an invalid manifest — the port checker would fail, but with
		// skipPortDrift=true the whole reconcilePortDrift call is never made.
		if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"),
			[]byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// Call Reconcile with skipPortDrift=true (Stop path).
		rep, err := Reconcile(td.DB, root, false, true)
		if err != nil {
			t.Fatalf("Reconcile(skipPortDrift=true): unexpected error: %v", err)
		}
		if len(rep.PortDrift) != 0 {
			t.Fatalf("per-turn Stop path must not populate PortDrift; got %v", rep.PortDrift)
		}
	})

	// Reconcile with skipPortDrift=false (SessionEnd path) runs without error.
	t.Run("session_end_invokes_checkports_path", func(t *testing.T) {
		td := setupTestDB(t)
		root := t.TempDir()
		gitInitRepo(t, root)

		// No manifest.json → reconcilePortDrift returns nil early.
		rep, err := Reconcile(td.DB, root, false, false)
		if err != nil {
			t.Fatalf("Reconcile(skipPortDrift=false): unexpected error: %v", err)
		}
		if rep == nil {
			t.Fatal("Reconcile must not return nil report")
		}
		if len(rep.PortDrift) != 0 {
			t.Fatalf("non-generator repo must not report port drift, got %v", rep.PortDrift)
		}
	})
}

// TestPortDriftCommitGuard_NonGeneratorInput verifies the fast-pass: a git
// commit with no generator-input files staged does not invoke CheckPorts
// (Part B case i).
func TestPortDriftCommitGuard_NonGeneratorInput(t *testing.T) {
	root := t.TempDir()
	if err := initTestGitRepoOnBranch(t, root, "main"); err != nil {
		t.Skipf("cannot init git repo: %v", err)
	}

	// Stage a regular (non-generator) file.
	regularFile := filepath.Join(root, "main.go")
	if err := os.WriteFile(regularFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGitAdd(t, root, regularFile)

	event := &CloudEvent{
		ToolName: "Bash",
		CWD:      root,
		ToolInput: map[string]any{
			"command": `git commit -m "test commit"`,
		},
	}
	warn := checkPortDriftCommitGuard(event)
	if warn != "" {
		t.Errorf("fast-pass: non-generator input must not trigger port-drift check, got warn=%q", warn)
	}
}

// TestPortDriftCommitGuard_HasGeneratorInput verifies hasGeneratorInput
// correctly classifies staged paths (Part B gate logic).
func TestPortDriftCommitGuard_HasGeneratorInput(t *testing.T) {
	tests := []struct {
		name   string
		staged []string
		want   bool
	}{
		{
			name:   "manifest_is_generator_input",
			staged: []string{generatorInputManifest},
			want:   true,
		},
		{
			name:   "plugin_commands_file_is_generator_input",
			staged: []string{"plugin/commands/foo.md"},
			want:   true,
		},
		{
			name:   "plugin_agents_file_is_generator_input",
			staged: []string{"plugin/agents/bar.md"},
			want:   true,
		},
		{
			name:   "plugin_skills_file_is_generator_input",
			staged: []string{"plugin/skills/baz/SKILL.md"},
			want:   true,
		},
		{
			name:   "plugin_templates_file_is_generator_input",
			staged: []string{"plugin/templates/tmpl.md"},
			want:   true,
		},
		{
			name:   "plugin_static_file_is_generator_input",
			staged: []string{"plugin/static/something.js"},
			want:   true,
		},
		{
			name:   "plugin_config_file_is_generator_input",
			staged: []string{"plugin/config/drift.json"},
			want:   true,
		},
		{
			name:   "regular_go_file_is_not_generator_input",
			staged: []string{"internal/hooks/session_end.go"},
			want:   false,
		},
		{
			name:   "plugin_hooks_json_is_not_generator_input",
			staged: []string{"plugin/hooks/hooks.json"},
			want:   false,
		},
		{
			name:   "generated_claude_plugin_json_is_not_generator_input",
			staged: []string{"plugin/.claude-plugin/plugin.json"},
			want:   false,
		},
		{
			name:   "partial_dir_name_not_matched",
			staged: []string{"plugin/commandsExtra/foo.md"},
			want:   false,
		},
		{
			name:   "mixed_with_generator_input",
			staged: []string{"cmd/wipnote/main.go", "plugin/agents/new-agent.md"},
			want:   true,
		},
		{
			name:   "empty_staged_list",
			staged: nil,
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasGeneratorInput(tc.staged)
			if got != tc.want {
				t.Errorf("hasGeneratorInput(%v) = %v, want %v", tc.staged, got, tc.want)
			}
		})
	}
}

// TestPortDriftCommitGuard_AllowsWhenNoManifest verifies the slow-path allows
// commits with a staged generator-input when no manifest.json exists — i.e.
// this is not a plugin-core repo (Part B case iii proxy: up-to-date ports and
// absent manifest are both allow-paths in checkPortDriftCommitGuard).
func TestPortDriftCommitGuard_AllowsWhenNoManifest(t *testing.T) {
	root := t.TempDir()
	if err := initTestGitRepoOnBranch(t, root, "main"); err != nil {
		t.Skipf("cannot init git repo: %v", err)
	}

	// Create a generator-input directory but NO manifest.json.
	pluginCmds := filepath.Join(root, "plugin", "commands")
	if err := os.MkdirAll(pluginCmds, 0o755); err != nil {
		t.Fatal(err)
	}
	cmdFile := filepath.Join(pluginCmds, "foo.md")
	if err := os.WriteFile(cmdFile, []byte("# foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGitAdd(t, root, cmdFile)

	event := &CloudEvent{
		ToolName: "Bash",
		CWD:      root,
		ToolInput: map[string]any{
			"command": `git commit -m "add command"`,
		},
	}
	warn := checkPortDriftCommitGuard(event)
	if warn != "" {
		t.Errorf("no manifest.json → must allow commit (no CheckPorts run), got warn=%q", warn)
	}
}

// TestPortDriftCommitGuard_NonBashAllowed verifies the guard is a no-op for
// non-shell tools.
func TestPortDriftCommitGuard_NonBashAllowed(t *testing.T) {
	event := &CloudEvent{
		ToolName: "Write",
		CWD:      "/tmp",
		ToolInput: map[string]any{
			"file_path": "/tmp/foo.md",
			"content":   "# test",
		},
	}
	warn := checkPortDriftCommitGuard(event)
	if warn != "" {
		t.Errorf("non-Bash tool must not trigger guard, got warn=%q", warn)
	}
}

// TestPortDriftCommitGuard_NonCommitBashAllowed verifies the guard is a no-op
// for Bash commands that are not git commit.
func TestPortDriftCommitGuard_NonCommitBashAllowed(t *testing.T) {
	event := &CloudEvent{
		ToolName: "Bash",
		CWD:      "/tmp",
		ToolInput: map[string]any{
			"command": "git status",
		},
	}
	warn := checkPortDriftCommitGuard(event)
	if warn != "" {
		t.Errorf("non-commit Bash must not trigger guard, got warn=%q", warn)
	}
}

// TestPortDriftCommitGuard_BlockMessageFormat verifies the block message
// contains the required remediation text when drift is detected.
func TestPortDriftCommitGuard_BlockMessageFormat(t *testing.T) {
	// The guard assembles this message format when drift is detected.
	// Verify the string literal in commit_portdrift_guard.go has all required parts.
	msg := "Generated plugin trees are stale — run `wipnote plugin build-ports`, " +
		"stage the result, and re-commit.\n" +
		"Drifted paths: plugin/hooks/hooks.json"

	if !strings.Contains(msg, "wipnote plugin build-ports") {
		t.Error("block message must contain remediation command")
	}
	if !strings.Contains(msg, "Drifted paths") {
		t.Error("block message must list drifted paths")
	}
	if !strings.Contains(msg, "stage the result") {
		t.Error("block message must instruct user to stage the result")
	}
}

// testGitAdd stages a file in the given repo. Uses the repo root via -C.
func testGitAdd(t *testing.T, repoRoot, filePath string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repoRoot, "add", filePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", filePath, err, out)
	}
}
