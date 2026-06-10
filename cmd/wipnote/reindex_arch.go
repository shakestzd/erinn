package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/arch"
	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// reindexArchCards ingests all arch cards from .wipnote/arch/*.md into the
// arch_cards SQLite read index. Returns (total, upserted, errCount).
// This is a full reindex — the table is a derived read index, never authoritative.
func reindexArchCards(database *sql.DB, wipnoteDir string, verbose bool) (int, int, int) {
	archDir := filepath.Join(wipnoteDir, "arch")
	entries, err := os.ReadDir(archDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, 0
		}
		if verbose {
			fmt.Printf("reindex arch: read dir error: %s: %v\n", archDir, err)
		}
		return 0, 0, 1
	}

	var total, upserted, errCount int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		total++
		path := filepath.Join(archDir, e.Name())
		card, parseErr := arch.ParseFile(path)
		if parseErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex arch: error: %s: %v\n", e.Name(), parseErr)
			}
			continue
		}

		var createdAt, updatedAt *time.Time
		if !card.CreatedAt.IsZero() {
			t := card.CreatedAt
			createdAt = &t
		}
		if !card.UpdatedAt.IsZero() {
			t := card.UpdatedAt
			updatedAt = &t
		}

		row, rowErr := dbpkg.ArchCardRowFromFields(
			card.Name,
			string(card.Kind),
			card.Paths,
			card.VerifiedAt,
			card.Links,
			card.CreatedBy,
			card.SupersededBy,
			card.Retired,
			card.Body,
			createdAt,
			updatedAt,
		)
		if rowErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex arch: marshal error: %s: %v\n", e.Name(), rowErr)
			}
			continue
		}
		if upsertErr := dbpkg.UpsertArchCard(database, row); upsertErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex arch: upsert error: %s: %v\n", e.Name(), upsertErr)
			}
			continue
		}
		upserted++
	}
	return total, upserted, errCount
}
