package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatusOutput_RendersCanonicalStorage verifies status reports the
// canonical storage boundary without opening or creating the persistent project
// DB cache.
func TestStatusOutput_RendersCanonicalStorage(t *testing.T) {
	// Set up a minimal project directory with a .wipnote dir.
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Use WIPNOTE_DB_PATH to make path selection deterministic.
	dbPath := filepath.Join(tmpDir, "test.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

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

	if !strings.Contains(output, "Storage: canonical HTML/ledgers") {
		t.Errorf("expected canonical storage line, got:\n%s", output)
	}
	if strings.Contains(output, "journal_mode=") || strings.Contains(output, "fstype=") {
		t.Errorf("status should not print project DB diagnostics, got:\n%s", output)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("status created/opened project DB %s: %v", dbPath, err)
	}
}
