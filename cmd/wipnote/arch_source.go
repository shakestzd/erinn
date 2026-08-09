package main

import (
	"sort"
	"time"

	"github.com/shakestzd/wipnote/core/arch"
	"github.com/shakestzd/wipnote/core/graph"
)

// archSourceFor returns the canonical architecture-card store for a .wipnote
// directory, for use as a graph.ArchSource.
//
// Graph queries resolve arch nodes through this store rather than the
// arch_cards SQLite table. The store reads .wipnote/architecture.html, which
// is the same path every `wipnote arch` command already takes, so it is the
// proven reader — the SQLite mirror was a second copy with a reindex-time
// sync obligation and no reader that needed it (spk-e6e82b5a).
//
// Returns nil when the store cannot be opened. A nil graph.ArchSource means
// arch nodes resolve to bare IDs, which matches how the SQL path behaved when
// the table was missing: degrade the labels, do not fail the traversal.
func archSourceFor(wipnoteDir string) graph.ArchSource {
	store, err := arch.NewStore(wipnoteDir)
	if err != nil {
		return nil
	}
	return store
}

// archGraphNodeCap bounds how many architecture cards the dashboard graph
// shows, matching the LIMIT the arch_cards query used.
const archGraphNodeCap = 500

// archGraphNodes returns architecture cards as dashboard graph nodes, newest
// first then by slug, capped at archGraphNodeCap.
//
// Ordering and cap reproduce the retired SQL:
//
//	ORDER BY COALESCE(updated_at, created_at, indexed_at) DESC, slug LIMIT 500
//
// indexed_at has no analogue outside the mirror — it recorded when the row was
// written, not anything about the card — so cards with neither timestamp sort
// last and tie-break on slug.
func archGraphNodes(src graph.ArchSource) []graphNode {
	if src == nil {
		return nil
	}
	cards, err := src.List(true)
	if err != nil {
		return nil
	}

	sort.Slice(cards, func(i, j int) bool {
		ti, tj := archCardSortTime(cards[i]), archCardSortTime(cards[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return cards[i].Name < cards[j].Name
	})

	out := make([]graphNode, 0, len(cards))
	for _, c := range cards {
		if len(out) >= archGraphNodeCap {
			break
		}
		status := "active"
		if c.Retired || c.SupersededBy != "" {
			status = "retired"
		}
		out = append(out, graphNode{
			ID:     arch.ArchNodeID(c.Name),
			Type:   "arch",
			Title:  c.Name,
			Status: status,
			Kind:   string(c.Kind),
		})
	}
	return out
}

// archCardSortTime is the card's recency key: updated_at, else created_at.
func archCardSortTime(c *arch.Card) time.Time {
	if !c.UpdatedAt.IsZero() {
		return c.UpdatedAt
	}
	return c.CreatedAt
}
