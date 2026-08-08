package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shakestzd/wipnote/core/claimledger"
	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// reindexClaimEpisodes ingests the canonical claim ledger (.wipnote/claims/*.html,
// shards plus the archive) into the claim_episodes read index. Returns
// (files, upserted, errCount).
//
// This is a FULL pass every time, never incremental. Two reasons: the ledger is
// small (one file per root session, tens of rows each), and — decisively — the
// incremental path keys off git-changed files, while ledger writes are
// committed asynchronously through the commit queue. An episode written but not
// yet flushed would be invisible to a git-diff-driven pass, which is precisely
// the window in which attribution queries run.
//
// The purge before ingest is what keeps archiving honest: compaction deletes a
// shard and folds its rows into archive.html, so without a purge the read index
// would carry the episode twice under the old and new source_file.
func reindexClaimEpisodes(database *sql.DB, wipnoteDir string, verbose bool) (int, int, int) {
	store := claimledger.NewStore(wipnoteDir)
	files, err := store.Files()
	if err != nil {
		if verbose {
			fmt.Printf("reindex claims: list ledger files: %v\n", err)
		}
		return 0, 0, 1
	}

	if err := dbpkg.PurgeClaimEpisodes(database); err != nil {
		if verbose {
			fmt.Printf("reindex claims: purge: %v\n", err)
		}
		return len(files), 0, 1
	}

	upserted, errCount := 0, 0
	for _, path := range files {
		episodes, readErr := claimledger.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			errCount++
			if verbose {
				fmt.Printf("reindex claims: parse %s: %v\n", filepath.Base(path), readErr)
			}
			continue
		}
		source := store.RelPath(path)
		for _, e := range episodes {
			if upsertErr := dbpkg.UpsertClaimEpisode(database, dbpkg.ClaimEpisode{
				EpisodeID:     e.ID,
				WorkItemID:    e.WorkItemID,
				SessionID:     e.SessionID,
				RootSessionID: e.RootSessionID,
				AgentID:       e.AgentID,
				StartedAt:     e.StartedAt,
				EndedAt:       e.EndedAt,
				Outcome:       string(e.Outcome),
				SourceFile:    source,
			}); upsertErr != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex claims: upsert %s: %v\n", e.ID, upsertErr)
				}
				continue
			}
			upserted++
		}
	}
	return len(files), upserted, errCount
}
