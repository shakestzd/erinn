package indexer

import (
	"bufio"
	"context"
	"database/sql"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/sink"
)

// syncSink is the optional interface implemented by sinks that expose a
// synchronous write path (currently sqlite.QueuedSink). When the
// configured sink satisfies it, the indexer routes batches through the
// sync path so it can wait for the SQLite commit before advancing
// `.index-offset`. Without the sync path, the underlying queue is
// fire-and-forget and a queue rejection / late op error would silently
// strand records as "indexed" in the checkpoint while the SQLite insert
// never happened (roborev #1501).
type syncSink interface {
	WriteBatchSync(ctx context.Context, harness otel.Harness, resourceAttrs map[string]any, signals []otel.UnifiedSignal) error
}

const pollInterval = 500 * time.Millisecond

// maxBytesPerTick caps the amount of NDJSON data processed per session per tick.
// Prevents a single huge session from monopolizing the SQLite writer lock and
// starving other sessions, which manifested as bug-faf8e395 (indexer retry loop
// on a 366MB events.ndjson).
const maxBytesPerTick = 4 * 1024 * 1024 // 4 MiB

// FileInfo holds per-file health metrics for the /api/indexer/status endpoint.
type FileInfo struct {
	LastOffset    int64     `json:"last_offset"`
	CurrentSize   int64     `json:"current_size"`
	LagBytes      int64     `json:"lag_bytes"`
	LastError     string    `json:"last_error"`
	LastIndexedAt time.Time `json:"last_indexed_at"`
}

// Indexer polls .wipnote/sessions/*/events.ndjson files for new appends,
// parses each line into a UnifiedSignal, and applies them to SQLite via snk.
type Indexer struct {
	wipnoteDir string
	snk        sink.SignalSink
	database   *sql.DB           // optional; read-only handle for orphan-filter SELECTs
	writeDB    *sql.DB           // optional; writable handle used for prompt-ID bridge when no queue
	queue      *writequeue.Queue // optional; when set, prompt-ID bridge goes through the queue

	// offsets is the per-session read position, held IN MEMORY for the life of
	// this Indexer and deliberately not seeded from disk.
	//
	// The destination is now a process-local projection that starts empty
	// (feat-fc3cc9e0). Resuming from a checkpoint left by an earlier process
	// would skip every line before it while the rows those lines produced no
	// longer exist anywhere — the projection would be permanently and silently
	// holed, which is the same failure shape as bug-0fc17d53 but with no schema
	// defect to find. Starting at zero each time makes a full replay the
	// guaranteed behaviour of every process; replay is cheap to get right
	// because the writer keys inserts on signal_id, so re-read lines collapse
	// onto rows already present.
	//
	// This also retired EnsureReingest and its migration-armed marker: forcing
	// a full re-ingest was only necessary while checkpoints survived restarts.
	offsets map[string]int64

	mu     sync.RWMutex
	status map[string]FileInfo
}

// New constructs an Indexer rooted at wipnoteDir.
// wipnoteDir is the .wipnote/ directory (e.g. /path/to/project/.wipnote).
func New(wipnoteDir string, snk sink.SignalSink) *Indexer {
	return &Indexer{
		wipnoteDir: wipnoteDir,
		snk:        snk,
		offsets:    make(map[string]int64),
		status:     make(map[string]FileInfo),
	}
}

// WithDB attaches a read-only *sql.DB to the indexer for the orphan-filter
// SELECT queries in filterSessionsByDB / queryKnownSessionIDs.  Using a
// read-only handle here (bug-272c5e34 Change 1) prevents those SELECTs from
// holding a SHARED lock on the writable handle, which was blocking the queue
// worker's BEGIN IMMEDIATE and causing a symmetric DELETE-journal deadlock.
//
// When a write queue is also attached (WithQueue), the prompt-ID bridge uses
// the queue instead of this handle for writes.  When no queue is attached,
// the bridge falls back to WriteDB (set via WithWriteDB) if provided.
func (idx *Indexer) WithDB(database *sql.DB) *Indexer {
	idx.database = database
	return idx
}

