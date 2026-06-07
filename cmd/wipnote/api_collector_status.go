package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/internal/otel/collector"
)

// WriterServiceStatus is the diagnostic JSON the dashboard reads from
// /api/collector-status to render the slice-6 writer-queue backpressure
// indicator. State is one of "init" / "running" / "draining" /
// "stopped"; "disabled" means the serve process did not construct a
// writer service (e.g., test harness, DB open failed at startup).
//
// Depth + Capacity + EnqueueRate + DequeueRate are the primary signals
// the dashboard surfaces; Enqueued / Dequeued / Rejected / Errors are
// monotonic counters useful for the contention-observability gate
// (slice 10).
//
// Slice-10 additions (additive — existing dashboard fields untouched):
//   - BusySubsystems / FirstPartyBusyTotal: process-level SQLITE_BUSY
//     classification keyed by subsystem; the launch criterion is
//     FirstPartyBusyTotal == 0 across the contention stress fixture.
type WriterServiceStatus struct {
	State               string           `json:"state"`
	Depth               int              `json:"depth"`
	Capacity            int              `json:"capacity"`
	Enqueued            int64            `json:"enqueued"`
	Dequeued            int64            `json:"dequeued"`
	Rejected            int64            `json:"rejected"`
	Errors              int64            `json:"errors"`
	EnqueueRatePerSec   float64          `json:"enqueue_rate_per_sec"`
	DequeueRatePerSec   float64          `json:"dequeue_rate_per_sec"`
	BusySubsystems      map[string]int64 `json:"busy_subsystems,omitempty"`
	FirstPartyBusyTotal int64            `json:"first_party_busy_total"`
}

// collectorWriterStatusHandler returns the live writer-queue status as
// JSON. Returns "disabled" state when no writer service is wired (which
// is correct for unit tests that build the mux without a DB).
func collectorWriterStatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out := readWriterServiceStatus(writerService.queue)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// readWriterServiceStatus converts queue.Stats into the wire shape
// the dashboard expects. Exposed so the test file can probe it without
// instantiating an HTTP recorder.
//
// Slice-10: every response also carries the process-level BUSY counters
// regardless of queue state — the counters are independent of the
// writer-service lifecycle (they accumulate even if the queue is
// "disabled" because callers from `wipnote serve` early-startup still
// record into them).
func readWriterServiceStatus(q *writequeue.Queue) WriterServiceStatus {
	busy := dbpkg.BusyCounts()
	subsystems := make(map[string]int64, len(busy))
	for k, v := range busy {
		subsystems[string(k)] = v
	}
	firstPartyTotal := dbpkg.FirstPartyBusyTotal()
	if q == nil {
		return WriterServiceStatus{
			State:               "disabled",
			BusySubsystems:      subsystems,
			FirstPartyBusyTotal: firstPartyTotal,
		}
	}
	s := q.Stats()
	return WriterServiceStatus{
		State:               string(s.State),
		Depth:               s.Depth,
		Capacity:            s.Capacity,
		Enqueued:            s.Enqueued,
		Dequeued:            s.Dequeued,
		Rejected:            s.Rejected,
		Errors:              s.Errors,
		EnqueueRatePerSec:   s.EnqueueRatePerSec,
		DequeueRatePerSec:   s.DequeueRatePerSec,
		BusySubsystems:      subsystems,
		FirstPartyBusyTotal: firstPartyTotal,
	}
}

// CollectorStatus holds the live health of a per-session OTel collector.
// Exported so tests can deserialise the HTTP response into this struct.
type CollectorStatus struct {
	PID            int   `json:"pid"`
	Port           int   `json:"port"`
	Alive          bool  `json:"alive"`
	UptimeSec      int64 `json:"uptime_s"`
	LastActivityMs int64 `json:"last_activity_ms"`
	// SignalsIngested is always 0 — counting every signal line on hot paths
	// is costly; use the file mtime for last_activity_ms instead.
	SignalsIngested int64 `json:"signals_ingested,omitempty"`
}

