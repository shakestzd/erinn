package retention

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RotateLog enforces a size cap on a single log file. When the file is at or
// over maxBytes it rotates: <path>.<keep-1>...<path>.1 shift down, the current
// content is copied into <path>.1, and the live file is truncated IN PLACE.
//
// SAFETY for actively-writing processes: the live file is never unlinked.
// wipnote's log writers either reopen the file per write with O_APPEND
// (debug.log in internal/hooks/log.go) or hold a long-lived O_APPEND fd for the
// duration of serve (serve-auto.log / serve-<id>.log). os.Truncate on the path
// truncates the same inode the writer's fd points at; with O_APPEND the next
// write seeks to the (now-zero) end of file. No fd is closed and no inode is
// swapped, so an in-flight writer never loses its descriptor or silently writes
// into a deleted file. Rotated copies (.1, .2, …) are regular files we own.
//
// Returns the number of bytes reclaimed from the live file (its size before
// truncation), or 0 when no rotation was needed.
func RotateLog(path string, maxBytes int64, keep int) (int64, error) {
	if maxBytes <= 0 {
		return 0, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat log %s: %w", path, err)
	}
	if info.Size() < maxBytes {
		return 0, nil
	}
	reclaimed := info.Size()

	if keep > 0 {
		// Shift existing rotated copies down: .{keep-1} -> .{keep}, … , .1 -> .2.
		for i := keep - 1; i >= 1; i-- {
			src := fmt.Sprintf("%s.%d", path, i)
			dst := fmt.Sprintf("%s.%d", path, i+1)
			if _, statErr := os.Stat(src); statErr == nil {
				_ = os.Rename(src, dst) // best-effort; a failed shift only loses an old rotation
			}
		}
		// Copy current content into .1 (copy, not rename — the live inode must
		// survive for the writer's open fd).
		if err := copyFile(path, fmt.Sprintf("%s.1", path)); err != nil {
			return 0, fmt.Errorf("rotate copy %s: %w", path, err)
		}
	}

	// Truncate the live file in place. The writer's O_APPEND fd keeps working.
	if err := os.Truncate(path, 0); err != nil {
		return 0, fmt.Errorf("truncate log %s: %w", path, err)
	}
	return reclaimed, nil
}

// copyFile copies src to dst, overwriting dst if it exists.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// rotateProjectLogs applies RotateLog to every known wipnote log file under
// wipnoteDir/logs and wipnoteDir/debug.log. Errors on individual files are
// returned joined-best-effort: the sweep continues past a single failure.
// Returns total bytes reclaimed across all rotated logs.
func rotateProjectLogs(wipnoteDir string, cfg Config) (int64, error) {
	var total int64
	var firstErr error
	note := func(n int64, err error) {
		total += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// debug.log lives directly under .wipnote/.
	note(rotateLogSafe(filepath.Join(wipnoteDir, "debug.log"), cfg))

	// serve-auto.log and serve-<projectID>.log live under .wipnote/logs/.
	logsDir := filepath.Join(wipnoteDir, "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return total, firstErr // logs dir may not exist yet — not an error
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Only the live logs, never the rotated copies (*.log.1, *.log.2).
		if filepath.Ext(name) != ".log" {
			continue
		}
		note(rotateLogSafe(filepath.Join(logsDir, name), cfg))
	}
	return total, firstErr
}

func rotateLogSafe(path string, cfg Config) (int64, error) {
	return RotateLog(path, cfg.LogMaxBytes, cfg.LogKeep)
}