// WithWriteDB attaches a writable *sql.DB for the prompt-ID bridge fallback
// path used when no write queue has been wired via WithQueue.  In production
// serve_child.go always calls WithQueue, so this handle is the last-resort
// path for callers (e.g. wipnote reindex) that drive the indexer without a
// queue.
func (idx *Indexer) WithWriteDB(writeDB *sql.DB) *Indexer {
	idx.writeDB = writeDB
	return idx
}

// WithQueue wires the single write-queue so the prompt-ID bridge
// (maybeSetPromptID) submits its SELECT+UPDATE as a fire-and-forget WriteOp
// through the queue instead of issuing an independent write directly.  This
// eliminates the second independent writer that was causing the symmetric
// DELETE-journal deadlock (bug-272c5e34 Change 2).
func (idx *Indexer) WithQueue(q *writequeue.Queue) *Indexer {
	idx.queue = q
	return idx
}

// Start runs the poll loop until ctx is cancelled. Intended to be called as a goroutine.
func (idx *Indexer) Start(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idx.runOnce(ctx)
		}
	}
}

// Status returns a snapshot of per-session file health.
func (idx *Indexer) Status() map[string]FileInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make(map[string]FileInfo, len(idx.status))
	for k, v := range idx.status {
		out[k] = v
	}
	return out
}

// RunOnce performs one indexer pass synchronously. It exists so callers
// outside the daemon (notably `wipnote reindex` — slice 9, feat-229f3333)
// can drive the same NDJSON-to-SQLite pipeline used by Start, but in
// foreground mode for full-rebuild scenarios. Idempotent: replaying the
// same offsets is safe because the receiver Writer uses INSERT OR IGNORE.
func (idx *Indexer) RunOnce(ctx context.Context) {
	idx.runOnce(ctx)
}

// runOnce discovers all sessions and processes any new data.
func (idx *Indexer) runOnce(ctx context.Context) {
	sessions, err := idx.discoverSessions()
	if err != nil {
		log.Printf("indexer: discover sessions: %v", err)
		return
	}
	for _, sid := range sessions {
		if ctx.Err() != nil {
			return
		}
		if err := idx.processSession(ctx, sid); err != nil {
			log.Printf("indexer: session %s: %v", sid, err)
			idx.recordError(sid, err)
		}
	}
}

// discoverSessions returns session IDs that have an events.ndjson file.
// When the indexer has a database attached (idx.database != nil), it also
// filters out session directories that have no corresponding row in the
// sessions table (orphans). Orphan directories are logged at debug level and
// skipped so the indexer never wastes writer cycles on data that cannot be
// attributed to a known session.
func (idx *Indexer) discoverSessions() ([]string, error) {
	sessionsDir := filepath.Join(idx.wipnoteDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var sessions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ndjson := filepath.Join(sessionsDir, e.Name(), "events.ndjson")
		if _, err := os.Stat(ndjson); err == nil {
			sessions = append(sessions, e.Name())
		}
	}

	// Gate on DB membership: skip orphan directories (no sessions row).
	sessions = filterSessionsByDB(idx.database, idx.wipnoteDir, sessions)
	return sessions, nil
}

// processSession tails events.ndjson for sessionID from this Indexer's in-memory
// read position, parses each line, and applies the batch to snk. On success it
// advances the in-memory position and republishes the on-disk progress marker.
func (idx *Indexer) processSession(ctx context.Context, sessionID string) error {
	sessDir := filepath.Join(idx.wipnoteDir, "sessions", sessionID)
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	checkpointPath := filepath.Join(sessDir, ".index-offset")

	offset := idx.resumeOffset(sessionID)

	info, err := os.Stat(ndjsonPath)
	if err != nil {
		return err
	}
	currentSize := info.Size()
	idx.updateSize(sessionID, offset, currentSize)

	if currentSize <= offset {
		return nil // no new data
	}

	// Cap the read window to maxBytesPerTick so a single huge file cannot
	// monopolize the SQLite writer lock across ticks (bug-faf8e395).
	readUpTo := currentSize
	if readUpTo-offset > maxBytesPerTick {
		readUpTo = offset + maxBytesPerTick
	}

	parsed, newOffset, err := idx.readNewSignals(ndjsonPath, offset, readUpTo)
	if err != nil {
		return err
	}
	if len(parsed) == 0 {
		idx.setResumeOffset(sessionID, newOffset)
		return publishProgress(checkpointPath, newOffset)
	}

	if err := idx.writeParsedBatch(ctx, parsed); err != nil {
		return err
	}

	// Advance the in-memory position ONLY after the batch is committed. The
	// sync-sink path exists so a queue rejection cannot leave records counted
	// as read while the insert never happened (roborev #1501); that guarantee
	// now protects the in-memory position rather than the file.
	idx.setResumeOffset(sessionID, newOffset)

	if err := publishProgress(checkpointPath, newOffset); err != nil {
		return err
	}

	idx.recordSuccess(sessionID, newOffset, currentSize)
	return nil
}

