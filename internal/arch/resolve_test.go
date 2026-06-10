package arch_test

import (
	"strings"
	"testing"

	corearch "github.com/shakestzd/wipnote/core/arch"
	"github.com/shakestzd/wipnote/internal/arch"
)

// --- glob matching ---

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		glob  string
		path  string
		match bool
	}{
		// Basic exact matches
		{"core/arch/card.go", "core/arch/card.go", true},
		// Wildcard single segment
		{"core/*.go", "core/foo.go", true},
		{"core/*.go", "core/sub/foo.go", false},
		// FALSE POSITIVE guard: core/* must not match corey/x.go
		{"core/*", "corey/x.go", false},
		// Double star
		{"core/**", "core/arch/card.go", true},
		{"core/**", "core/x.go", true},
		// Double star prefix
		{"**/card.go", "core/arch/card.go", true},
		{"**/card.go", "card.go", true},
		// Prefix + double star + suffix
		{"internal/**/resolve.go", "internal/arch/resolve.go", true},
		{"internal/**/resolve.go", "internal/arch/sub/resolve.go", true},
		{"internal/**/resolve.go", "cmd/arch/resolve.go", false},
		// No match
		{"cmd/**", "internal/foo.go", false},
	}
	for _, tc := range cases {
		got := arch.GlobMatch(tc.glob, tc.path)
		if got != tc.match {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", tc.glob, tc.path, got, tc.match)
		}
	}
}

// --- kind priority ordering ---

func makeCard(name string, kind corearch.Kind) *corearch.Card {
	return &corearch.Card{
		Name:      name,
		Kind:      kind,
		CreatedBy: "test",
		Body:      "test body",
		Paths:     []string{"**"},
	}
}

func TestKindOrder(t *testing.T) {
	cards := []*corearch.Card{
		makeCard("d", corearch.KindDecision),
		makeCard("s", corearch.KindSubsystemMap),
		makeCard("h", corearch.KindHazard),
		makeCard("i", corearch.KindInvariant),
	}
	arch.SortByKindPriority(cards)
	want := []string{"h", "i", "s", "d"}
	for i, c := range cards {
		if c.Name != want[i] {
			t.Errorf("position %d: got %s, want %s", i, c.Name, want[i])
		}
	}
}

// --- budget truncation ---

func makeBodyCard(name string, kind corearch.Kind, body string) *corearch.Card {
	return &corearch.Card{
		Name:      name,
		Kind:      kind,
		CreatedBy: "test",
		Body:      body,
		Paths:     []string{"**"},
	}
}

func TestBudgetTruncation(t *testing.T) {
	// Each card body is 5 words. Budget=10 allows 2 cards.
	fiveWords := "one two three four five"
	cards := []*corearch.Card{
		makeBodyCard("h1", corearch.KindHazard, fiveWords),
		makeBodyCard("h2", corearch.KindHazard, fiveWords),
		makeBodyCard("h3", corearch.KindHazard, fiveWords),
	}

	result := arch.ApplyBudget(cards, 10)
	if len(result.Emitted) != 2 {
		t.Fatalf("expected 2 emitted cards, got %d", len(result.Emitted))
	}
	if result.Omitted != 1 {
		t.Errorf("expected 1 omitted, got %d", result.Omitted)
	}
}

func TestBudgetNeverSplitsCard(t *testing.T) {
	// Budget=3 — not enough for any card with 5-word body. First card must still be emitted.
	// The spec says "emit whole cards … until next card would exceed budget" so the first card
	// always emits (otherwise zero output on tight budget).
	fiveWords := "one two three four five"
	cards := []*corearch.Card{
		makeBodyCard("h1", corearch.KindHazard, fiveWords),
		makeBodyCard("h2", corearch.KindHazard, fiveWords),
	}
	result := arch.ApplyBudget(cards, 3)
	// First card always emits; second omitted.
	if len(result.Emitted) != 1 {
		t.Fatalf("expected 1 emitted card, got %d", len(result.Emitted))
	}
	if result.Omitted != 1 {
		t.Errorf("expected 1 omitted, got %d", result.Omitted)
	}
}

func TestBudgetAllFit(t *testing.T) {
	fiveWords := "one two three four five"
	cards := []*corearch.Card{
		makeBodyCard("h1", corearch.KindHazard, fiveWords),
		makeBodyCard("h2", corearch.KindHazard, fiveWords),
	}
	result := arch.ApplyBudget(cards, 1000)
	if len(result.Emitted) != 2 {
		t.Fatalf("expected 2 emitted cards, got %d", len(result.Emitted))
	}
	if result.Omitted != 0 {
		t.Errorf("expected 0 omitted, got %d", result.Omitted)
	}
}

// --- render output format ---

func TestRenderCard(t *testing.T) {
	card := makeCard("my-subsystem", corearch.KindSubsystemMap)
	card.Body = "This describes the subsystem layout."
	card.VerifiedAt = "abc1234"

	out := arch.RenderCard(card, "")
	if !strings.Contains(out, "[subsystem-map]") {
		t.Errorf("output missing kind: %q", out)
	}
	if !strings.Contains(out, "my-subsystem") {
		t.Errorf("output missing name: %q", out)
	}
	if !strings.Contains(out, "This describes") {
		t.Errorf("output missing body: %q", out)
	}
}

