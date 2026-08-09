package main

import (
	"database/sql"
	"fmt"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/gateledger"
)

// reindexGateRecords projects the canonical gate ledger
// (.wipnote/gate-ledger.html) into the derived gate_records table. Returns
// (rows inserted, errors).
//
// This is a FULL pass every time, never incremental, for the same two reasons as
// reindexClaimEpisodes: the ledger is small (one row per gate run, seventy-five
// in this repo's entire history), and — decisively — the incremental path keys
// off git-changed files while ledger writes are committed asynchronously through
// the commit queue. A gate run written but not yet flushed would be invisible to
// a git-diff-driven pass.
//
// # Insert-if-absent, not purge-and-rebuild
//
// The projection is idempotent on record_id via a partial unique index, so a
// record already in the table (written by the gate run itself) replays as a
// no-op. Purging first — the shape reindexClaimEpisodes uses — would be wrong
// here: it would also delete legacy rows written before the ledger existed, which
// have no canonical source to be rebuilt from. Those rows get canonical ids from
// backfillGateLedgerFromIndex instead, and only then become rebuildable.
//
// A purged cache therefore rebuilds every canonical record, and a warm cache
// costs one parse and a run of ignored inserts.
func reindexGateRecords(database *sql.DB, wipnoteDir string, verbose bool) (int, int) {
	if database == nil {
		return 0, 0
	}
	records, err := gateledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		if verbose {
			fmt.Printf("reindex gate ledger: %v\n", err)
		}
		return 0, 1
	}

	inserted, errCount := 0, 0
	for _, r := range records {
		wrote, insertErr := dbpkg.InsertGateRecordIfAbsent(database, dbpkg.GateRecordFromLedger(r))
		if insertErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex gate ledger: %s: %v\n", r.ID, insertErr)
			}
			continue
		}
		if wrote {
			inserted++
		}
	}
	return inserted, errCount
}

// backfillGateLedgerFromIndex gives a canonical home to gate runs recorded before
// the ledger existed. Returns (rows written, errors).
//
// This is the pass that actually closes bug-550c1cd8 for HISTORY rather than only
// for future runs. Without it the ledger would protect every gate run from today
// onward and the seventy-five already recorded would still vanish on the first
// cache purge — the exact loss the feature exists to prevent.
//
// It runs on every reindex and is self-terminating: each backfilled row is
// stamped with its new record_id, and the query only selects rows where that is
// empty, so the second pass finds nothing.
//
// DEDUPE IS BY SIGNATURE, not by the stamp. If the process dies between the
// ledger append and the stamp, the row is still unstamped and would be offered
// again; matching against the signatures already in the ledger — a checksum over
// the run's decision-relevant fields — keeps that retry from writing a duplicate.
func backfillGateLedgerFromIndex(database *sql.DB, wipnoteDir string, verbose bool) (int, int) {
	if database == nil {
		return 0, 0
	}
	legacy, err := dbpkg.UnledgeredGateRecords(database)
	if err != nil {
		if verbose {
			fmt.Printf("backfill gate ledger: %v\n", err)
		}
		return 0, 1
	}
	if len(legacy) == 0 {
		return 0, 0
	}

	store := gateledger.NewStore(wipnoteDir)
	seen, err := store.Signatures()
	if err != nil {
		if verbose {
			fmt.Printf("backfill gate ledger: read ledger: %v\n", err)
		}
		return 0, 1
	}

	written, errCount := 0, 0
	for _, row := range legacy {
		if row.Signature != "" && seen[row.Signature] {
			// Already canonical from an earlier interrupted pass. Stamp the row so
			// it stops being offered, but do not write it twice.
			if stampErr := dbpkg.SetGateRecordID(database, row.ID, gateledger.NewRecordID()); stampErr != nil && verbose {
				fmt.Printf("backfill gate ledger: stamp %d: %v\n", row.ID, stampErr)
			}
			continue
		}

		record, appendErr := store.Append(gateledger.Record{
			SessionID:   row.SessionID,
			WorkItemID:  row.WorkItemID,
			Harness:     row.Harness,
			ProjectType: row.ProjectType,
			GateCommand: row.GateCommand,
			Status:      row.Status,
			CheckedAt:   row.CheckedAt,
			// The legacy signature is carried over VERBATIM rather than recomputed.
			// It is the record's integrity proof as it was actually recorded; a
			// recomputed one would re-bless a row that had been tampered with, and
			// would also mask any historical row whose signature never verified.
			Signature:         row.Signature,
			AllowlistHitsJSON: row.AllowlistHitsJSON,
			AllowlistHitCount: row.AllowlistHitCount,
			Source:            row.Source,
			OutputSummary:     row.OutputSummary,
			ProfileSignature:  row.ProfileSignature,
			GuardsRunJSON:     row.GuardsRunJSON,
		})
		if appendErr != nil {
			errCount++
			if verbose {
				fmt.Printf("backfill gate ledger: row %d: %v\n", row.ID, appendErr)
			}
			continue
		}
		if row.Signature != "" {
			seen[row.Signature] = true
		}
		if stampErr := dbpkg.SetGateRecordID(database, row.ID, record.ID); stampErr != nil {
			errCount++
			if verbose {
				fmt.Printf("backfill gate ledger: stamp %d: %v\n", row.ID, stampErr)
			}
			continue
		}
		written++
	}
	return written, errCount
}
