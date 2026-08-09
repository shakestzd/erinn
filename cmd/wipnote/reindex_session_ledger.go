package main

import (
	"database/sql"
	"fmt"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/sessionledger"
)

// reindexSessionLedger projects the canonical sessions ledger into the derived
// sessions table, inserting a row for every recorded session the table does not
// already hold.
//
// # Why projection, and not a fourth read path
//
// This one pass discharges BOTH halves of the ledger's contract, which is the
// reason it is written this way:
//
//   - VALIDITY. collectSessionIDs runs immediately after this and reads the
//     sessions table, so a ledger-only session lands in validIDs and
//     graph.ClassifyEdgeTarget answers EdgeTargetLive. The ledger becomes the
//     authority for sessions telemetry no longer knows about, without
//     ClassifyEdgeTarget learning anything about where an id came from.
//   - RENDERABILITY. Session titles are resolved from the sessions TABLE in
//     three independent readers — resolveNodes in core/graph/querybuilder.go,
//     resolveProvenanceNode, and loadGraphNodes. Making a session VALID from a
//     new source without giving those readers something to read produces a node
//     that indexes as ordinarily live, carries no tombstone marker, and still
//     renders blank: strictly worse than the tombstone it replaced, which at
//     least explained the blank. See the hazard card
//     edge-target-validity-and-renderability-are-separate, which names reusing
//     the sessions table as the projection as the alternative to touching all
//     three readers. This is that alternative. Adding a fourth reader later
//     cannot reintroduce the hazard, because there is no ledger-shaped node that
//     the table does not already carry.
//
// # Ordering
//
// Must run AFTER reindexSessions (telemetry is the richer source and wins) and
// BEFORE collectSessionIDs and purgeStaleEntries — the purge judges targets
// against validIDs as it stands at that moment, so an id registered afterwards
// is one the purge has already condemned.
//
// # Insert-if-absent
//
// A session with live telemetry already has a row carrying real event counts, a
// generated title, and a status the titler and reapers maintain. Overwriting it
// with the ledger's four fields would be a downgrade, so the projection only
// fills gaps. That also makes it idempotent across repeated reindexes.
//
// Returns the number of rows inserted.
func reindexSessionLedger(database *sql.DB, wipnoteDir string, verbose bool) int {
	if database == nil {
		return 0
	}
	records, err := sessionledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		if verbose {
			fmt.Printf("reindex session ledger: %v\n", err)
		}
		return 0
	}

	inserted := 0
	for _, r := range records {
		// The shape check is the gate, not a formality: an id that is not
		// session-shaped would still register in validIDs through the sessions
		// table and silently make an arbitrary token a valid edge target. The
		// ledger writer refuses these too — this is the second line.
		if !graph.IsSessionShapedID(r.SessionID) {
			continue
		}

		status := "active"
		var completedAt any
		if !r.IsOpen() {
			status = "completed"
			completedAt = sessionledger.FormatTime(r.EndedAt)
		}

		res, execErr := database.Exec(`
			INSERT OR IGNORE INTO sessions
				(session_id, agent_assigned, created_at, completed_at, status,
				 title, harness, project_dir, total_events)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.SessionID, "session", sessionledger.FormatTime(r.StartedAt), completedAt,
			status, r.Label(), r.Harness, r.ProjectDir, r.Events,
		)
		if execErr != nil {
			if verbose {
				fmt.Printf("reindex session ledger: %s: %v\n", r.SessionID, execErr)
			}
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted
}