func TestRenderCardDrift(t *testing.T) {
	card := makeCard("my-hazard", corearch.KindHazard)
	card.Body = "Watch out."
	card.VerifiedAt = "deadbeef1234"

	out := arch.RenderCard(card, "deadbeef")
	if !strings.Contains(out, "UNVERIFIED since deadbeef") {
		t.Errorf("drift prefix missing: %q", out)
	}
}

func TestRenderCardNoVerifiedAt(t *testing.T) {
	card := makeCard("my-decision", corearch.KindDecision)
	card.Body = "We chose X."
	card.VerifiedAt = ""

	out := arch.RenderCard(card, "")
	if !strings.Contains(out, "UNVERIFIED") {
		t.Errorf("card with empty verified_at should render as UNVERIFIED: %q", out)
	}
}

// --- batched drift: unique verified_at grouping ---

func TestGroupByVerifiedAt(t *testing.T) {
	cards := []*corearch.Card{
		{Name: "a", VerifiedAt: "sha1"},
		{Name: "b", VerifiedAt: "sha1"},
		{Name: "c", VerifiedAt: "sha2"},
		{Name: "d", VerifiedAt: ""},
	}
	groups := arch.GroupByVerifiedAt(cards)
	// Should produce 2 non-empty SHA groups + 1 empty group.
	if len(groups["sha1"]) != 2 {
		t.Errorf("expected 2 cards for sha1, got %d", len(groups["sha1"]))
	}
	if len(groups["sha2"]) != 1 {
		t.Errorf("expected 1 card for sha2, got %d", len(groups["sha2"]))
	}
	if len(groups[""]) != 1 {
		t.Errorf("expected 1 card for empty SHA, got %d", len(groups["sha2"]))
	}
}

// --- match cards against paths ---

func TestMatchCardsForPaths(t *testing.T) {
	cards := []*corearch.Card{
		{Name: "core-arch", Kind: corearch.KindSubsystemMap, CreatedBy: "test",
			Body: "body", Paths: []string{"core/arch/**"}},
		{Name: "cmd-only", Kind: corearch.KindDecision, CreatedBy: "test",
			Body: "body", Paths: []string{"cmd/**"}},
		{Name: "no-paths", Kind: corearch.KindHazard, CreatedBy: "test",
			Body: "body", Paths: nil},
	}
	paths := []string{"core/arch/card.go", "core/arch/store.go"}
	matched := arch.MatchCards(cards, paths)
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched card, got %d", len(matched))
	}
	if matched[0].Name != "core-arch" {
		t.Errorf("wrong card matched: %s", matched[0].Name)
	}
}

func TestMatchCardsRetiredHidden(t *testing.T) {
	cards := []*corearch.Card{
		{Name: "active", Kind: corearch.KindHazard, CreatedBy: "test",
			Body: "body", Paths: []string{"**"}},
		{Name: "retired", Kind: corearch.KindHazard, CreatedBy: "test",
			Body: "body", Paths: []string{"**"}, Retired: true},
		{Name: "superseded", Kind: corearch.KindHazard, CreatedBy: "test",
			Body: "body", Paths: []string{"**"}, SupersededBy: "active"},
	}
	paths := []string{"any/file.go"}
	matched := arch.MatchCards(cards, paths)
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched card (active only), got %d: %v", len(matched), cardNames(matched))
	}
	if matched[0].Name != "active" {
		t.Errorf("wrong card: %s", matched[0].Name)
	}
}

// --- sentinel line format ---

func TestFormatOutput_Sentinel(t *testing.T) {
	// Budget=6 allows only 1 five-word card; the second is omitted.
	fiveWords := "one two three four five"
	cards := []*corearch.Card{
		makeBodyCard("h1", corearch.KindHazard, fiveWords),
		makeBodyCard("h2", corearch.KindHazard, fiveWords),
	}

	driftMap := map[string]string{}
	out := arch.FormatOutput(cards, 6, driftMap)
	if !strings.Contains(out, "(1 card omitted by budget)") {
		t.Errorf("sentinel line missing or wrong: %q", out)
	}
}

func TestFormatOutput_NoSentinel(t *testing.T) {
	card := makeBodyCard("h1", corearch.KindHazard, "one two three")
	out := arch.FormatOutput([]*corearch.Card{card}, 1000, map[string]string{})
	if strings.Contains(out, "omitted") {
		t.Errorf("should not have sentinel when no omissions: %q", out)
	}
}

// --- work-item ID detection ---

func TestLooksLikeWorkItemID(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"feat-256d5099", true},
		{"bug-abc12345", true},
		{"spk-deadbeef", true},
		{"spike-deadbeef", false},
		{"feat-xyz", false},
		{"internal/arch/resolve.go", false},
		{"core/arch/card.go", false},
	}
	for _, tc := range cases {
		got := arch.LooksLikeWorkItemID(tc.s)
		if got != tc.want {
			t.Errorf("LooksLikeWorkItemID(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

func cardNames(cards []*corearch.Card) []string {
	var names []string
	for _, c := range cards {
		names = append(names, c.Name)
	}
	return names
}
