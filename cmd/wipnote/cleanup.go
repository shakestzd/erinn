package main

import (
	"github.com/spf13/cobra"
)

// cleanupCmd groups cleanup operations that respect the HTML-canonical
// invariant.
//
// `cleanup ghost-sessions` was removed in feat-fc3cc9e0. It deleted rows from
// the `sessions` table that had no HTML file and no messages/tool_calls/
// agent_events. That table is now a per-process projection hydrated from the
// canonical session ledger on every openDB, so the DELETE reached a throwaway
// database and reindexSessionLedger re-inserted every "deleted" row on the next
// command. Run twice, it reported the same deletions both times. There is no
// canonical redirect either: a session's canonical record IS its ledger entry,
// so a row with no HTML but a live ledger entry is not a ghost — it is a
// session whose HTML has not been rendered, and deleting the ledger entry would
// destroy real provenance rather than tidy up after it.
//
// `cleanup orphan-sessions` stays and is unaffected: it deletes NDJSON
// directories from disk, which is a durable filesystem effect, and it consults
// the projection only to ask which session dirs have no corresponding session —
// a question the canonical ledger genuinely answers.
func cleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Cleanup operations that respect the HTML-canonical invariant",
	}
	cmd.AddCommand(cleanupOrphanSessionsCmd())
	return cmd
}
