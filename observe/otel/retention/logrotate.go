package retention

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RotateLog enforces a size cap on a single log file. When the file is at or
// over maxBytes it rotates: <path>.<keep-1>...<path>.1 shift down, the current
// tail (up to maxBytes) is copied into <path>.1, and the live file is truncated
// IN PLACE.
//
// SAFETY for actively-writing processes: the live file is never unlinked.
// wipnote's log writers either reopen the file per write with O_APPEND
// (debug.log in internal/hooks/log.go) or hold a long-lived O_APPEND fd for the
// duration of serve (serve-auto.log / serve-<id>.log). os.Truncate on the path
// truncates the same inode the writer's fd points at; with O_APPEND the next
// write seeks to the (now-zero) end of file. No fd is closed and no inode is
// swapped, so an in-flight writer never loses its descriptor or silently writes
// into a deleted file. Rotated copies (.1, .2, ...) are regular files we own.
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
		// Shift existing rotated copies down: .{keep-1} -> .{keep}, ..., .1 -> .2.
		for i := keep - 1; i >= 1; i-- {
			src := fmt.Sprintf("%s.%d", path, i)
			dst := fmt.Sprintf("%s.%d", path, i+1)
			if _, statErr := os.Stat(src); statErr == nil {
				_ = os.Rename(src, dst) // best-effort; a failed shift only loses an old rotation
			}
		}
		// Copy only the tail into .1 (copy, not rename: the live inode must
		// survive for the writer's open fd). Copying the entire old file would
		// preserve a runaway multi-GB log under the rotated name and reclaim
		// almost no disk.
		if err := copyFileTail(path, fmt.Sprintf("%s.1", path), maxBytes); err != nil {
			return 0, fmt.Errorf("rotate copy %s: %w", path, err)
		}
	}

	// Truncate the live file in place. The writer's O_APPEND fd keeps working.
	if err := os.Truncate(path, 0); err != nil {
		return 0, fmt.Errorf("truncate log %s: %w", path, err)
	}
	return reclaimed, nil
}

// copyFileTail copies the last maxBytes of src to dst, overwriting dst if it
// exists. If src is smaller than maxBytes, it copies the whole file.
func copyFileTail(src, dst string, maxBytes int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		if _, err := in.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
			return err
		}
	}
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

// userCacheDir resolves the platform user cache directory. A package-level
// var (rather than a direct os.UserCacheDir() call) so tests can override it
// cross-platform: os.UserCacheDir() honors $XDG_CACHE_HOME only on Linux —
// on Darwin it always returns $HOME/Library/Caches regardless of the env
// var — so a test that wants to redirect the cache root must swap this func,
// not set XDG_CACHE_HOME (bug-6882ecaa).
var userCacheDir = os.UserCacheDir

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
	if cacheDir, err := userCacheDir(); err == nil {
		// Older/dev launcher paths wrote a process-level serve.log directly
		// under the XDG cache root. It is outside .wipnote/logs, so include it
		// here until all long-lived environments have aged past that behavior.
		note(rotateLogSafe(filepath.Join(cacheDir, "wipnote", "serve.log"), cfg))
	}

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

// OpenBoundedLog rotates path if it already exceeds maxBytes (keeping keep
// rotated copies), then opens the live file for append. It is intended for
// long-lived log writers (serve-auto.log, writer.log, serve-<id>.log) that
// are opened once per process and therefore cannot rely solely on the 24-hour
// retention sweep to enforce size bounds.
//
// If maxBytes <= 0 the rotation step is skipped and the file is opened as-is.
// The caller owns the returned *os.File and must close it.
// Returns (nil, nil) when the path cannot be opened — matches the existing
// best-effort pattern in cmd/wipnote callers.
func OpenBoundedLog(path string, maxBytes int64, keep int) (*os.File, error) {
	if maxBytes > 0 {
		if _, err := RotateLog(path, maxBytes, keep); err != nil {
			// Non-fatal: log the rotation failure but proceed with the open so
			// the serving process is not blocked by a transient FS error.
			fmt.Fprintf(os.Stderr, "wipnote: log rotation before open %s: %v\n", path, err)
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
