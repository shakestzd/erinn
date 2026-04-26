package indexer

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/otel"
	"github.com/shakestzd/htmlgraph/internal/otel/sink"
)

const (
	pollInterval     = 500 * time.Millisecond
	defaultBatchSize = 500
)

// FileInfo holds per-file health metrics for the /api/indexer/status endpoint.
type FileInfo struct {
	LastOffset    int64     `json:"last_offset"`
	CurrentSize   int64     `json:"current_size"`
	LagBytes      int64     `json:"lag_bytes"`
	LastError     string    `json:"last_error"`
	LastIndexedAt time.Time `json:"last_indexed_at"`
}

// Indexer polls .htmlgraph/sessions/*/events.ndjson files for new appends,
// parses each line into a UnifiedSignal, and applies them to SQLite via snk.
type Indexer struct {
	htmlgraphDir string
	snk          sink.SignalSink
	database     *sql.DB // optional; enables prompt_id bridging when set

	mu     sync.RWMutex
	status map[string]FileInfo
}

// New constructs an Indexer rooted at htmlgraphDir.
// htmlgraphDir is the .htmlgraph/ directory (e.g. /path/to/project/.htmlgraph).
func New(htmlgraphDir string, snk sink.SignalSink) *Indexer {
	return &Indexer{
		htmlgraphDir: htmlgraphDir,
		snk:          snk,
		status:       make(map[string]FileInfo),
	}
}

// WithDB attaches a *sql.DB to the indexer so it can bridge OTel prompt_id
// values back to the corresponding UserQuery rows in agent_events. This is
// optional: when not set, prompt_id bridging is silently skipped.
func (idx *Indexer) WithDB(database *sql.DB) *Indexer {
	idx.database = database
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

// RunOnce performs a single discover-and-apply pass. Exported so the
// per-session collector can drive a final drain after Start has returned
// (e.g. on SIGTERM): pass a fresh non-cancelled context with the remaining
// shutdown budget so the cancelled poll-loop ctx does not short-circuit
// the drain.
func (idx *Indexer) RunOnce(ctx context.Context) {
	idx.runOnce(ctx)
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
func (idx *Indexer) discoverSessions() ([]string, error) {
	sessionsDir := filepath.Join(idx.htmlgraphDir, "sessions")
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
	return sessions, nil
}

// processSession tails events.ndjson for sessionID from the last checkpoint,
// parses each line, and applies the batch to snk. On success, writes a new checkpoint.
func (idx *Indexer) processSession(ctx context.Context, sessionID string) error {
	sessDir := filepath.Join(idx.htmlgraphDir, "sessions", sessionID)
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	checkpointPath := filepath.Join(sessDir, ".index-offset")

	offset, err := readCheckpoint(checkpointPath)
	if err != nil {
		return err
	}

	info, err := os.Stat(ndjsonPath)
	if err != nil {
		return err
	}
	currentSize := info.Size()
	idx.updateSize(sessionID, offset, currentSize)

	if currentSize <= offset {
		return nil // no new data
	}

	parsed, newOffset, err := idx.readNewSignals(ndjsonPath, offset)
	if err != nil {
		return err
	}
	if len(parsed) == 0 {
		return writeCheckpoint(checkpointPath, newOffset)
	}

	if err := idx.writeParsedBatch(ctx, parsed); err != nil {
		return err
	}

	if err := writeCheckpoint(checkpointPath, newOffset); err != nil {
		return err
	}

	idx.recordSuccess(sessionID, newOffset, currentSize)
	return nil
}

// readNewSignals opens ndjsonPath, seeks to offset, reads complete
// newline-terminated lines, and parses them. Incomplete trailing data
// (no newline at EOF) is left uncheckpointed so the next poll retries
// once the writer finishes the line.
func (idx *Indexer) readNewSignals(ndjsonPath string, offset int64) ([]parsedSignal, int64, error) {
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

// writeParsedBatch persists parsed signals through the sink, grouping
// consecutive entries that share the same harness + resourceAttrs into a
// single WriteBatch call. The receiver.Writer wraps each WriteBatch in
// one BEGIN IMMEDIATE transaction (writer.go:20-22), so each group becomes
// one txn — this turns N sequential INSERTs into ⌈N/batchSize⌉ batched
// txns and clears the throughput floor for the slice-1 indexer mount.
//
// Group flushes whenever (a) the next signal's harness or resourceAttrs
// differ, or (b) the accumulator hits the batch-size cap (defaults to
// defaultBatchSize, override via HTMLGRAPH_INDEXER_BATCH_SIZE). Capping
// bounds rollback cost on partial failure and keeps any single
// transaction short enough not to starve the work-item HTML hooks that
// share the canonical DB.
//
// On any WriteBatch error the function returns immediately — the caller
// (processSession) will skip the writeCheckpoint call so the failed
// span and everything after it replays on the next poll tick.
//
// maybeSetPromptID still runs per signal after each successful flush so
// the user_prompt → UserQuery bridge stays correct.
func (idx *Indexer) writeParsedBatch(ctx context.Context, parsed []parsedSignal) error {
	if len(parsed) == 0 {
		return nil
	}
	batchCap := batchSizeCap()

	var (
		curHarness  otel.Harness
		curResAttrs map[string]any
		curResKey   string
		acc         []otel.UnifiedSignal
	)

	flush := func() error {
		if len(acc) == 0 {
			return nil
		}
		if err := idx.snk.WriteBatch(ctx, curHarness, curResAttrs, acc); err != nil {
			return err
		}
		for i := range acc {
			idx.maybeSetPromptID(acc[i])
		}
		acc = acc[:0]
		return nil
	}

	for _, p := range parsed {
		h := p.Signal.Harness
		if h == "" {
			h = otel.HarnessClaude
		}
		resKey := resourceAttrsKey(p.ResourceAttrs)

		if len(acc) > 0 && (h != curHarness || resKey != curResKey || len(acc) >= batchCap) {
			if err := flush(); err != nil {
				return err
			}
		}
		if len(acc) == 0 {
			curHarness = h
			curResAttrs = p.ResourceAttrs
			curResKey = resKey
		}
		acc = append(acc, p.Signal)
	}
	return flush()
}

// resourceAttrsKey returns a stable string key for a resourceAttrs map
// so writeParsedBatch can detect when consecutive signals' resource
// attributes diverge and flush before merging them. encoding/json sorts
// map keys when marshaling, giving a deterministic byte sequence.
func resourceAttrsKey(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// batchSizeCap reads HTMLGRAPH_INDEXER_BATCH_SIZE; returns defaultBatchSize
// when unset, unparseable, or non-positive.
func batchSizeCap() int {
	if s := os.Getenv("HTMLGRAPH_INDEXER_BATCH_SIZE"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return defaultBatchSize
}

// maybeSetPromptID correlates a user_prompt OTel signal back to the closest
// UserQuery event in agent_events by session_id + timestamp. It is a no-op
// when the indexer has no database attached, the signal is not a user_prompt,
// or the signal carries no prompt_id.
func (idx *Indexer) maybeSetPromptID(sig otel.UnifiedSignal) {
	if idx.database == nil {
		return
	}
	if sig.Kind != otel.KindLog {
		return
	}
	if sig.CanonicalName != otel.CanonicalUserPrompt {
		return
	}
	if sig.PromptID == "" || sig.SessionID == "" {
		return
	}
	if err := db.SetPromptID(idx.database, sig.SessionID, sig.PromptID, sig.Timestamp); err != nil {
		log.Printf("indexer: set prompt_id (session=%s, prompt=%s): %v",
			sig.SessionID, sig.PromptID, err)
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
