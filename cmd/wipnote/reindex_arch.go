package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/arch"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// reindexArchCards ingests the canonical .wipnote/architecture.html ledger
// plus legacy/import .wipnote/arch/*.{yaml,md} cards into the arch_cards
// SQLite read index and derives arch→work-item lineage edges. Returns
// (total, upserted, errCount). This is a full reindex: rows for
// deleted/renamed cards are purged so the table stays consistent with the
// canonical ledger.
func reindexArchCards(database *sql.DB, wipnoteDir string, verbose bool) (int, int, int) {
	ledgerCards, ledgerErr := loadLedgerCardsForReindex(wipnoteDir, verbose)
	archDir := filepath.Join(wipnoteDir, "arch")
	entries, err := os.ReadDir(archDir)
	if err != nil {
		if os.IsNotExist(err) {
			if len(ledgerCards) == 0 {
				// No legacy dir and no ledger — purge any stale rows.
				_, _ = database.Exec(`DELETE FROM arch_cards`)
				_, _ = database.Exec(`DELETE FROM graph_edges WHERE from_node_type = 'arch' OR to_node_type = 'arch'`)
				if ledgerErr != nil {
					return 0, 0, 1
				}
				return 0, 0, 0
			}
			entries = nil
		}
		if err != nil && !os.IsNotExist(err) && verbose {
			fmt.Printf("reindex arch: read dir error: %s: %v\n", archDir, err)
		}
		if err != nil && !os.IsNotExist(err) {
			if ledgerErr != nil {
				return 0, 0, 2
			}
			return 0, 0, 1
		}
	}

	// Collect the set of slugs present in legacy/import file storage.
	diskSlugs := make(map[string]struct{})
	selected := make(map[string]string)
	errCount := 0
	if ledgerErr != nil {
		errCount++
	}
	_, _ = database.Exec(`DELETE FROM graph_edges WHERE from_node_type = 'arch' OR to_node_type = 'arch'`)

	for _, e := range entries {
		if e.IsDir() || !isArchCardFile(e.Name()) {
			continue
		}
		path := filepath.Join(archDir, e.Name())
		slug := archCardSlug(e.Name())
		existing, ok := selected[slug]
		if ok && filepath.Ext(existing) == ".yaml" {
			continue
		}
		if filepath.Ext(path) == ".yaml" || !ok {
			selected[slug] = path
		}
	}

	cards := make(map[string]*arch.Card, len(ledgerCards)+len(selected))
	for slug, card := range ledgerCards {
		cards[slug] = card
		diskSlugs[slug] = struct{}{}
	}
	for slug, path := range selected {
		if _, exists := cards[slug]; exists {
			continue
		}
		card, parseErr := arch.ParseFile(path)
		if parseErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex arch: error: %s: %v\n", filepath.Base(path), parseErr)
			}
			continue
		}
		cards[slug] = card
		diskSlugs[slug] = struct{}{}
	}

	slugs := make([]string, 0, len(cards))
	for slug := range cards {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	totalSources := len(ledgerCards) + len(selected)

	upserted := 0
	for _, slug := range slugs {
		card := cards[slug]
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
				fmt.Printf("reindex arch: marshal error: %s: %v\n", card.Name, rowErr)
			}
			continue
		}
		if upsertErr := dbpkg.UpsertArchCard(database, row); upsertErr != nil {
			errCount++
			if verbose {
				fmt.Printf("reindex arch: upsert error: %s: %v\n", card.Name, upsertErr)
			}
			continue
		}
		for _, link := range card.Links {
			toType := archLinkNodeType(link)
			if toType == "" {
				continue
			}
			archNodeID := arch.ArchNodeID(card.Name)
			learnedEdgeID := fmt.Sprintf("%s-%s-%s", archNodeID, models.RelLearnedFrom, link)
			if edgeErr := dbpkg.InsertEdge(
				database,
				learnedEdgeID,
				archNodeID,
				"arch",
				link,
				toType,
				string(models.RelLearnedFrom),
				nil,
			); edgeErr != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex arch: edge upsert error: %s -> %s: %v\n", card.Name, link, edgeErr)
				}
			}
			hasLearningEdgeID := fmt.Sprintf("%s-%s-%s", link, models.RelHasLearning, archNodeID)
			if edgeErr := dbpkg.InsertEdge(
				database,
				hasLearningEdgeID,
				link,
				toType,
				archNodeID,
				"arch",
				string(models.RelHasLearning),
				nil,
			); edgeErr != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex arch: reverse edge upsert error: %s -> %s: %v\n", link, card.Name, edgeErr)
				}
			}
		}
		upserted++
	}

	// Purge rows for cards that no longer exist in the canonical ledger or
	// legacy/import file storage.
	purgeStaleArchCards(database, diskSlugs, verbose)

	return totalSources, upserted, errCount
}

func isArchCardFile(name string) bool {
	ext := filepath.Ext(name)
	return ext == ".yaml" || ext == ".md"
}

func archCardSlug(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func archLinkNodeType(id string) string {
	switch {
	case strings.HasPrefix(id, "feat-"):
		return "feature"
	case strings.HasPrefix(id, "bug-"):
		return "bug"
	case strings.HasPrefix(id, "spk-"):
		return "spike"
	case strings.HasPrefix(id, "trk-"):
		return "track"
	case strings.HasPrefix(id, "plan-"):
		return "plan"
	case strings.HasPrefix(id, "spec-"), strings.HasPrefix(id, "spc-"):
		return "spec"
	default:
		return ""
	}
}

func loadLedgerCardsForReindex(wipnoteDir string, verbose bool) (map[string]*arch.Card, error) {
	cards := make(map[string]*arch.Card)
	ledgerPath := arch.LedgerPath(wipnoteDir)
	ledgerCards, err := arch.ReadLedger(ledgerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cards, nil
		}
		if verbose {
			fmt.Printf("reindex arch: read ledger error: %s: %v\n", ledgerPath, err)
		}
		return cards, err
	}
	for _, card := range ledgerCards {
		cards[card.Name] = card
	}
	return cards, nil
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
