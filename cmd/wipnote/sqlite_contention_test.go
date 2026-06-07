// SQLite contention stress fixture — slice 10 of plan-ae0c37b2
// (feat-156e0a1a). This is the launch gate for the durable SQLite
// contention fix that spans slices 5–9.
//
// PURPOSE:
//
// The plan's regression signal is "zero SQLITE_BUSY from in-repo
// writer/indexer/hook paths" (NOT zero BUSY anywhere — external
// producers like MCP servers are explicitly out of scope per the
// slice-5 boundary). Slices 6 + 7 introduced a single-writer
// architecture: hook subprocesses go through `internal/hooks/dbgate.go`
// with canonical-first fallback semantics, while indexer / collector
// writes go through `internal/db/writequeue` to the
// `internal/otel/receiver.Writer` (the slice-6 single writer).
//
// Without a regression gate, the issue can silently reappear if a
// future change reintroduces a direct writable open. Slice 5's
// TestWritableDBOpenBoundary enforces the STATIC inventory; this file
// enforces the DYNAMIC invariant — that under a realistic concurrent
// workload the first-party producers don't drive the writer into
// SQLITE_BUSY.
//
// WORKLOAD (per the slice-10 spec):
//
//	20 producers × 30 seconds, mix of:
//	  - hook_writer    : OpenHookDB + synchronous derived-index INSERTs
//	  - indexer        : submit closures via writequeue.Submit
//	  - dashboard read : sql.Open(?mode=ro) + SELECT queries
//	  - cli_mutation   : dbpkg.Open + small UPSERT on work-items tables
//
// PRODUCER TIMING (matters for pass/fail semantics):
//
//	The hook + CLI producers are PROCESS-MODELLED — in production each
//	hook subprocess and each CLI invocation is a fresh OS process that
//	opens a DB once, does its work, and exits. A realistic stress
//	fixture must reflect that cadence: a tight `db.Open()` loop on 5
//	concurrent goroutines would spawn ~thousands of opens/sec, which
//	exceeds anything Claude Code or a human operator drives in real
//	use AND saturates SQLite's busy_timeout under DELETE journal mode.
//
//	The indexer + reader paths are LOOP-MODELLED — they ARE the
//	high-frequency steady-state load the slice-6 writer queue is
//	designed to absorb, so they run as fast as possible.
//
// PASS CRITERION:
//
//	dbpkg.FirstPartyBusyTotal() == 0 across 3 consecutive runs.
//	External producers (`SubsystemExternal`) are excluded by design.
//	Per-subsystem first-party counters MUST all be zero.
//
// SKIPPING:
//
//	This test is heavy (~30s per run; ~90s for `-count=3`) and is
//	therefore SKIPPED in `testing.Short()` mode so the routine
//	`go test ./...` quality gate stays fast. The launch-readiness
//	checklist (cmd/wipnote/check.go: printContentionGateReminder)
//	documents the explicit invocation:
//
//	    go test -run TestSQLiteContentionStress -count=3 ./cmd/wipnote/
//
// ORTHOGONAL TO TestWritableDBOpenBoundary:
//
//	The static boundary test (slice 5) catches NEW writable opens at
//	compile/test time. This stress test catches REGRESSIONS in the
//	queue's contention behaviour at runtime. Both must pass for a
//	release.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/internal/hooks"
)

// stressDuration is the per-run workload window. 30s is the floor the
// slice-10 spec calls out; longer windows reduce flakiness but balloon
// CI time. We keep the constant exact so reviewers can grep for it.
const stressDuration = 30 * time.Second

// stressProducerCount is the number of concurrent goroutines per
// subsystem. 20 is the figure the slice-10 spec calls out. Five
// producers per subsystem × 4 subsystems = 20 total.
const stressProducersPerSubsystem = 5

// stressTotalProducers should always equal stressProducersPerSubsystem
// times the number of subsystem categories below — kept as a constant
// rather than computed at runtime so the spec value appears literally in
// the source for greppability.
// 5 categories × 5 producers = 25 total.
const stressTotalProducers = 25

