// Package ndjson provides a SignalSink that appends unified OTel signals as
// newline-delimited JSON (one line per signal) to a per-session events.ndjson
// file. No DB connection is opened. Placeholder/upgrade logic is intentionally
// absent — the NDJSON→SQLite indexer (slice 5) handles that on replay.
//
// File layout: .wipnote/sessions/<session_id>/events.ndjson
//
// Each line is a JSON object with all UnifiedSignal fields plus:
//   - "kind"    — signal kind ("span", "metric", "log")
//   - "ts"      — timestamp in RFC3339Nano
//   - "harness" — harness name
//
// Durability: signals are staged in an in-memory bytes.Buffer and drained to
// the file under the cross-process flock after every FlushThreshold events and
// on the SyncInterval ticker, whichever comes first. This ensures events reach
// disk even if the process is killed (host sleep, devcontainer disconnect,
// SIGKILL) between writes. Close also flushes+syncs before releasing the file.
//
// flushAndSyncLocked acquires syscall.Flock(LOCK_EX) before writing the staged
// bytes (matching the pattern in session_html.go:147 and materialize.go:241),
// so concurrent collector + indexer processes cannot interleave partial records.
package ndjson

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/sink"
)

const (
	// FlushThreshold is the number of events after which a flush+sync is triggered.
	FlushThreshold = 64
	// SyncInterval is the maximum time between periodic flush+sync calls.
	SyncInterval = 2 * time.Second
)

// Sink appends signals to a per-session NDJSON file.
// Marshaled lines accumulate in an in-memory bytes.Buffer. flushAndSyncLocked
// acquires the cross-process flock and writes the staged bytes directly to the
// file in a single atomic append, so concurrent collector + indexer processes
// can't interleave partial records.
// A background goroutine periodically flushes and syncs the file.
type Sink struct {
	path string
	mu   sync.Mutex // guards f, buf, eventCount, closed

	f          *os.File
	buf        bytes.Buffer
	eventCount int
	closed     bool

	stopCh chan struct{}
}

// New constructs a Sink for the given project directory and session ID.
// The events.ndjson file is opened immediately in append+create mode so that
// a replacement collector (after host sleep or reconnect) extends the same log
// rather than starting empty. The session directory must already exist.
// A background goroutine starts to periodically flush+sync the file.
func New(projectDir, sessionID string) (*Sink, error) {
	path := filepath.Join(projectDir, ".wipnote", "sessions", sessionID, "events.ndjson")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ndjson open %s: %w", path, err)
	}

	s := &Sink{
		path:   path,
		f:      f,
		stopCh: make(chan struct{}),
	}

	go s.periodicSync()
	return s, nil
}

// periodicSync runs in the background and flushes+syncs the file every SyncInterval.
// It exits when the sink is closed.
func (s *Sink) periodicSync() {
	ticker := time.NewTicker(SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			if !s.closed {
				_ = s.flushAndSyncLocked()
			}
			s.mu.Unlock()
		case <-s.stopCh:
			return
		}
	}
}

