// Package indexer provides a polling NDJSON-to-SQLite indexer that tails
// per-session events.ndjson files and applies each signal line to SQLite
// via the existing Writer (through the SQLiteSink from S1). Checkpoints
// track byte offsets so only new lines are processed on each poll cycle.
package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readCheckpoint reads the byte offset from path.
// Returns 0 (and no error) when the file is missing, empty, or corrupt.
// Only returns an error for unexpected I/O failures.
func readCheckpoint(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read checkpoint %s: %w", path, err)
	}

	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, nil
	}

	offset, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// Corrupted checkpoint: fall back to 0 for full replay.
		// INSERT OR IGNORE ensures idempotent replay.
		return 0, nil
	}
	return offset, nil
}

// writeCheckpoint writes offset to path atomically via write-to-temp-then-rename.
// The temporary file is placed in the same directory to ensure rename is atomic.
func writeCheckpoint(path string, offset int64) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".index-offset-tmp-")
	if err != nil {
		return fmt.Errorf("create temp checkpoint: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := fmt.Fprintf(tmp, "%d", offset); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp checkpoint: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp checkpoint: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename checkpoint: %w", err)
	}
	return nil
}
