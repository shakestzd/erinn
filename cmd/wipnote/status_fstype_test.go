package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/storage"
)

// TestStatusOutput_RendersFstype verifies that runStatus includes
// fstype= and journal_mode= in its output, satisfying the diagnostics
// requirement of Slice 4 (WAL-safe cache path selection).
func TestStatusOutput_RendersFstype(t *testing.T) {
	// Set up a minimal project directory with a .wipnote dir.
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Use WIPNOTE_DB_PATH to make path selection deterministic.
	dbPath := filepath.Join(tmpDir, "test.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	// Inject a deterministic fstype probe.
	origProber := storage.FsTypeProber
	t.Cleanup(func() { storage.FsTypeProber = origProber })
	storage.FsTypeProber = func(_ string) (string, bool) {
		return "ext4", true
	}

	// Point project dir at tmpDir.
	origProjectDir := projectDirFlag
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = origProjectDir }()

	// Capture stdout.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	_ = runStatus(nil, nil)

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 16*1024)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "fstype=") {
		t.Errorf("expected output to contain 'fstype=', got:\n%s", output)
	}
	if !strings.Contains(output, "journal_mode=") {
		t.Errorf("expected output to contain 'journal_mode=', got:\n%s", output)
	}
}
