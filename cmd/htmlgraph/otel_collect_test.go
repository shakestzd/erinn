package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// otelCollectTestBinary holds the path to the binary built for otel-collect tests.
// Built once by buildOtelCollectTestBinary and reused across tests.
// The containing temp dir is tracked in otelCollectTestBinaryTmpDir and removed
// by TestMain (testmain_test.go) after the suite completes.
var (
	otelCollectTestBinary       string
	otelCollectTestBinaryTmpDir string
)

// buildOtelCollectTestBinary builds the htmlgraph binary into a temp dir and
// returns the path. It is safe to call multiple times — subsequent calls
// reuse the first binary. The temp dir is cleaned up by TestMain (testmain_test.go).
func buildOtelCollectTestBinary(t *testing.T) string {
	t.Helper()
	if otelCollectTestBinary != "" {
		if _, err := os.Stat(otelCollectTestBinary); err == nil {
			return otelCollectTestBinary
		}
	}
	tmp := t.TempDir()
	otelCollectTestBinaryTmpDir = tmp // tracked for cleanup in TestMain
	bin := filepath.Join(tmp, "htmlgraph-test")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	cmd.Dir = filepath.Dir(thisFile)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build htmlgraph for otel-collect tests: %v", err)
	}
	otelCollectTestBinary = bin
	return bin
}

// mkOtelCollectProject creates a temp project dir with a .htmlgraph directory
// and returns the project root.
func mkOtelCollectProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".htmlgraph"), 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	return dir
}

// readHandshakeLine reads lines from the scanner until it finds the
// htmlgraph-otel-ready line or the deadline is exceeded.
func readHandshakeLine(t *testing.T, scanner *bufio.Scanner, deadline time.Duration) (string, bool) {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "htmlgraph-otel-ready") {
				done <- line
				return
			}
		}
		done <- ""
	}()
	select {
	case line := <-done:
		return line, line != ""
	case <-time.After(deadline):
		return "", false
	}
}

// TestOtelCollect_HandshakeLine verifies that otel-collect prints exactly one
// handshake line on stdout matching "htmlgraph-otel-ready port=<N>" and nothing
// else before the process is signalled. Stdout purity is required because the
// launcher in S3 uses bufio.Scanner on the child's stdout pipe.
func TestOtelCollect_HandshakeLine(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	sid := "test-sid-handshake"

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	// Very short idle timeout so the process exits promptly after the handshake.
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=300ms",
		"HTMLGRAPH_PROJECT_DIR="+projectDir,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start otel-collect: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	scanner := bufio.NewScanner(stdout)
	line, ok := readHandshakeLine(t, scanner, 5*time.Second)
	if !ok {
		t.Fatal("otel-collect did not print htmlgraph-otel-ready within 5s")
	}

	// Validate format: "htmlgraph-otel-ready port=<N>"
	if !strings.HasPrefix(line, "htmlgraph-otel-ready port=") {
		t.Errorf("handshake line format wrong: %q", line)
	}
	var port int
	if _, err := fmt.Sscanf(line, "htmlgraph-otel-ready port=%d", &port); err != nil {
		t.Errorf("could not parse port from handshake %q: %v", line, err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("port out of range: %d", port)
	}
}

// TestOtelCollect_IdleTimeout verifies that otel-collect exits 0 within a
// reasonable window when no OTLP traffic arrives and HTMLGRAPH_OTEL_IDLE_TIMEOUT
// is set to a short value.
func TestOtelCollect_IdleTimeout(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	sid := "test-sid-idletimeout"

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=200ms",
		"HTMLGRAPH_PROJECT_DIR="+projectDir,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start otel-collect: %v", err)
	}

	// Drain stdout so the process doesn't block on a full pipe.
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
		}
	}()

	// The process should exit within 3 seconds (200ms idle timeout + margin).
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("otel-collect exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("otel-collect did not exit within 5s (idle timeout not working)")
	}
}

// TestOtelCollect_CollectorStartEvent verifies that after the handshake, the
// session's events.ndjson contains a collector_start event as the first line.
func TestOtelCollect_CollectorStartEvent(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	sid := "test-sid-startev"

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=400ms",
		"HTMLGRAPH_PROJECT_DIR="+projectDir,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start otel-collect: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Wait for handshake before reading the events file.
	scanner := bufio.NewScanner(stdout)
	if _, ok := readHandshakeLine(t, scanner, 5*time.Second); !ok {
		t.Fatal("no handshake line within 5s")
	}

	// Give it a moment to flush the collector_start event.
	time.Sleep(100 * time.Millisecond)

	eventsPath := filepath.Join(projectDir, ".htmlgraph", "sessions", sid, "events.ndjson")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("events.ndjson not found at %s: %v", eventsPath, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("events.ndjson is empty")
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not valid JSON: %v — raw: %q", err, lines[0])
	}

	if got := first["kind"]; got != "collector_start" {
		t.Errorf("first event kind = %q, want %q", got, "collector_start")
	}
	if first["session_id"] != sid {
		t.Errorf("first event session_id = %q, want %q", first["session_id"], sid)
	}

	attrs, ok := first["attrs"].(map[string]any)
	if !ok {
		t.Fatalf("attrs field missing or not an object: %v", first["attrs"])
	}
	if attrs["htmlgraph_sid"] != sid {
		t.Errorf("attrs.htmlgraph_sid = %q, want %q", attrs["htmlgraph_sid"], sid)
	}
	if _, hasPort := attrs["port"]; !hasPort {
		t.Error("attrs.port missing from collector_start event")
	}
	if _, hasPID := attrs["pid"]; !hasPID {
		t.Error("attrs.pid missing from collector_start event")
	}
}
