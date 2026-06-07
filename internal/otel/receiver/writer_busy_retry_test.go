package receiver

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/otel"
)

func TestWriteBatchRetriesRawTransactionWhenSharedReaderBlocksCommit(t *testing.T) {
	db.ResetBusyCounters()
	origBackoff := db.DefaultBusyBackoff
	db.DefaultBusyBackoff = []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
	}
	t.Cleanup(func() {
		db.DefaultBusyBackoff = origBackoff
		db.ResetBusyCounters()
	})

	database, dbPath := openDeleteJournalOTelDB(t)
	defer database.Close()

	w, err := NewWriterFromDB(database)
	if err != nil {
		t.Fatalf("NewWriterFromDB: %v", err)
	}

	releaseReadLock := holdSharedReadLock(t, dbPath)
	released := make(chan error, 1)
	go func() {
		time.Sleep(250 * time.Millisecond)
		released <- releaseReadLock()
	}()

	inserted, err := w.WriteBatch(context.Background(), otel.HarnessClaude, nil, []otel.UnifiedSignal{
		busyRetrySignal("receiver"),
	})
	if err != nil {
		t.Fatalf("WriteBatch under temporary shared read lock: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}
	if err := <-released; err != nil {
		t.Fatalf("release read lock: %v", err)
	}
	if got := db.BusyCount(db.SubsystemWriterService); got != 0 {
		t.Fatalf("writer_service busy count = %d, want 0 for retried transient BUSY", got)
	}

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE signal_id = ?`, "sig-receiver").Scan(&count); err != nil {
		t.Fatalf("count inserted signal: %v", err)
	}
	if count != 1 {
		t.Fatalf("inserted signal count = %d, want 1", count)
	}
}

func openDeleteJournalOTelDB(t *testing.T) (*sql.DB, string) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "otel-busy.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	database.SetMaxOpenConns(1)

	var mode string
	if err := database.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		database.Close()
		t.Fatalf("force DELETE journal mode: %v", err)
	}
	if !strings.EqualFold(mode, "delete") {
		database.Close()
		t.Fatalf("journal_mode = %q, want DELETE", mode)
	}
	if _, err := database.Exec("PRAGMA busy_timeout=1"); err != nil {
		database.Close()
		t.Fatalf("set writer busy_timeout: %v", err)
	}
	return database, dbPath
}

func holdSharedReadLock(t *testing.T, dbPath string) func() error {
	t.Helper()

	reader, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	reader.SetMaxOpenConns(1)

	if _, err := reader.Exec("BEGIN"); err != nil {
		reader.Close()
		t.Fatalf("reader BEGIN: %v", err)
	}
	var count int
	if err := reader.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		reader.Close()
		t.Fatalf("reader SELECT: %v", err)
	}

	var once sync.Once
	return func() error {
		var releaseErr error
		once.Do(func() {
			if _, err := reader.Exec("COMMIT"); err != nil {
				releaseErr = err
			}
			if err := reader.Close(); releaseErr == nil && err != nil {
				releaseErr = err
			}
		})
		return releaseErr
	}
}

func busyRetrySignal(suffix string) otel.UnifiedSignal {
	return otel.UnifiedSignal{
		Harness:       otel.HarnessClaude,
		SignalID:      "sig-" + suffix,
		Kind:          otel.KindLog,
		CanonicalName: otel.CanonicalAPIRequest,
		NativeName:    "api_request",
		Timestamp:     time.Unix(0, 1735000000000000000),
		SessionID:     "sess-" + suffix,
		PromptID:      "prompt-" + suffix,
		RawAttrs:      map[string]any{"request_id": "req-" + suffix},
	}
}