// resumeOffset returns where this Indexer left off for sessionID within THIS
// process. A session it has not read yet resumes at zero — see the offsets
// field for why that is the required behaviour, not a simplification.
func (idx *Indexer) resumeOffset(sessionID string) int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.offsets[sessionID]
}

// setResumeOffset records how far this Indexer has committed for sessionID.
func (idx *Indexer) setResumeOffset(sessionID string, offset int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.offsets[sessionID] = offset
}

// readNewSignals opens ndjsonPath, seeks to offset, reads complete
// newline-terminated lines up to readUpTo bytes, and parses them.
// Incomplete trailing data (no newline at EOF) is left uncheckpointed
// so the next poll retries once the writer finishes the line.
// readUpTo limits how many bytes are consumed per tick (bug-faf8e395).
func (idx *Indexer) readNewSignals(ndjsonPath string, offset, readUpTo int64) ([]parsedSignal, int64, error) {
	f, err := os.Open(ndjsonPath)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, offset, err
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	var result []parsedSignal
	committedOffset := offset

	for {
		// Stop once we've reached or passed the byte budget ceiling.
		// Because readLine consumes exactly one complete newline-terminated
		// record, the cutoff is always on a newline boundary.
		if committedOffset >= readUpTo {
			break
		}
		line, err := readLine(reader)
		if err != nil {
			break
		}
		lineLen := int64(len(line)) + 1
		if len(line) == 0 {
			committedOffset += lineLen
			continue
		}
		p, parseErr := parseLine(line)
		if parseErr != nil {
			log.Printf("indexer: skip malformed line at offset ~%d: %v",
				committedOffset, parseErr)
			committedOffset += lineLen
			continue
		}
		if p == nil {
			committedOffset += lineLen
			continue
		}
		result = append(result, *p)
		committedOffset += lineLen
	}
	return result, committedOffset, nil
}

// writeParsedBatch writes parsed signals to the sink, passing through
// each signal's resource attributes so placeholder/re-attribution logic
// in the SQLite writer functions correctly. After persisting each signal it
// attempts to bridge prompt_id from user_prompt log records back to the
// matching UserQuery row in agent_events (best-effort, silently skipped on failure).
//
// When the sink supports a synchronous path (sqlite.QueuedSink does), we
// use it so the caller can refuse to advance the `.index-offset`
// checkpoint until the SQLite commit succeeds. This closes roborev
// #1501: WriteBatch on the QueuedSink is fire-and-forget, so without
// the sync variant a queue rejection (full / unavailable / timeout) or
// a late op error would let processSession checkpoint records as
// "indexed" while the DB write never happened.
func (idx *Indexer) writeParsedBatch(ctx context.Context, parsed []parsedSignal) error {
	sync, useSync := idx.snk.(syncSink)
	for _, p := range parsed {
		h := p.Signal.Harness
		if h == "" {
			h = otel.HarnessClaude
		}
		signals := []otel.UnifiedSignal{p.Signal}
		var err error
		if useSync {
			err = sync.WriteBatchSync(ctx, h, p.ResourceAttrs, signals)
		} else {
			err = idx.snk.WriteBatch(ctx, h, p.ResourceAttrs, signals)
		}
		if err != nil {
			return err
		}
		idx.maybeSetPromptID(p.Signal)
	}
	return nil
}

