package commitqueue

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Outbox is the durable append-only NDJSON log of pending commit intents plus
// its sibling dead-letter log. It is keyed by an absolute path; the CLI derives
// that path from internal/storage so it lands in the per-user cache dir. Tests
// point it at a temp dir.
//
// Outbox is process-safe via a dedicated sibling lock file: withLock serializes
// each WHOLE Outbox operation (an Append, or an entire Flush snapshot-process-
// rewrite cycle) against every other operation on the same queue. The lock spans
// the whole flush cycle — not just the individual file writes — so an Append
// landing mid-flush can never be clobbered by the stale-snapshot rewrite (the
// lost-update race flagged by roborev on feat-76504033).
type Outbox struct {
	path     string // the pending-intents NDJSON file
	dlPath   string // the dead-letter NDJSON sibling
	lockPath string // the dedicated cross-operation lock file (path + ".lock")

	// beforeLockForTest, when non-nil, is invoked inside withLock immediately
	// before the blocking flock acquisition. It is a test-only seam (mirroring
	// the gitRunner/gitLockSleep seams in cmd/wipnote) that lets a test prove an
	// operation has reached the lock boundary without a timing sleep. Production
	// never sets it.
	beforeLockForTest func()
}

// NewOutbox constructs an Outbox at path. The dead-letter sibling is path with
// a ".deadletter" suffix inserted before the extension. The parent directory is
// created lazily on first Append.
func NewOutbox(path string) *Outbox {
	return &Outbox{path: path, dlPath: deadLetterPath(path), lockPath: path + ".lock"}
}

// withLock serializes a whole Outbox operation against every other operation on
// the same queue, using a dedicated sibling lock file. This is REQUIRED for
// correctness: Flush snapshots the pending file, commits each intent, then
// rewrites the file with what remains. Without a lock spanning that whole cycle,
// an Append landing between the snapshot and the rewrite would be silently
// dropped by the stale-snapshot rewrite. The lock file is SEPARATE from the data
// file because Flush replaces the data file via rename — a lock on the data-file
// inode would not survive the swap, whereas the stable lock-file inode does.
func (o *Outbox) withLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(o.lockPath), 0o755); err != nil {
		return fmt.Errorf("commitqueue: mkdir lock dir: %w", err)
	}
	f, err := os.OpenFile(o.lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("commitqueue: open lock %s: %w", o.lockPath, err)
	}
	defer f.Close()
	if o.beforeLockForTest != nil {
		o.beforeLockForTest()
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("commitqueue: flock %s: %w", o.lockPath, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
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
	// Hold the cross-operation lock so an Append concurrent with a Flush is
	// ordered relative to that flush's snapshot/rewrite rather than racing it.
	return o.withLock(func() error {
		if err := os.MkdirAll(filepath.Dir(o.path), 0o755); err != nil {
			return fmt.Errorf("commitqueue: mkdir outbox dir: %w", err)
		}
		return appendLineLocked(o.path, line)
	})
}

// AppendCoalescingByRelPath records an intent after dropping older pending
// intents for the same repo-relative artifact path. Deferred work-item
// transitions rewrite one canonical HTML file in place, so flushing an older
// "create" intent after a later "complete" write would commit the latest file
// contents with a stale transition message. Coalescing keeps the queue honest:
// the newest transition is the one that will be committed.
func (o *Outbox) AppendCoalescingByRelPath(i Intent) error {
	if err := i.Validate(); err != nil {
		return err
	}
	return o.withLock(func() error {
		pending, err := readIntents(o.path)
		if err != nil {
			return err
		}
		keys := intentRelPathKeys(i)
		kept := pending[:0]
		for _, existing := range pending {
			if !intentSharesRelPath(existing, keys) {
				kept = append(kept, existing)
			}
		}
		kept = append(kept, i)
		return o.rewrite(kept)
	})
}

func intentRelPathKeys(i Intent) map[string]struct{} {
	keys := make(map[string]struct{}, len(i.RelPaths))
	for _, rel := range i.RelPaths {
		keys[normaliseIntentRelPath(rel)] = struct{}{}
	}
	return keys
}

func intentSharesRelPath(i Intent, keys map[string]struct{}) bool {
	for _, rel := range i.RelPaths {
		if _, ok := keys[normaliseIntentRelPath(rel)]; ok {
			return true
		}
	}
	return false
}

func normaliseIntentRelPath(rel string) string {
	return strings.ReplaceAll(filepath.ToSlash(filepath.Clean(rel)), "\\", "/")
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
	// O_RDWR (not O_APPEND) so we can inspect the trailing byte before writing.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("commitqueue: open %s: %w", path, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("commitqueue: flock %s: %w", path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	out := make([]byte, 0, len(line)+2)
	// If a prior append was torn by a crash (file doesn't end in '\n'), terminate
	// that partial line FIRST so the new intent lands on its own clean line — else
	// it merges into the corrupt tail and readIntents silently drops both
	// (roborev #3723).
	if fi, serr := f.Stat(); serr == nil && fi.Size() > 0 {
		tail := make([]byte, 1)
		if _, rerr := f.ReadAt(tail, fi.Size()-1); rerr == nil && tail[0] != '\n' {
			out = append(out, '\n')
		}
	}
	out = append(out, line...)
	out = append(out, '\n')

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("commitqueue: seek %s: %w", path, err)
	}
	if _, err := f.Write(out); err != nil {
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
