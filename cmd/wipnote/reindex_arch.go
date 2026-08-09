package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/core/arch"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// reindexArchCards reads the canonical .wipnote/architecture.html ledger plus
// legacy/import .wipnote/arch/*.{yaml,md} cards and derives the arch→work-item
// lineage edges. Returns (total, edgesFor, errCount).
//
// It no longer mirrors card content into the arch_cards SQLite table. Every
// reader now goes to core/arch.Store, which reads the same canonical ledger
// and is the path `wipnote arch` has always used; the mirror was a second copy
// carrying a sync obligation for no reader that needed it (spk-e6e82b5a). The
// table is left defined in the schema and its repository functions are left in
// core/db so restoring the mirror is a matter of re-adding the upsert call.
// Existing rows are cleared once so a stale mirror cannot be mistaken for live
// data by anything that was missed.
func reindexArchCards(database *sql.DB, wipnoteDir string, verbose bool) (int, int, int) {
	ledgerCards, ledgerErr := loadLedgerCardsForReindex(wipnoteDir, verbose)
	archDir := filepath.Join(wipnoteDir, "arch")
	entries, err := os.ReadDir(archDir)
	if err != nil {
		if os.IsNotExist(err) {
			if len(ledgerCards) == 0 {
				// No legacy dir and no ledger — clear the retired mirror and
				// the derived edges.
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
	// The arch_cards mirror is no longer written; clear it so nothing reads
	// stale card content, and rebuild the derived edges from scratch.
	_, _ = database.Exec(`DELETE FROM arch_cards`)
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

	edgesFor := 0
	for _, slug := range slugs {
		card := cards[slug]
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
		edgesFor++
	}

	return totalSources, edgesFor, errCount
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

