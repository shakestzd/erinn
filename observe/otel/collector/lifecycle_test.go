package collector_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/observe/otel/collector"
)

// startSleepProc starts a long-lived /bin/sh sleep process for test injection.
func startSleepProc(t *testing.T) *os.Process {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep proc: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd.Process
}

// TestProcessCollector_Spawn_Success injects a fake SpawnFn returning
// (port=12345, a real sleep process, nil). Asserts port=12345 and cleanup is
// non-nil. Calls cleanup and asserts no panic.
func TestProcessCollector_Spawn_Success(t *testing.T) {
	var buf bytes.Buffer
	lc := collector.NewProcessCollector(collector.ProcessCollectorOpts{
		Stderr: &buf,
		SpawnFn: func(binPath, sessionID, projectDir string, requestedPort int) (int, *os.Process, error) {
			return 12345, startSleepProc(t), nil
		},
	})

	projectDir := t.TempDir()
	port, cleanup, err := lc.Spawn("/fake/bin", "test-sess-success", projectDir)
	if err != nil {
		t.Fatalf("Spawn returned unexpected error: %v", err)
	}
	if port != 12345 {
		t.Errorf("port = %d, want 12345", port)
	}
	if cleanup == nil {
		t.Fatal("cleanup should be non-nil on success")
	}

	// cleanup must not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("cleanup panicked: %v", r)
		}
	}()
	cleanup()
}

// TestProcessCollector_Spawn_CleanupIdempotent verifies that calling the
// returned cleanup multiple times is safe — required because os.Exit in
// the launcher bypasses deferred cleanups, so the launcher calls cleanup
// explicitly before os.Exit while still leaving the deferred call in
// place for non-os.Exit paths.
func TestProcessCollector_Spawn_CleanupIdempotent(t *testing.T) {
	var buf bytes.Buffer
	lc := collector.NewProcessCollector(collector.ProcessCollectorOpts{
		Stderr: &buf,
		SpawnFn: func(binPath, sessionID, projectDir string, requestedPort int) (int, *os.Process, error) {
			return 7777, startSleepProc(t), nil
		},
	})

	_, cleanup, err := lc.Spawn("/fake/bin", "test-sess-idempotent", t.TempDir())
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup should be non-nil on success")
	}

	// Call cleanup twice — second call must be a no-op, not panic.
	cleanup()
	cleanup()
}

// TestProcessCollector_Spawn_SingleAttemptNoRetry verifies that Spawn makes a
// single attempt and does NOT retry on a transient failure. maxAttempts was
// reduced from 3 to 1 to bound launch latency (bug-1af3c3d9): a failing/slow
// collector must not add retry-backoff delay to every launch, and a dropped
// collector only costs a few early spans.
func TestProcessCollector_Spawn_SingleAttemptNoRetry(t *testing.T) {
	var buf bytes.Buffer
	callCount := 0

	lc := collector.NewProcessCollector(collector.ProcessCollectorOpts{
		Stderr: &buf,
		SpawnFn: func(binPath, sessionID, projectDir string, requestedPort int) (int, *os.Process, error) {
			callCount++
			return 0, nil, fmt.Errorf("transient error attempt %d", callCount)
		},
	})

	projectDir := t.TempDir()
	port, cleanup, err := lc.Spawn("/fake/bin", "test-sess-single", projectDir)
	if err == nil {
		t.Fatal("expected error: a single transient failure must not be retried")
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (no retry)", callCount)
	}
	if port != 0 {
		t.Errorf("port = %d, want 0 on failure", port)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on failure")
	}
}

// TestProcessCollector_Spawn_FailsAfterMaxRetries verifies that when all
// spawn attempts fail, Spawn returns a non-nil error and stderr has a FATAL line.
func TestProcessCollector_Spawn_FailsAfterMaxRetries(t *testing.T) {
	var buf bytes.Buffer
	callCount := 0

	lc := collector.NewProcessCollector(collector.ProcessCollectorOpts{
		Stderr: &buf,
		SpawnFn: func(binPath, sessionID, projectDir string, requestedPort int) (int, *os.Process, error) {
			callCount++
			return 0, nil, fmt.Errorf("persistent failure attempt %d", callCount)
		},
	})

	projectDir := t.TempDir()
	port, cleanup, err := lc.Spawn("/fake/bin", "test-sess-allfail", projectDir)
	if err == nil {
		t.Fatal("expected error when all spawn attempts fail, got nil")
	}
	if port != 0 {
		t.Errorf("port = %d, want 0 on failure", port)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on failure")
	}

	stderr := buf.String()
	if !strings.Contains(stderr, "FATAL") {
		t.Errorf("expected FATAL line in stderr, got: %q", stderr)
	}
}
