package projection

import (
	"testing"

	"github.com/shakestzd/wipnote/core/arch"
)

// TestExecuteDSLSupportsArchNodeType is the regression for defect 3
// (feat-fc3cc9e0): core/graph/dsl.go has always treated "arch" as a
// first-class node type served from canonical HTML
// (.wipnote/architecture.html), but core/projection/dsl.go's
// normalizeNodeType had no "arch" case, so `wipnote query 'arch[...] -> ...'`
// regressed to "unsupported node type \"arch\"" once query.go moved onto the
// projection. This seeds a real architecture card via arch.Store — the same
// canonical ledger `wipnote arch` commands use — and asserts both the bare
// type selector and a kind= filter resolve it.
func TestExecuteDSLSupportsArchNodeType(t *testing.T) {
	wipnoteDir := newProject(t)
	store, err := arch.NewStore(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}
	card := &arch.Card{
		Name:      "query-dsl-arch",
		Kind:      arch.KindDecision,
		CreatedBy: "agent",
		Body:      "Architecture cards are first-class query targets.",
	}
	if err := store.Create(card); err != nil {
		t.Fatal(err)
	}

	snap, err := Load(wipnoteDir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := snap.ExecuteDSL("arch")
	if err != nil {
		t.Fatalf("query error: %v", err)
	}
	if len(got) != 1 || got[0].ID != arch.ArchNodeID(card.Name) {
		t.Fatalf("arch query = %#v, want single result %s", got, arch.ArchNodeID(card.Name))
	}

	filtered, err := snap.ExecuteDSL("arch[kind=decision]")
	if err != nil {
		t.Fatalf("kind-filtered query error: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != arch.ArchNodeID(card.Name) {
		t.Fatalf("arch kind filter = %#v, want single result %s", filtered, arch.ArchNodeID(card.Name))
	}

	none, err := snap.ExecuteDSL("arch[kind=hazard]")
	if err != nil {
		t.Fatalf("non-matching kind filter error: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("arch kind=hazard filter = %#v, want no results", none)
	}
}
