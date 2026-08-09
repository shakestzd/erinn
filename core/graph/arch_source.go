package graph

import (
	"sort"

	corearch "github.com/shakestzd/wipnote/core/arch"
)

// ArchSource supplies architecture memory cards to graph queries.
//
// Architecture cards are canonically stored in .wipnote/architecture.html.
// core/arch.Store reads that ledger (plus any legacy/import cards under
// .wipnote/arch/) and is what every `wipnote arch` command already uses, so it
// satisfies this interface directly. graph reads cards through here rather
// than from the arch_cards SQLite table: that table was a second copy of data
// the canonical store already serves, with its own reindex-time sync
// obligation and nothing to show for it (spk-e6e82b5a).
//
// A nil ArchSource is valid and means "no architecture cards are visible".
// Callers that cannot encounter arch nodes, and tests that do not exercise
// them, pass nil rather than constructing a store.
type ArchSource interface {
	List(includeRetired bool) ([]*corearch.Card, error)
}

// archLookup is a per-query view over an ArchSource.
//
// Cards come from parsing one HTML ledger, and a single DSL execution can
// consult them at three separate points (type selector, selector filter, node
// resolution). Loading once per lookup keeps that at one parse instead of
// three — the same redundant-read shape as bug-1a51ab15.
type archLookup struct {
	src    ArchSource
	loaded bool
	bySlug map[string]*corearch.Card
	slugs  []string // sorted, for deterministic result ordering
}

func newArchLookup(src ArchSource) *archLookup {
	return &archLookup{src: src}
}

// load parses the card set on first use. Errors are swallowed deliberately:
// the SQL this replaced treated a missing or unreadable arch_cards table as
// "no arch nodes" rather than failing the whole query, and every caller here
// is a traversal that can legitimately return nothing.
func (a *archLookup) load() {
	if a.loaded {
		return
	}
	a.loaded = true
	a.bySlug = map[string]*corearch.Card{}
	if a.src == nil {
		return
	}
	cards, err := a.src.List(true)
	if err != nil {
		return
	}
	for _, c := range cards {
		if c == nil || c.Name == "" {
			continue
		}
		a.bySlug[c.Name] = c
		a.slugs = append(a.slugs, c.Name)
	}
	sort.Strings(a.slugs)
}

// allSlugs returns every known card slug in sorted order.
func (a *archLookup) allSlugs() []string {
	a.load()
	return a.slugs
}

// get returns the card for a slug, or nil.
func (a *archLookup) get(slug string) *corearch.Card {
	a.load()
	return a.bySlug[slug]
}

// nodeResult renders a card as a graph NodeResult, mirroring the columns the
// arch_cards SELECT used to project.
func archNodeResult(c *corearch.Card) NodeResult {
	return NodeResult{
		ID:     corearch.ArchNodeID(c.Name),
		Type:   "arch",
		Title:  c.Name,
		Status: archCardStatus(c),
	}
}

// archCardStatus reproduces the derived status the SQL read path computed as
//
//	CASE WHEN retired = 1 OR COALESCE(superseded_by,'') != ''
//	     THEN 'retired' ELSE 'active' END
func archCardStatus(c *corearch.Card) string {
	if c.Retired || c.SupersededBy != "" {
		return "retired"
	}
	return "active"
}

// archFieldValue resolves a whitelisted DSL filter field against a card.
// Fields are the keys of typeFilterColumns["arch"].
func archFieldValue(c *corearch.Card, field string) (string, bool) {
	switch field {
	case "status":
		return archCardStatus(c), true
	case "kind":
		return string(c.Kind), true
	case "created_by":
		return c.CreatedBy, true
	default:
		return "", false
	}
}

// archSelect returns the arch node IDs whose field matches value. An empty
// field selects every card. Results are sorted by slug.
func (a *archLookup) archSelect(field, value string) []string {
	var out []string
	for _, slug := range a.allSlugs() {
		c := a.get(slug)
		if field != "" {
			got, ok := archFieldValue(c, field)
			if !ok || got != value {
				continue
			}
		}
		out = append(out, corearch.ArchNodeID(c.Name))
	}
	return out
}

// archFilter narrows an existing ID set to the arch nodes matching
// field=value, preserving the caller's ordering.
func (a *archLookup) archFilter(ids []string, field, value string) []string {
	var out []string
	for _, id := range ids {
		slug, ok := corearch.ArchSlugFromNodeID(id)
		if !ok {
			continue
		}
		c := a.get(slug)
		if c == nil {
			continue
		}
		if field != "" {
			got, fieldOK := archFieldValue(c, field)
			if !fieldOK || got != value {
				continue
			}
		}
		out = append(out, corearch.ArchNodeID(c.Name))
	}
	return out
}