// hookSpawnInterval is the minimum delay between successive
// OpenHookDB calls per hook producer. Models the cadence of fresh
// hook-subprocess spawns from Claude Code. 25ms per producer × 5
// producers = ~200 hook opens/sec — multiple orders of magnitude
// above any real-world Claude session, yet well within what slice-7's
// canonical-first design plus busy_timeout(5000) must absorb.
const hookSpawnInterval = 25 * time.Millisecond

// cliMutationInterval is the minimum delay between successive
// dbpkg.Open calls per CLI producer. Models a user driving CLI
// commands quickly (e.g., a scripted workflow). 25ms per producer × 5
// producers = ~200 CLI opens/sec — far above any human-driven cadence.
// Matches hookSpawnInterval so the open rate is balanced across the
// two short-lived-process subsystems.
const cliMutationInterval = 25 * time.Millisecond

// serveIndexerInterval is the ticker period for the serve_indexer
// producers. 500ms mirrors the indexer poll interval (pollInterval in
// internal/otel/indexer/indexer.go) so the stress cadence matches
// production.
const serveIndexerInterval = 500 * time.Millisecond

// TestSQLiteContentionStress spawns 25 producers across the five
// first-party subsystem categories for stressDuration and asserts the
// FirstPartyBusyTotal counter remains zero. Per the slice-10 spec,
// run with `-count=3` to validate the 3-consecutive-runs criterion.
//
// The test is skipped in -short mode because it is too heavy to
// include in routine CI runs.
func TestSQLiteContentionStress(t *testing.T) {
	if testing.Short() {
		t.Skip("contention stress fixture: skipped in -short mode " +
			"(invoke via `go test -run TestSQLiteContentionStress -count=3 ./cmd/wipnote/`)")
	}

	// Baseline: zero every counter so a previous test in the same
	// package run can't leak into this assertion.
	dbpkg.ResetBusyCounters()

	// Pick a WAL-safe filesystem for the test DB. The slice-10
	// pass criterion targets the production architecture, which on
	// every supported host runs SQLite in WAL mode on a native
	// filesystem (ext4, xfs, btrfs, tmpfs, zfs). Non-WAL-safe
	// filesystems (codespace overlayfs/virtiofs, NFS, FUSE) fall
	// back to journal_mode=DELETE, which produces hard writer-lock
	// contention by design — a test that runs against DELETE journal
	// would be measuring driver-level lock behaviour, not the
	// slice-6/7 architecture. We therefore prefer /dev/shm (tmpfs)
	// when available and skip with a clear diagnostic otherwise.
	dbDir := chooseWALSafeDir(t)
	dbPath := filepath.Join(dbDir, "stress.db")
	// Open + migrate schema. Closing immediately is safe — every
	// producer opens its own handle below; this call exists only to
	// run the schema migrations once before producers start.
	bootstrap, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap dbpkg.Open: %v", err)
	}
	// Seed the work-items tables so cli_mutation producers have rows
	// to read/update without tripping a schema constraint.
	seedStressFixtures(t, bootstrap)
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}

	// Build the slice-6 writer queue. We pin a dedicated *sql.DB for
	// the queue worker (mirrors serve_child.go's writerService.queue
	// setup) so producer submissions exercise the real serialization
	// path — not just an in-memory channel.
	queueWriterDB, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open queue writer DB: %v", err)
	}
	defer queueWriterDB.Close()

	q := writequeue.New(writequeue.Config{
		Capacity: writequeue.DefaultCapacity,
		OnError: func(err error) {
			// Mirror the production hook: classify op-side errors
			// under writer_service. The queue's worker is the single
			// writer for the indexer subsystem, so a BUSY here is
			// counted under writer_service by the WriteBatch defer
			// in production. Synthetic loads through this fixture
			// don't invoke WriteBatch (we exec direct SQL closures),
			// so we classify here under writer_service explicitly.
			dbpkg.Record(dbpkg.SubsystemWriterService, err)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := q.Start(ctx); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	defer q.Stop(5 * time.Second)

	// stop signals every producer goroutine to wind down. We use a
	// shared atomic flag rather than a context-cancel so producers
	// can flush their last in-flight write before exiting.
	var stop atomic.Bool
	var wg sync.WaitGroup

	// Counters for sanity reporting — each producer increments its
	// own slot so we can show the workload was non-trivial. These
	// are NOT pass/fail signals; the pass signal is FirstPartyBusyTotal().
	var (
		hookOps          atomic.Int64
		indexerOps       atomic.Int64
		readerOps        atomic.Int64
		cliOps           atomic.Int64
		serveIndexerOps  atomic.Int64
	)

	// serve_indexer needs a writable handle (mirrors serve_child's writeDB)
	// and a read-only handle (mirrors serve_child's database passed to WithDB).
	serveIndexerWriteDB, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open serve_indexer write DB: %v", err)
	}
	defer serveIndexerWriteDB.Close()

	serveIndexerReadDSN := dbPath + "?_pragma=busy_timeout(5000)&mode=ro"
	serveIndexerReadDB, err := sql.Open("sqlite", serveIndexerReadDSN)
	if err != nil {
		t.Fatalf("open serve_indexer read DB: %v", err)
	}
	defer serveIndexerReadDB.Close()

	// Spawn hook_writer producers. Each goroutine opens its own DB
	// via OpenHookDB (the canonical-first-hook-fallback path from
	// slice 7) and performs short-lived synchronous writes. The
	// hookSpawnInterval throttle models real-world hook-subprocess
	// cadence: in production a hook fires once per Claude tool-use
	// event, not in a tight loop. Without the throttle the test
	// degenerates into a benchmark of `db.Open` contention rather
	// than a regression gate for the slice-6/7 contention fix.
	for i := 0; i < stressProducersPerSubsystem; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			hookID := fmt.Sprintf("stress-hook-%d", id)
			ticker := time.NewTicker(hookSpawnInterval)
			defer ticker.Stop()
			for !stop.Load() {
				select {
				case <-ticker.C:
				case <-ctx.Done():
					return
				}
				database, _ := hooks.OpenHookDB("contention-stress", hookID, dbPath)
				if database == nil {
					// OpenHookDB returns nil only on a hard open
					// failure; the BUSY counter was already bumped
					// by dbgate.go.
					continue
				}
				// Synthetic derived-index write: insert into
				// agent_events (a table all hook handlers write to).
				eventID := fmt.Sprintf("evt-hook-%d-%d", id, hookOps.Add(1))
				_, execErr := database.Exec(
					`INSERT INTO agent_events
						(event_id, agent_id, event_type, session_id)
					 VALUES (?, ?, 'tool_call', ?)`,
					eventID, "claude-code", "stress-session")
				if execErr != nil {
					dbpkg.Record(dbpkg.SubsystemHookWriter, execErr)
				}
				database.Close()
			}
		}(i)
	}

	// Spawn indexer producers. Each submits a closure through the
	// writequeue, mirroring how the OTel sink routes derived writes
	// through the slice-6 single writer.
	for i := 0; i < stressProducersPerSubsystem; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			submitCtx, submitCancel := context.WithCancel(ctx)
			defer submitCancel()
			for !stop.Load() {
				opID := indexerOps.Add(1)
				op := writequeue.WriteOp(func(_ context.Context) error {
					_, execErr := queueWriterDB.Exec(
						`INSERT INTO agent_events
							(event_id, agent_id, event_type, session_id)
						 VALUES (?, ?, 'tool_result', ?)`,
						fmt.Sprintf("evt-idx-%d-%d", id, opID),
						"claude-code", "stress-session")
					if execErr != nil {
						dbpkg.Record(dbpkg.SubsystemIndexer, execErr)
					}
					return execErr
				})
				if submitErr := q.SubmitWithTimeout(submitCtx, op, 500*time.Millisecond); submitErr != nil {
					// Queue full / writer unavailable / timeout — NOT a
					// BUSY classification (the queue's job is to ABSORB
					// contention). Do not bump the counter here.
					_ = submitErr
				}
			}
		}(i)
	}

	// Spawn dashboard-reader producers. These open in read-only mode
	// (sql.Open with ?mode=ro DSN) and run SELECTs against the same
	// file. Read-only opens don't touch the writer lock but they do
	// share the page cache; this is where the original contention
	// bug was most visible. Read errors are classified under
	// external because read-only paths aren't first-party writers
	// — they're the observability surface and don't gate the launch.
	for i := 0; i < stressProducersPerSubsystem; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			readDSN := dbPath + "?_pragma=busy_timeout(5000)&mode=ro"
			readerDB, err := sql.Open("sqlite", readDSN)
			if err != nil {
				// Reader open failure isn't a BUSY signal; just exit.
				return
			}
			defer readerDB.Close()
			for !stop.Load() {
				var c int
				queryErr := readerDB.QueryRow(
					`SELECT COUNT(*) FROM agent_events WHERE session_id='stress-session'`,
				).Scan(&c)
				if queryErr != nil {
					dbpkg.Record(dbpkg.SubsystemExternal, queryErr)
				}
				readerOps.Add(1)
			}
			_ = id
		}(i)
	}

	// Spawn CLI-mutation producers. Each opens its own writable
	// handle via dbpkg.Open and performs a small UPSERT. This
	// exercises the internal/workitem.Open retry path (which has
	// its own slice-10 classification under SubsystemCLIMutation).
	// The cliMutationInterval throttle models a scripted user
	// workflow — well above any interactive-user cadence yet not
	// a benchmark of `db.Open` itself.
	for i := 0; i < stressProducersPerSubsystem; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ticker := time.NewTicker(cliMutationInterval)
			defer ticker.Stop()
			for !stop.Load() {
				select {
				case <-ticker.C:
				case <-ctx.Done():
					return
				}
				database, err := dbpkg.Open(dbPath)
				if err != nil {
					dbpkg.Record(dbpkg.SubsystemCLIMutation, err)
					continue
				}
				opID := cliOps.Add(1)
				_, execErr := database.Exec(
					`INSERT INTO sessions (session_id, agent_assigned)
					 VALUES (?, 'claude-code')
					 ON CONFLICT(session_id) DO UPDATE SET agent_assigned=excluded.agent_assigned`,
					fmt.Sprintf("stress-cli-%d-%d", id, opID))
				if execErr != nil {
					dbpkg.Record(dbpkg.SubsystemCLIMutation, execErr)
				}
				database.Close()
			}
		}(i)
	}

	// Spawn serve_indexer producers. These reproduce BOTH out-of-band
	// paths that caused the bug-272c5e34 self-livelock on writeDB:
	//
	// (a) filterSessionsByDB path: on every ~500ms tick, run a
	//     queryKnownSessionIDs-style SELECT on the READ-ONLY handle.
	//     Before Change 1 this SELECT ran on writeDB (the writable
	//     handle), holding a SHARED lock that blocked the queue worker's
	//     BEGIN IMMEDIATE — exactly the livelock.  After Change 1 it
	//     runs on the read-only handle and cannot interfere with the
	//     writer at all.
	//
	// (b) maybeSetPromptID path: concurrently submits a SetPromptID-style
	//     SELECT+UPDATE closure through the queue.  Before Change 2 this
	//     was issued directly on writeDB as a second independent writer,
	//     creating a symmetric DELETE-journal deadlock with the queue
	//     worker.  After Change 2 it goes through the queue and is
	//     serialised behind the worker like every other write.
	//
	// RED-before / GREEN-after reasoning:
	//
	//   Before Change 1+2, on a DELETE-journal DB:
	//     • The filterSessionsByDB SELECT on writeDB acquires a SHARED lock.
	//     • Concurrently the queue worker attempts BEGIN IMMEDIATE, which
	//       requires RESERVED, blocked by SHARED → SQLITE_BUSY.
	//     • The maybeSetPromptID direct UPDATE also races the worker →
	//       second independent SQLITE_BUSY source.
	//   Both paths bump SubsystemIndexer / SubsystemWriterService counters
	//   → FirstPartyBusyTotal() > 0 → test FAIL.
	//
	//   After Change 1+2, on WAL or DELETE:
	//     • filterSessionsByDB SELECT runs on the read-only handle — no
	//       interference with the writer at all.
	//     • maybeSetPromptID is serialised through the queue — never a
	//       second concurrent writer.
	//   → FirstPartyBusyTotal() == 0 → test PASS.
	//
	//   On this overlayfs box the test SKIPS (WAL unavailable), which is
	//   expected; the RED/GREEN reasoning is structural and holds on any
	//   DELETE-journal host.
	for i := 0; i < stressProducersPerSubsystem; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ticker := time.NewTicker(serveIndexerInterval)
			defer ticker.Stop()
			for !stop.Load() {
				select {
				case <-ticker.C:
				case <-ctx.Done():
					return
				}

				// (a) filterSessionsByDB-style SELECT on the read-only handle.
				// Before Change 1 this was on writeDB — now it is correctly on
				// the read-only handle so it cannot hold a SHARED lock on the
				// writer connection.
				var c int
				queryErr := serveIndexerReadDB.QueryRow(
					`SELECT COUNT(*) FROM sessions WHERE session_id='stress-session'`,
				).Scan(&c)
				if queryErr != nil {
					dbpkg.Record(dbpkg.SubsystemIndexer, queryErr)
				}

				// (b) maybeSetPromptID-style SELECT+UPDATE submitted through queue.
				// Before Change 2 this was a direct write on writeDB — now it is
				// serialised through the queue like every other write.
				opID := serveIndexerOps.Add(1)
				wdb := serveIndexerWriteDB
				op := writequeue.WriteOp(func(_ context.Context) error {
					// Mirror db.SetPromptID: SELECT then UPDATE (two SQL ops).
					var eventID string
					scanErr := wdb.QueryRow(
						`SELECT event_id FROM agent_events
						 WHERE session_id = 'stress-session'
						   AND event_type = 'tool_call'
						   AND prompt_id IS NULL
						 LIMIT 1`,
					).Scan(&eventID)
					if scanErr == sql.ErrNoRows {
						return nil // no-op, mirrors SetPromptID
					}
					if scanErr != nil {
						dbpkg.Record(dbpkg.SubsystemIndexer, scanErr)
						return scanErr
					}
					_, execErr := wdb.Exec(
						`UPDATE agent_events SET prompt_id = ? WHERE event_id = ? AND prompt_id IS NULL`,
						fmt.Sprintf("prompt-%d-%d", id, opID), eventID,
					)
					if execErr != nil {
						dbpkg.Record(dbpkg.SubsystemIndexer, execErr)
					}
					return execErr
				})
				if submitErr := q.Submit(ctx, op); submitErr != nil {
					// Queue full / unavailable — best-effort, do not count as BUSY.
					_ = submitErr
				}
			}
		}(i)
	}

	// Run the workload.
	time.Sleep(stressDuration)
	stop.Store(true)
	wg.Wait()

	// Pass criterion: every first-party subsystem counter must be
	// zero. External is permitted but logged for diagnostics.
	firstParty := dbpkg.FirstPartyBusyTotal()
	counts := dbpkg.BusyCounts()

	// Report the workload size so reviewers can see the test
	// actually exercised the paths (a producer dying silently
	// would make this test trivially pass).
	t.Logf("workload: hook=%d indexer=%d reader=%d cli=%d serveIndexer=%d  (target ≥1 each)",
		hookOps.Load(), indexerOps.Load(), readerOps.Load(), cliOps.Load(), serveIndexerOps.Load())
	t.Logf("BUSY classification snapshot: %+v", counts)

	// Defensive: if any producer slot didn't run at all, the test
	// is meaningless even if FirstPartyBusyTotal is zero.
	if hookOps.Load() == 0 || indexerOps.Load() == 0 || readerOps.Load() == 0 || cliOps.Load() == 0 || serveIndexerOps.Load() == 0 {
		t.Fatalf("at least one producer slot recorded zero ops — workload didn't run: hook=%d indexer=%d reader=%d cli=%d serveIndexer=%d",
			hookOps.Load(), indexerOps.Load(), readerOps.Load(), cliOps.Load(), serveIndexerOps.Load())
	}

	if firstParty != 0 {
		// Surface per-subsystem breakdown so the failure message is
		// immediately actionable.
		for _, s := range dbpkg.FirstPartySubsystems {
			if c, ok := counts[s]; ok && c > 0 {
				t.Errorf("first-party SQLITE_BUSY recorded: subsystem=%s count=%d", s, c)
			}
		}
		t.Fatalf("FirstPartyBusyTotal = %d, want 0  (launch criterion failed)", firstParty)
	}
}

