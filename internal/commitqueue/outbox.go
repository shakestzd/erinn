package commitqueue

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Outbox is the durable append-only NDJSON log of pending commit intents plus
// its sibling dead-letter log. It is keyed by an absolute path; the CLI derives
// that path from internal/storage so it lands in the per-user cache dir. Tests
// point it at a temp dir.
//
// Outbox is process-safe via syscall.Flock(LOCK_EX) around each whole-file
// operation (mirroring internal/otel/sink/ndjson), so a concurrent recorder and
// a flush never interleave a partial line or race the rewrite.
type Outbox struct {
	path   string // the pending-intents NDJSON file
	dlPath string // the dead-letter NDJSON sibling
}

// NewOutbox constructs an Outbox at path. The dead-letter sibling is path with
// a ".deadletter" suffix inserted before the extension. The parent directory is
// created lazily on first Append.
func NewOutbox(path string) *Outbox {
	return &Outbox{path: path, dlPath: deadLetterPath(path)}
}

// Path returns the pending-intents file path.
func (o *Outbox) Path() string { return o.path }

// DeadLetterPath returns the dead-letter file path.
func (o *Outbox) DeadLetterPath() string { return o.dlPath }

func deadLetterPath(path string) string {
	ext := filepath.Ext(path)
	base := path[:len(path)-len(ext)]
	return base + ".deadletter" + ext
}

// Append records one commit intent by appending a single NDJSON line under an
// exclusive file lock + fsync. The intent is validated first so a structurally
// useless record never reaches the queue. Append is the ONLY mutation a
// producer performs — it never touches git.
func (o *Outbox) Append(i Intent) error {
	if err := i.Validate(); err != nil {
		return err
	}
	line, err := marshalLine(i)
	if err != nil {
		return fmt.Errorf("commitqueue: marshal intent: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(o.path), 0o755); err != nil {
		return fmt.Errorf("commitqueue: mkdir outbox dir: %w", err)
	}
	return appendLineLocked(o.path, line)
}

// Pending reads and returns all intents currently queued, in FIFO order. A
// missing file is an empty queue (not an error). Blank lines are skipped; a
// trailing partial line that fails to parse is dropped (crash-mid-write
// tolerance) rather than aborting the whole drain.
func (o *Outbox) Pending() ([]Intent, error) {
	return readIntents(o.path)
}

// DeadLettered reads and returns all dead-lettered intents. A missing file is
// an empty slice.
func (o *Outbox) DeadLettered() ([]Intent, error) {
	return readIntents(o.dlPath)
}

// Depth returns the number of pending intents. Surfaced by `wipnote status` and
// the flush command output.
func (o *Outbox) Depth() (int, error) {
	pending, err := o.Pending()
	if err != nil {
		return 0, err
	}
	return len(pending), nil
}

// DeadLetterDepth returns the number of dead-lettered intents.
func (o *Outbox) DeadLetterDepth() (int, error) {
	dl, err := o.DeadLettered()
	if err != nil {
		return 0, err
	}
	return len(dl), nil
}

// rewrite atomically replaces the pending file with the given intents (used to
// remove drained/dead-lettered intents). An empty slice truncates the file.
// Write to a temp sibling then rename so a crash never leaves a half-written
// queue.
func (o *Outbox) rewrite(intents []Intent) error {
	return atomicWriteIntents(o.path, intents)
}

// appendDeadLetter appends an intent to the dead-letter log under lock+fsync.
func (o *Outbox) appendDeadLetter(i Intent) error {
	line, err := marshalLine(i)
	if err != nil {
		return fmt.Errorf("commitqueue: marshal dead-letter intent: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(o.dlPath), 0o755); err != nil {
		return fmt.Errorf("commitqueue: mkdir dead-letter dir: %w", err)
	}
	return appendLineLocked(o.dlPath, line)
}

// appendLineLocked opens path in append mode, takes an exclusive flock, writes
// line+newline, fsyncs, and releases. Mirrors internal/otel/sink/ndjson.
func appendLineLocked(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("commitqueue: open %s: %w", path, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("commitqueue: flock %s: %w", path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("commitqueue: write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("commitqueue: fsync %s: %w", path, err)
	}
	return nil
}

// readIntents parses every NDJSON line in path. A missing file returns an empty
// slice with no error. Malformed lines are skipped (a partial trailing line
// from a crash mid-append is tolerated) so a single bad line never wedges the
// drain.
func readIntents(path string) ([]Intent, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("commitqueue: open %s: %w", path, err)
	}
	defer f.Close()

	var intents []Intent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		intent, ok, perr := parseLine(scanner.Bytes())
		if perr != nil || !ok {
			continue // skip blank/partial/corrupt lines (crash tolerance)
		}
		intents = append(intents, intent)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("commitqueue: scan %s: %w", path, err)
	}
	return intents, nil
}

// atomicWriteIntents writes intents to a temp file in the same directory then
// renames over path. An empty slice produces an empty (truncated) file. The
// rename is atomic on POSIX so a reader never sees a partial rewrite.
func atomicWriteIntents(path string, intents []Intent) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("commitqueue: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".outbox-*.tmp")
	if err != nil {
		return fmt.Errorf("commitqueue: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	w := bufio.NewWriter(tmp)
	for _, i := range intents {
		line, mErr := marshalLine(i)
		if mErr != nil {
			tmp.Close()
			return fmt.Errorf("commitqueue: marshal during rewrite: %w", mErr)
		}
		if _, wErr := w.Write(append(line, '\n')); wErr != nil {
			tmp.Close()
			return fmt.Errorf("commitqueue: write temp: %w", wErr)
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return fmt.Errorf("commitqueue: flush temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("commitqueue: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("commitqueue: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("commitqueue: rename temp over %s: %w", path, err)
	}
	return nil
}
