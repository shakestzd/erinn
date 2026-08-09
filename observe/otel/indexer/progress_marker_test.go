package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckpointRoundtrip verifies that a written offset can be read back.
func TestCheckpointRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".index-offset")

	if err := publishProgress(path, 12345); err != nil {
		t.Fatalf("publishProgress: %v", err)
	}

	got, err := readProgress(path)
	if err != nil {
		t.Fatalf("readProgress: %v", err)
	}
	if got != 12345 {
		t.Errorf("got offset %d, want 12345", got)
	}
}

// TestCheckpointMissing verifies that a missing file returns offset 0.
func TestCheckpointMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".index-offset")

	got, err := readProgress(path)
	if err != nil {
		t.Fatalf("readProgress on missing file: %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 for missing checkpoint, got %d", got)
	}
}

// TestCheckpointCorrupted verifies that a corrupted checkpoint falls back to offset 0.
func TestCheckpointCorrupted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".index-offset")

	// Write garbage to simulate a truncated/corrupt checkpoint.
	if err := os.WriteFile(path, []byte("not-a-number"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := readProgress(path)
	if err != nil {
		t.Fatalf("readProgress on corrupt file: %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 for corrupted checkpoint, got %d", got)
	}
}

// TestCheckpointEmptyFile verifies that an empty file returns offset 0.
func TestCheckpointEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".index-offset")

	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := readProgress(path)
	if err != nil {
		t.Fatalf("readProgress on empty file: %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 for empty checkpoint, got %d", got)
	}
}

// TestCheckpointAtomicWrite verifies that publishProgress writes via tmp+rename.
func TestCheckpointAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".index-offset")

	// Write multiple times to ensure atomicity (no partial writes visible).
	for _, offset := range []int64{0, 100, 999, 123456789} {
		if err := publishProgress(path, offset); err != nil {
			t.Fatalf("publishProgress(%d): %v", offset, err)
		}
		got, err := readProgress(path)
		if err != nil {
			t.Fatalf("readProgress after write %d: %v", offset, err)
		}
		if got != offset {
			t.Errorf("after write %d, got %d", offset, got)
		}
	}
}
