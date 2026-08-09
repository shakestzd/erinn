package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// launchMarkerFile mirrors the JSON shape core/hooks/session_start.go's
// launchModeFile and core/hooks/session_liveness.go's resolveOwningPID expect
// from .wipnote/.launch-mode. It is duplicated here (rather than imported)
// because core/hooks is a different module boundary (cmd -> internal/core,
// never the reverse) — this struct exists purely to decode what
// writeLaunchMarker produced and assert it matches the reader's contract.
type launchMarkerFile struct {
	Mode      string `json:"mode"`
	PID       int    `json:"pid"`
	Timestamp string `json:"timestamp"`
}

// TestWriteLaunchMarker_ShapeMatchesFreshnessCheck verifies writeLaunchMarker
// produces the exact {mode,pid,timestamp} shape that
// core/hooks/session_liveness.go's resolveOwningPID and
// core/hooks/session_start.go's bareLaunchNudge decode, with a fresh mtime
// and the calling process's own pid (the launcher's pid, not any child's —
// resolveOwningPID polls this pid for liveness for as long as the launcher
// blocks in runHarnessWithCleanup).
func TestWriteLaunchMarker_ShapeMatchesFreshnessCheck(t *testing.T) {
	root := t.TempDir()

	writeLaunchMarker("default", root)

	markerPath := filepath.Join(root, ".wipnote", ".launch-mode")
	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatalf("expected marker at %s, stat err=%v", markerPath, err)
	}
	if age := time.Since(info.ModTime()); age > 5*time.Second {
		t.Fatalf("marker mtime not fresh: age=%v", age)
	}

	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("reading marker: %v", err)
	}
	var lm launchMarkerFile
	if err := json.Unmarshal(data, &lm); err != nil {
		t.Fatalf("marker is not valid JSON in the expected shape: %v\ncontent: %s", err, data)
	}
	if lm.Mode != "default" {
		t.Errorf("mode = %q, want %q", lm.Mode, "default")
	}
	if lm.PID != os.Getpid() {
		t.Errorf("pid = %d, want current process pid %d", lm.PID, os.Getpid())
	}
	if lm.Timestamp == "" {
		t.Error("timestamp is empty")
	}
	if _, err := time.Parse(time.RFC3339, lm.Timestamp); err != nil {
		t.Errorf("timestamp %q is not RFC3339: %v", lm.Timestamp, err)
	}
}

// TestWriteLaunchMarker_EmptyProjectRootSkipsWrite documents the documented
// guard in writeLaunchMarker: an empty projectRoot must not pollute cwd.
func TestWriteLaunchMarker_EmptyProjectRootSkipsWrite(t *testing.T) {
	oldWD, _ := os.Getwd()
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	writeLaunchMarker("default", "")

	if _, err := os.Stat(filepath.Join(tmp, ".wipnote")); !os.IsNotExist(err) {
		t.Fatalf("expected no .wipnote/ created in cwd for empty projectRoot, stat err=%v", err)
	}
}

// TestExecCodexWritesFreshLaunchMarker is the spk-5a716533 / bug-632fe31f
// proof: a Codex session started through execCodex must now be
// distinguishable from a bare `codex` session via .wipnote/.launch-mode,
// exactly like the Claude and Antigravity launchers already are (claude.go,
// antigravity_launch.go). Before the fix under test, this asserted a marker
// that execCodex never wrote — running it against the pre-fix code fails on
// the os.Stat below.
//
// codex is stubbed on PATH so this never talks to a real Codex CLI or model:
// execCodex's only external calls before exec'ing the child are
// `codex debug models` (to build model-instructions config args) and the
// final launch invocation itself — both handled by exiting 0 (with a minimal
// model catalog for the former, since a `debug models` failure is logged and
// otherwise ignored, but keeping it green keeps the test representative of a
// real run rather than a degraded one).
func TestExecCodexWritesFreshLaunchMarker(t *testing.T) {
	projectRoot := t.TempDir()
	binDir := t.TempDir()

	stub := "#!/bin/sh\n" +
		"if [ \"$1\" = debug ] && [ \"$2\" = models ]; then\n" +
		"  echo '{\"models\":[{\"slug\":\"test-model\",\"base_instructions\":\"test instructions\"}]}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Keep this test hermetic: no real dashboard autostart, no real OTel
	// collector spawn, no real DB path under the developer's actual cache dir.
	t.Setenv("WIPNOTE_NO_AUTO_SERVE", "1")
	t.Setenv("WIPNOTE_OTEL_ENABLED", "0")
	t.Setenv("WIPNOTE_DB_PATH", filepath.Join(projectRoot, "wipnote.db"))
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")

	markerPath := filepath.Join(projectRoot, ".wipnote", ".launch-mode")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("marker must not exist before launch, stat err=%v", err)
	}

	before := time.Now()
	if err := execCodex(codexLaunchOpts{
		ProjectRoot: projectRoot,
		Mode:        codexLaunchModeDefault,
	}); err != nil {
		t.Fatalf("execCodex: %v", err)
	}

	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatalf("expected a launch marker after execCodex (this is what makes a launched Codex session distinguishable from a bare one), stat err=%v", err)
	}
	if info.ModTime().Before(before.Add(-time.Second)) {
		t.Fatalf("marker mtime %v predates the launch (before=%v) — stale, not fresh", info.ModTime(), before)
	}

	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("reading marker: %v", err)
	}
	var lm launchMarkerFile
	if err := json.Unmarshal(data, &lm); err != nil {
		t.Fatalf("marker is not valid JSON: %v\ncontent: %s", err, data)
	}
	if lm.Mode != string(codexLaunchModeDefault) {
		t.Errorf("mode = %q, want %q", lm.Mode, codexLaunchModeDefault)
	}
	if lm.PID != os.Getpid() {
		t.Errorf("pid = %d, want the wipnote launcher's own pid %d (it stays alive for the session's duration; codex runs as its child)", lm.PID, os.Getpid())
	}
}