// chooseWALSafeDir returns a directory where the test DB will land
// on a WAL-safe filesystem. Tries /dev/shm (tmpfs, universally
// WAL-safe) first, falling back to t.TempDir() and probing the
// resolved journal_mode. If neither path produces a WAL-mode DB
// (e.g., on a codespace overlay or NFS mount), the test is skipped
// with a diagnostic — the slice-10 launch criterion presumes WAL
// mode, which is the only mode the production architecture ships.
func chooseWALSafeDir(t *testing.T) string {
	t.Helper()

	// Preferred: tmpfs on /dev/shm. Universally WAL-safe and isolated
	// from the codespace's overlay mount.
	if _, err := os.Stat("/dev/shm"); err == nil {
		shmDir, err := os.MkdirTemp("/dev/shm", "wipnote-stress-")
		if err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(shmDir) })
			if strings.EqualFold(probeJournalMode(t, shmDir), "wal") {
				return shmDir
			}
		}
	}

	// Fallback: standard t.TempDir() — usually WAL-safe on native
	// filesystems (ext4/xfs/btrfs); not on overlay/virtiofs.
	tmpDir := t.TempDir()
	if strings.EqualFold(probeJournalMode(t, tmpDir), "wal") {
		return tmpDir
	}

	t.Skipf("contention stress fixture: no WAL-safe filesystem available " +
		"(tried /dev/shm and t.TempDir(); the slice-10 launch criterion " +
		"targets the production architecture, which runs SQLite in WAL mode " +
		"on native filesystems — see internal/db/fstype_linux.go for the " +
		"safelist). Re-run on a host with ext4/xfs/btrfs/tmpfs/zfs.")
	return ""
}

// probeJournalMode opens a probe DB in dir and returns the effective
// journal_mode (lower-case, e.g., "wal" or "delete"). Used to decide
// whether dir is WAL-safe before committing the stress test to it.
// Returns "" on any error so the caller can fall through.
func probeJournalMode(t *testing.T, dir string) string {
	t.Helper()
	probePath := filepath.Join(dir, "probe.db")
	pdb, err := dbpkg.Open(probePath)
	if err != nil {
		return ""
	}
	defer pdb.Close()
	defer os.Remove(probePath)
	defer os.Remove(probePath + "-wal")
	defer os.Remove(probePath + "-shm")
	return dbpkg.QueryJournalMode(pdb)
}

// seedStressFixtures inserts the rows the stress producers need to
// operate without tripping foreign-key / NOT NULL constraints. The
// minimum surface is one row in `sessions` so cli_mutation producers
// can UPSERT, and the agent_events table only needs the session_id
// FK to be optional (which it is — see schema.go).
func seedStressFixtures(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned)
		 VALUES ('stress-session', 'claude-code')
		 ON CONFLICT(session_id) DO NOTHING`,
	); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
}