// maybeSetPromptID correlates a user_prompt OTel signal back to the closest
// UserQuery event in agent_events by session_id + timestamp. It is a no-op
// when the indexer has no writable path attached, the signal is not a
// user_prompt, or the signal carries no prompt_id.
//
// Write routing (bug-272c5e34 Change 2):
//
//	When a write queue is attached (WithQueue), the SELECT+UPDATE is
//	submitted as a fire-and-forget WriteOp through the queue.  This is
//	acceptable because the prompt-ID bridge is explicitly best-effort —
//	a missed correlation is a cosmetic gap, never a data-loss event.  On
//	submit error (queue full / stopped) we log and continue, matching the
//	prior best-effort behaviour.
//
//	When no queue is attached (e.g. wipnote reindex) the bridge falls back
//	to WriteDB if set, or is silently skipped.
func (idx *Indexer) maybeSetPromptID(sig otel.UnifiedSignal) {
	if sig.Kind != otel.KindLog {
		return
	}
	if sig.CanonicalName != otel.CanonicalUserPrompt {
		return
	}
	if sig.PromptID == "" || sig.SessionID == "" {
		return
	}

	// Prefer queue path: fire-and-forget through the single writer.
	if idx.queue != nil && idx.writeDB != nil {
		sessionID := sig.SessionID
		promptID := sig.PromptID
		ts := sig.Timestamp
		wdb := idx.writeDB
		op := writequeue.WriteOp(func(_ context.Context) error {
			return db.SetPromptID(wdb, sessionID, promptID, ts)
		})
		if err := idx.queue.Submit(context.Background(), op); err != nil {
			log.Printf("indexer: set prompt_id submit (session=%s, prompt=%s): %v",
				sig.SessionID, sig.PromptID, err)
		}
		return
	}

	// Fallback: direct write on writeDB (no queue — e.g. wipnote reindex).
	if idx.writeDB != nil {
		if err := db.SetPromptID(idx.writeDB, sig.SessionID, sig.PromptID, sig.Timestamp); err != nil {
			log.Printf("indexer: set prompt_id (session=%s, prompt=%s): %v",
				sig.SessionID, sig.PromptID, err)
		}
		return
	}

	// Legacy path: if only the old read/write database field is set (callers
	// that used WithDB before bug-272c5e34 split the handles), use it.
	if idx.database != nil {
		if err := db.SetPromptID(idx.database, sig.SessionID, sig.PromptID, sig.Timestamp); err != nil {
			log.Printf("indexer: set prompt_id (session=%s, prompt=%s): %v",
				sig.SessionID, sig.PromptID, err)
		}
	}
}

const maxLineSize = 4 * 1024 * 1024

// readLine reads until the next newline, returning the line content
// without the newline. Returns io.EOF when no more complete lines
// exist. Lines exceeding maxLineSize are skipped with a log warning.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if !isPrefix {
			return buf, nil
		}
		if len(buf) > maxLineSize {
			skipToNewline(r)
			log.Printf("indexer: line exceeds %d bytes — skipped", maxLineSize)
			return buf[:0], nil // return empty so caller advances offset
		}
	}
}

func skipToNewline(r *bufio.Reader) {
	for {
		_, isPrefix, err := r.ReadLine()
		if err != nil || !isPrefix {
			return
		}
	}
}

// updateSize records the current file size without touching LastIndexedAt.
func (idx *Indexer) updateSize(sessionID string, offset, currentSize int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	fi := idx.status[sessionID]
	fi.LastOffset = offset
	fi.CurrentSize = currentSize
	fi.LagBytes = currentSize - offset
	idx.status[sessionID] = fi
}

// recordSuccess updates the status snapshot after a successful batch.
func (idx *Indexer) recordSuccess(sessionID string, newOffset, currentSize int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.status[sessionID] = FileInfo{
		LastOffset:    newOffset,
		CurrentSize:   currentSize,
		LagBytes:      currentSize - newOffset,
		LastIndexedAt: time.Now().UTC(),
	}
}

// recordError updates the last_error field in the status snapshot.
func (idx *Indexer) recordError(sessionID string, err error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	fi := idx.status[sessionID]
	fi.LastError = err.Error()
	idx.status[sessionID] = fi
}
