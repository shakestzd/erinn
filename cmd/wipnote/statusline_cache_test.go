package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunStatuslineCache_NonBlockingStdin is a regression guard for the
// background stdin drain: a harness (agy) that holds the status-line command's
// stdin open without sending EOF must NOT hang `statusline --cache`. The test
// hands runStatuslineCache a pipe whose write end stays open and asserts the
// call returns promptly while still printing the cached work item. A reversion
// to a synchronous io.Copy(os.Stdin) drain would block here and fail.
func TestRunStatuslineCache_NonBlockingStdin(t *testing.T) {
	// Isolated project + cache so we control exactly what the cache returns.
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", proj)
	t.Setenv("WIPNOTE_CACHE_DIR", t.TempDir())

	orig, _ := os.Getwd()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	// Write the cache at exactly the path runStatuslineCache will read.
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		t.Skipf("findWipnoteDir: %v", err)
	}
	const want = "cache demo item"
	if err := os.WriteFile(statuslineCachePath(wipnoteDir), []byte(want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// stdin = pipe whose writer stays open (never sends EOF).
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer wIn.Close() // keep the writer open for the duration of the call
	origIn := os.Stdin
	os.Stdin = rIn
	defer func() { os.Stdin = origIn; rIn.Close() }()

	// Capture stdout.
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut := os.Stdout
	os.Stdout = wOut

	done := make(chan error, 1)
	go func() { done <- runStatuslineCache() }()

	select {
	case e := <-done:
		os.Stdout = origOut
		wOut.Close()
		if e != nil {
			t.Errorf("runStatuslineCache returned error: %v", e)
		}
		out, _ := io.ReadAll(rOut)
		if !strings.Contains(string(out), want) {
			t.Errorf("expected cached line %q in output, got %q", want, string(out))
		}
	case <-time.After(5 * time.Second):
		os.Stdout = origOut
		wOut.Close()
		t.Fatal("runStatuslineCache blocked on an open stdin pipe (did not return) — the background drain regressed")
	}
}