// ReadCollectorStatus constructs a CollectorStatus for the given session
// directory by reading:
//   - .collector-pid  → PID + liveness probe (start-time-verified on Linux)
//   - events.ndjson   → collector_start event for port + start timestamp
//
// Returns an error only when .collector-pid is missing or unreadable.
// A dead (unreachable) process is not an error — Alive is set to false.
func ReadCollectorStatus(sessDir string) (CollectorStatus, error) {
	pid, err := readPIDFile(filepath.Join(sessDir, ".collector-pid"))
	if err != nil {
		return CollectorStatus{}, fmt.Errorf("read collector PID: %w", err)
	}

	// Use the collector package's identity-verifying liveness check rather
	// than a bare kill(pid,0) probe, so a recycled PID is reported as dead.
	alive, _ := collector.IsCollectorAlive(sessDir)

	port, startTS := readCollectorStartEvent(filepath.Join(sessDir, "events.ndjson"))

	var uptimeSec int64
	if !startTS.IsZero() {
		uptimeSec = int64(time.Since(startTS).Seconds())
		if uptimeSec < 0 {
			uptimeSec = 0
		}
	}

	// LastActivityMs: use events.ndjson mtime as a cheap proxy.
	// Scanning every signal line would be too costly on busy sessions.
	lastActivityMs := fileModTimeMs(filepath.Join(sessDir, "events.ndjson"))

	return CollectorStatus{
		PID:            pid,
		Port:           port,
		Alive:          alive,
		UptimeSec:      uptimeSec,
		LastActivityMs: lastActivityMs,
	}, nil
}

// collectorStatusHandler returns an http.HandlerFunc for GET /api/otel/status.
// Query param: ?session=<session-id>  (matches ?session= used by transcriptHandler)
// projectDir is the project root (parent of .wipnote/).
func collectorStatusHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionID := r.URL.Query().Get("session")
		if sessionID == "" {
			http.Error(w, "session parameter required", http.StatusBadRequest)
			return
		}
		if !isSafeSessionID(sessionID) {
			http.Error(w, "invalid session id", http.StatusBadRequest)
			return
		}
		sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
		status, err := ReadCollectorStatus(sessDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		respondJSON(w, status)
	}
}

// isSafeSessionID rejects values that contain path separators, ".." segments,
// or NUL bytes, preventing the session query parameter from escaping the
// .wipnote/sessions/ directory via path traversal.
func isSafeSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`+"\x00") {
		return false
	}
	if strings.Contains(id, "..") {
		return false
	}
	return true
}

// readPIDFile reads the collector PID from .collector-pid. The file format
// may be either single-line (legacy: "<pid>\n") or two-line ("<pid>\n<starttime>\n",
// new format used by Linux writes). Delegates to collector.ReadCollectorPIDFile
// so all consumers stay in sync with the writer.
func readPIDFile(path string) (int, error) {
	pid, _, _, err := collector.ReadCollectorPIDFile(path)
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// collectorStartLine is the minimal shape we parse from events.ndjson.
type collectorStartLine struct {
	Kind  string         `json:"kind"`
	TS    string         `json:"ts"`
	Attrs map[string]any `json:"attrs"`
}

// readCollectorStartEvent scans events.ndjson for the first collector_start
// line and extracts port + timestamp. Returns zero values when the file is
// missing or the event has not been written yet.
func readCollectorStartEvent(evPath string) (port int, startTS time.Time) {
	f, err := os.Open(evPath)
	if err != nil {
		return 0, time.Time{}
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line collectorStartLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue
		}
		if line.Kind != "collector_start" {
			continue
		}
		// Extract port — may be float64 or int depending on JSON decoder.
		if p, ok := line.Attrs["port"]; ok {
			switch v := p.(type) {
			case float64:
				port = int(v)
			case int:
				port = v
			}
		}
		if line.TS != "" {
			startTS, _ = time.Parse(time.RFC3339Nano, line.TS)
		}
		return port, startTS
	}
	return 0, time.Time{}
}

// fileModTimeMs returns the file's modification time as Unix milliseconds,
// or 0 if the file does not exist.
func fileModTimeMs(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().UnixMilli()
}
