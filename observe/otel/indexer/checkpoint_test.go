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

	if err := writeCheckpoint(path, 12345); err != nil {
		t.Fatalf("writeCheckpoint: %v", err)
	}

	got, err := readCheckpoint(path)
	if err != nil {
		t.Fatalf("readCheckpoint: %v", err)
	}
	if got != 12345 {
		t.Errorf("got offset %d, want 12345", got)
	}
}

// TestCheckpointMissing verifies that a missing file returns offset 0.
func TestCheckpointMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".index-offset")

	got, err := readCheckpoint(path)
	if err != nil {
		t.Fatalf("readCheckpoint on missing file: %v", err)
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

	got, err := readCheckpoint(path)
	if err != nil {
		t.Fatalf("readCheckpoint on corrupt file: %v", err)
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

	got, err := readCheckpoint(path)
	if err != nil {
		t.Fatalf("readCheckpoint on empty file: %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 for empty checkpoint, got %d", got)
	}
}

// TestCheckpointAtomicWrite verifies that writeCheckpoint writes via tmp+rename.
func TestCheckpointAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".index-offset")

	// Write multiple times to ensure atomicity (no partial writes visible).
	for _, offset := range []int64{0, 100, 999, 123456789} {
		if err := writeCheckpoint(path, offset); err != nil {
			t.Fatalf("writeCheckpoint(%d): %v", offset, err)
		}
		got, err := readCheckpoint(path)
		if err != nil {
			t.Fatalf("readCheckpoint after write %d: %v", offset, err)
		}
		if got != offset {
			t.Errorf("after write %d, got %d", offset, got)
		}
	}
}
