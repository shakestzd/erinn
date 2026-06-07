package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeRoborevScript creates a fake "roborev" binary in a temp dir that echoes
// the given output and prepends the dir to PATH. It returns the dir so the
// caller can clean up (via t.TempDir, which auto-cleans).
//
// Note: t.TempDir() resolves to /tmp which is mounted noexec in some
// environments (e.g. Codespaces devcontainers). We use os.MkdirTemp with
// "." as the root so the fake binary lands in the package source directory,
// which is always on an exec-capable filesystem.
func makeRoborevScript(t *testing.T, output string) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "roborev-fake-*")
	if err != nil {
		t.Fatalf("mkdirtemp for fake roborev: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	script := filepath.Join(dir, "roborev")
	content := "#!/bin/sh\necho '" + output + "'\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake roborev: %v", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs path for fake roborev dir: %v", err)
	}
	t.Setenv("PATH", absDir+":"+os.Getenv("PATH"))
	return absDir
}

// commitEvent builds a minimal CloudEvent that looks like a yolo git-commit
// Bash invocation, optionally with a CWD set.
func commitEvent(cwd string) *CloudEvent {
	return &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git commit -m 'test'"},
		CWD:       cwd,
	}
}

// TestCheckYoloRoborevGuard_NonYoloBypass verifies that yolo=false is a no-op.
func TestCheckYoloRoborevGuard_NonYoloBypass(t *testing.T) {
	// No fake roborev needed — guard must short-circuit before exec.
	event := commitEvent("")
	result := checkYoloRoborevGuard(event, false)
	if result != "" {
		t.Errorf("expected allow (non-yolo), got: %s", result)
	}
}

// TestCheckYoloRoborevGuard_NonCommitBypass verifies that non-commit Bash
// commands are not blocked even in yolo mode.
func TestCheckYoloRoborevGuard_NonCommitBypass(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git status"},
	}
	result := checkYoloRoborevGuard(event, true)
	if result != "" {
		t.Errorf("expected allow (non-commit command), got: %s", result)
	}
}

// TestCheckYoloRoborevGuard_NoFindings verifies that an empty findings list
// allows the commit.
func TestCheckYoloRoborevGuard_NoFindings(t *testing.T) {
	makeRoborevScript(t, "[]")
	result := checkYoloRoborevGuard(commitEvent(""), true)
	if result != "" {
		t.Errorf("expected allow (no findings), got: %s", result)
	}
}

// TestCheckYoloRoborevGuard_OpenFindings verifies that a completed review with
// verdict "F" blocks the commit and includes the job ID in the message.
func TestCheckYoloRoborevGuard_OpenFindings(t *testing.T) {
	makeRoborevScript(t, `[{"id":"j1","verdict":"F","commit_subject":"bad commit"}]`)
	result := checkYoloRoborevGuard(commitEvent(""), true)
	if result == "" {
		t.Fatal("expected block (open findings with verdict F), got allow")
	}
	if !strings.Contains(result, "j1") {
		t.Errorf("expected block message to contain job ID 'j1', got: %s", result)
	}
}

// TestCheckYoloRoborevGuard_RunningReview verifies that a still-running review
// (empty verdict) does not block.
func TestCheckYoloRoborevGuard_RunningReview(t *testing.T) {
	makeRoborevScript(t, `[{"id":"j2","verdict":"","commit_subject":"pending"}]`)
	result := checkYoloRoborevGuard(commitEvent(""), true)
	if result != "" {
		t.Errorf("expected allow (running review, no verdict yet), got: %s", result)
	}
}

// TestCheckYoloRoborevGuard_MalformedJSON verifies fail-open on bad output.
func TestCheckYoloRoborevGuard_MalformedJSON(t *testing.T) {
	makeRoborevScript(t, "not valid json {{{")
	result := checkYoloRoborevGuard(commitEvent(""), true)
	if result != "" {
		t.Errorf("expected fail-open (malformed JSON), got: %s", result)
	}
}

// TestCheckYoloRoborevGuard_CommandNotFound verifies fail-open when roborev is
// not on PATH.
func TestCheckYoloRoborevGuard_CommandNotFound(t *testing.T) {
	// Point PATH at an empty temp dir so no "roborev" binary is found.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)
	result := checkYoloRoborevGuard(commitEvent(""), true)
	if result != "" {
		t.Errorf("expected fail-open (roborev not found), got: %s", result)
	}
}
