package main

// bug-74a7bda7: durable, filesystem-agnostic SQLITE_BUSY fix.
//
// These tests prove the fix behaves identically under WAL (host installs on
// APFS/ext4) and DELETE (constrained overlayfs/FUSE devcontainers) journal
// modes. The journal mode is injected DIRECTLY via `PRAGMA journal_mode` on a
// real temp-file DB — NOT by stubbing isUnsafeForMmap / editing pragmas.go
// (those are correct and out of scope per the bug brief). Forcing the mode
// explicitly on a temp-file DB is filesystem-agnostic by construction: the
// assertion holds regardless of the host filesystem under the test.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/internal/otel"
	otelreceiver "github.com/shakestzd/wipnote/internal/otel/receiver"

	_ "modernc.org/sqlite"
)

// newJournalModeDB creates a fresh schema'd temp-file DB and forces it into
// the requested journal mode ("wal" or "delete"). Returns the db path so
// callers can open additional handles (read pool, writer service) against
// the same file. The schema-creating writable handle is closed before
// returning so it does not itself contend; the file's journal mode persists.
func newJournalModeDB(t *testing.T, mode string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "busy-agnostic.db")
	w, err := db.Open(dbPath) // creates schema + runs migrations
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if _, err := w.Exec("PRAGMA journal_mode = " + mode); err != nil {
		w.Close()
		t.Fatalf("set journal_mode=%s: %v", mode, err)
	}
	got := db.QueryJournalMode(w)
	w.Close()
	// On a WAL-unsafe host filesystem an attempt to set WAL silently stays
	// DELETE. Skip the WAL variant there rather than assert a false negative
	// — the DELETE variant still proves the filesystem-agnostic contract.
	if mode == "wal" && got != "wal" {
		t.Skipf("host filesystem rejected WAL (got %q); DELETE variant covers this host", got)
	}
	if mode == "delete" && got != "delete" {
		t.Fatalf("journal_mode = %q, want delete", got)
	}
	return dbPath
}

// seedFeature inserts an in-progress feature row that the completion writer
// will transition to done under read contention.
func seedFeature(t *testing.T, dbPath, id string) {
	t.Helper()
	w, err := db.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable seed: %v", err)
	}
	defer w.Close()
	_, err = w.Exec(
		`INSERT INTO features (id, type, title, status) VALUES (?, 'bug', 'Agnostic', 'in-progress')`,
		id,
	)
	if err != nil {
		t.Fatalf("seed feature: %v", err)
	}
}

