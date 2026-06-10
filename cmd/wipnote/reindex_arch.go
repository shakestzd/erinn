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
// This is a full reindex: rows for deleted/renamed cards are purged so the
// table stays consistent with the canonical .md files.
func reindexArchCards(database *sql.DB, wipnoteDir string, verbose bool) (int, int, int) {
	archDir := filepath.Join(wipnoteDir, "arch")
	entries, err := os.ReadDir(archDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No arch dir — purge any stale rows.
			_, _ = database.Exec(`DELETE FROM arch_cards`)
			return 0, 0, 0
		}
		if verbose {
			fmt.Printf("reindex arch: read dir error: %s: %v\n", archDir, err)
		}
		return 0, 0, 1
	}

	// Collect the set of slugs present on disk.
	diskSlugs := make(map[string]struct{})
	var total, upserted, errCount int

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		total++
		slug := strings.TrimSuffix(e.Name(), ".md")
		path := filepath.Join(archDir, e.Name())
		card, parseErr := arch.ParseFile(path)
		if parseErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex arch: error: %s: %v\n", e.Name(), parseErr)
			}
			continue
		}
		diskSlugs[slug] = struct{}{}

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

	// Purge rows for cards that no longer exist on disk.
	purgeStaleArchCards(database, diskSlugs, verbose)

	return total, upserted, errCount
}

// purgeStaleArchCards deletes arch_cards rows whose slugs are not in diskSlugs.
func purgeStaleArchCards(database *sql.DB, diskSlugs map[string]struct{}, verbose bool) {
	rows, err := database.Query(`SELECT slug FROM arch_cards`)
	if err != nil {
		return
	}
	defer rows.Close()
	var stale []string
	for rows.Next() {
		var slug string
		if rows.Scan(&slug) == nil {
			if _, ok := diskSlugs[slug]; !ok {
				stale = append(stale, slug)
			}
		}
	}
	rows.Close()
	for _, slug := range stale {
		if _, err := database.Exec(`DELETE FROM arch_cards WHERE slug = ?`, slug); err == nil && verbose {
			fmt.Printf("reindex arch: purged stale row: %s\n", slug)
		}
	}
}