// flushAndSyncLocked writes any staged bytes to the file and fsyncs. Must be
// called with s.mu held. The cross-process flock is held for the duration of
// the file write + sync so concurrent processes cannot interleave partial
// records. The buffer is reset only after a successful write so a failure
// preserves the unwritten bytes for the next flush attempt.
func (s *Sink) flushAndSyncLocked() error {
	if s.f == nil {
		return nil
	}
	if s.buf.Len() == 0 {
		return nil
	}
	if err := syscall.Flock(int(s.f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("ndjson flock %s for flush: %w", s.path, err)
	}
	defer syscall.Flock(int(s.f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	// Drain the buffer: advance past written bytes on partial write so a
	// retry doesn't duplicate what already reached disk.
	for s.buf.Len() > 0 {
		n, err := s.f.Write(s.buf.Bytes())
		if n > 0 {
			s.buf.Next(n)
		}
		if err != nil {
			return fmt.Errorf("ndjson write: %w", err)
		}
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("ndjson fsync: %w", err)
	}
	return nil
}

// WriteBatch appends one JSON line per signal to events.ndjson.
// Lines are staged in an in-memory bytes.Buffer; flushAndSyncLocked drains
// the buffer to the file under the cross-process flock so concurrent
// collector + indexer processes can't interleave partial records. Empty
// batches are a no-op. After every FlushThreshold cumulative events a
// flush+sync is triggered.
func (s *Sink) WriteBatch(_ context.Context, harness otel.Harness, resourceAttrs map[string]any, signals []otel.UnifiedSignal) error {
	if len(signals) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("ndjson sink is closed")
	}

	for i := range signals {
		line, err := marshalLine(harness, resourceAttrs, &signals[i])
		if err != nil {
			return fmt.Errorf("ndjson marshal signal %s: %w", signals[i].SignalID, err)
		}
		s.buf.Write(line)
		s.buf.WriteByte('\n')
		s.eventCount++
	}

	// Flush+sync after every FlushThreshold events to bound the data-loss window.
	if s.eventCount >= FlushThreshold {
		s.eventCount = 0
		if err := s.flushAndSyncLocked(); err != nil {
			return err
		}
	}

	return nil
}

// Flush immediately flushes the in-memory buffer and fsyncs the underlying
// file to stable storage. Callers that need guaranteed durability before the
// next periodic tick (e.g. after writing a sentinel event) should call this.
func (s *Sink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushAndSyncLocked()
}

// Close flushes buffered data, syncs the file to disk, stops the background
// goroutine, and closes the file handle. Safe to call multiple times.
// Short-lived callers must call Close (or defer it) after writing to guarantee
// durability — the periodic ticker will not fire before a hook process exits.
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	close(s.stopCh)

	var firstErr error
	if err := s.flushAndSyncLocked(); err != nil && firstErr == nil {
		firstErr = err
	}
	if s.f != nil {
		if err := s.f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.f = nil
	}
	return firstErr
}

// Ensure Sink implements SignalSink at compile time.
var _ sink.SignalSink = (*Sink)(nil)

// signalLine is the on-disk JSON representation of a single signal.
// Top-level fields carry the most-queried attributes; RawAttrs holds everything else.
type signalLine struct {
	Kind      string `json:"kind"`
	Harness   string `json:"harness"`
	TS        string `json:"ts"`
	SignalID  string `json:"signal_id"`
	SessionID string `json:"session_id"`
	PromptID  string `json:"prompt_id,omitempty"`

	CanonicalName string `json:"canonical,omitempty"`
	NativeName    string `json:"native,omitempty"`

	TraceID    string `json:"trace_id,omitempty"`
	SpanID     string `json:"span_id,omitempty"`
	ParentSpan string `json:"parent_span,omitempty"`

	ToolName       string `json:"tool_name,omitempty"`
	ToolUseID      string `json:"tool_use_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Decision       string `json:"decision,omitempty"`
	DecisionSource string `json:"decision_source,omitempty"`

	TokensInput         int64 `json:"tokens_input,omitempty"`
	TokensOutput        int64 `json:"tokens_output,omitempty"`
	TokensCacheRead     int64 `json:"tokens_cache_read,omitempty"`
	TokensCacheCreation int64 `json:"tokens_cache_creation,omitempty"`
	TokensThought       int64 `json:"tokens_thought,omitempty"`
	TokensTool          int64 `json:"tokens_tool,omitempty"`
	TokensReasoning     int64 `json:"tokens_reasoning,omitempty"`

	CostUSD    float64 `json:"cost_usd,omitempty"`
	CostSource string  `json:"cost_source,omitempty"`

	DurationMs int64  `json:"duration_ms,omitempty"`
	Success    *bool  `json:"success,omitempty"`
	ErrorMsg   string `json:"error_msg,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`

	ResourceAttrs map[string]any `json:"resource_attrs,omitempty"`
	Attrs         map[string]any `json:"attrs,omitempty"`
}

// marshalLine converts a UnifiedSignal into a JSON byte slice for NDJSON output.
func marshalLine(harness otel.Harness, resourceAttrs map[string]any, s *otel.UnifiedSignal) ([]byte, error) {
	line := signalLine{
		Kind:                string(s.Kind),
		Harness:             string(harness),
		TS:                  s.Timestamp.UTC().Format(time.RFC3339Nano),
		SignalID:            s.SignalID,
		SessionID:           s.SessionID,
		PromptID:            s.PromptID,
		CanonicalName:       s.CanonicalName,
		NativeName:          s.NativeName,
		TraceID:             s.TraceID,
		SpanID:              s.SpanID,
		ParentSpan:          s.ParentSpan,
		ToolName:            s.ToolName,
		ToolUseID:           s.ToolUseID,
		Model:               s.Model,
		Decision:            s.Decision,
		DecisionSource:      s.DecisionSource,
		TokensInput:         s.Tokens.Input,
		TokensOutput:        s.Tokens.Output,
		TokensCacheRead:     s.Tokens.CacheRead,
		TokensCacheCreation: s.Tokens.CacheCreation,
		TokensThought:       s.Tokens.Thought,
		TokensTool:          s.Tokens.Tool,
		TokensReasoning:     s.Tokens.Reasoning,
		CostUSD:             s.CostUSD,
		CostSource:          string(s.CostSource),
		DurationMs:          s.DurationMs,
		Success:             s.Success,
		ErrorMsg:            s.ErrorMsg,
		Attempt:             s.Attempt,
		StatusCode:          s.StatusCode,
		ResourceAttrs:       resourceAttrs,
		Attrs:               s.RawAttrs,
	}
	return json.Marshal(line)
}
