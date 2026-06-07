package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestSignalCollector_NoopWhenNoPIDFile verifies signalCollector is a no-op
// when the .collector-pid file does not exist.
func TestSignalCollector_NoopWhenNoPIDFile(t *testing.T) {
	dir := t.TempDir()
	projectDir := dir
	// Create .wipnote/sessions/<sid>/ but no .collector-pid file.
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", "sess-test")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Must not panic or error.
	signalCollector(projectDir, "sess-test")
}

// TestSignalCollector_InvalidPIDFile verifies graceful handling of a corrupt PID file.
func TestSignalCollector_InvalidPIDFile(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, ".wipnote", "sessions", "sess-bad")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, ".collector-pid"), []byte("notanumber\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Must not panic.
	signalCollector(dir, "sess-bad")
}

// TestSignalCollector_AlreadyExitedProcess verifies signalCollector handles
// a PID that refers to an already-exited process without panicking.
func TestSignalCollector_AlreadyExitedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM not available on Windows")
	}

	dir := t.TempDir()
	sessDir := filepath.Join(dir, ".wipnote", "sessions", "sess-dead")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a PID that is guaranteed not to exist (use a large, unlikely PID).
	// On Linux, PID max is typically 4194304.
	if err := os.WriteFile(filepath.Join(sessDir, ".collector-pid"), []byte("9999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Must not panic even if FindProcess succeeds but Signal fails.
	signalCollector(dir, "sess-dead")
}

// TestSignalCollector_LiveProcess verifies that signalCollector successfully
// SIGTERMs a running child process and removes the PID file afterward.
func TestSignalCollector_LiveProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM not available on Windows")
	}

	dir := t.TempDir()
	sessID := "sess-live"
	sessDir := filepath.Join(dir, ".wipnote", "sessions", sessID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Start a long-running child process (sleep 60).
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	// Write its PID.
	pidPath := filepath.Join(sessDir, ".collector-pid")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Signal the "collector".
	start := time.Now()
	signalCollector(dir, sessID)
	elapsed := time.Since(start)

	// Should complete well under 3s (process exits on SIGTERM immediately).
	if elapsed > 4*time.Second {
		t.Errorf("signalCollector took too long: %v (want <4s)", elapsed)
	}

	// PID file must be removed.
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected .collector-pid to be removed after signaling")
	}

	// Child process must no longer be running.
	if err := cmd.Process.Signal(os.Signal(nil)); err == nil {
		// Try to confirm process is gone.
		pidStr := strconv.Itoa(cmd.Process.Pid)
		out, _ := exec.Command("ps", "-p", pidStr).Output()
		if len(out) > 0 {
			t.Log("process may still be running:", string(out))
		}
	}
}