// TestBusyFix_FilesystemAgnostic is the cornerstone regression test. For
// BOTH journal modes it spins up a dashboard-like read pool (many concurrent
// readers on a capped, read-only handle) hammering the DB while the CLI
// completion write path (UpdateFeatureStatus wrapped in RetryOnBusy via
// Collection.Complete's helper) transitions a feature to done. It asserts:
//
//   - the completion write ultimately succeeds (after backoff if needed), and
//   - db.FirstPartyBusyTotal() stays 0 (the cli_mutation counter is only
//     bumped on a TERMINAL busy; transient busy that the retry absorbs must
//     not leak into the launch-gate counter).
func TestBusyFix_FilesystemAgnostic(t *testing.T) {
	for _, mode := range []string{"wal", "delete"} {
		t.Run(mode, func(t *testing.T) {
			db.ResetBusyCounters()
			dbPath := newJournalModeDB(t, mode)
			const featID = "bug-agnostic-1"
			seedFeature(t, dbPath, featID)

			// Read pool: read-only handle, capped exactly like the
			// dashboard mux (dashboardReadPoolMaxConns).
			readPool, err := db.OpenReadOnly(dbPath)
			if err != nil {
				t.Fatalf("OpenReadOnly: %v", err)
			}
			defer readPool.Close()
			readPool.SetMaxOpenConns(dashboardReadPoolMaxConns)

			// Dedicated writable handle for the completion path (this is
			// the rerouted topology: writers never use the ro mux handle).
			writeDB, err := db.OpenWritable(dbPath)
			if err != nil {
				t.Fatalf("OpenWritable: %v", err)
			}
			defer writeDB.Close()

			ctx, cancel := context.WithCancel(context.Background())
			var readers sync.WaitGroup
			for range 24 {
				readers.Add(1)
				go func() {
					defer readers.Done()
					for ctx.Err() == nil {
						var n int
						_ = readPool.QueryRow(
							`SELECT COUNT(*) FROM features`).Scan(&n)
					}
				}()
			}

			// Completion write path: same RetryOnBusy budget the real
			// Collection.Complete helper uses.
			var writeErr error
			done := make(chan struct{})
			go func() {
				defer close(done)
				writeErr = db.RetryOnBusy(db.DefaultBusyBackoff, func() error {
					return db.UpdateFeatureStatus(writeDB, featID, "done")
				})
				db.Record(db.SubsystemCLIMutation, writeErr)
			}()

			select {
			case <-done:
			case <-time.After(20 * time.Second):
				cancel()
				readers.Wait()
				t.Fatal("completion write did not finish within 20s")
			}
			cancel()
			readers.Wait()

			if writeErr != nil {
				t.Fatalf("[%s] completion write failed after backoff: %v", mode, writeErr)
			}
			if total := db.FirstPartyBusyTotal(); total != 0 {
				t.Fatalf("[%s] FirstPartyBusyTotal = %d, want 0 "+
					"(transient busy must be absorbed by retry)", mode, total)
			}

			// Verify the transition actually landed.
			var status string
			if err := writeDB.QueryRow(
				`SELECT status FROM features WHERE id = ?`, featID,
			).Scan(&status); err != nil {
				t.Fatalf("read back status: %v", err)
			}
			if status != "done" {
				t.Fatalf("[%s] status = %q, want done", mode, status)
			}
		})
	}
}

// TestReadOnlyMuxRejectsWritesButQueuePathSucceeds proves the STEP 1
// cornerstone: the dashboard mux handle (OpenReadOnly) hard-rejects any
// write, while the slice-6 write-queue path against a separate writable
// handle still commits successfully against the same DB file.
func TestReadOnlyMuxRejectsWritesButQueuePathSucceeds(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ro-mux.db")
	if w, err := db.Open(dbPath); err != nil { // create schema
		t.Fatalf("db.Open: %v", err)
	} else {
		w.Close()
	}

	muxDB, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly mux: %v", err)
	}
	defer muxDB.Close()

	// The mux handle must reject every write verb.
	if _, err := muxDB.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('x','bug','x','todo')`,
	); err == nil {
		t.Fatal("read-only mux handle accepted an INSERT; want SQLITE_READONLY")
	}

	// The slice-6 writer + write queue, owning the only writable handle,
	// still commits a real signal against the same file.
	writer, err := otelreceiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer writer.Close()

	q := writequeue.New(writequeue.Config{Capacity: 16})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	defer q.Stop(2 * time.Second)

	sig := otel.UnifiedSignal{
		Harness:       otel.HarnessClaude,
		SignalID:      "sig-ro-mux-1",
		Kind:          otel.KindLog,
		CanonicalName: otel.CanonicalUserPrompt,
		NativeName:    "user_prompt",
		Timestamp:     time.Now(),
		SessionID:     "sess-ro-mux",
	}
	submitErr := q.SubmitSync(context.Background(), func(ctx context.Context) error {
		_, werr := writer.WriteBatch(ctx, otel.HarnessClaude, nil,
			[]otel.UnifiedSignal{sig})
		return werr
	})
	if submitErr != nil {
		t.Fatalf("write-queue path failed: %v", submitErr)
	}

	// The mux (read-only) can still SEE the queue-written row.
	var n int
	if err := muxDB.QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE signal_id = ?`, "sig-ro-mux-1",
	).Scan(&n); err != nil {
		t.Fatalf("mux read-back: %v", err)
	}
	if n != 1 {
		t.Fatalf("queue-written signal count = %d, want 1 "+
			"(read-only mux must observe the writable path's commit)", n)
	}
}
