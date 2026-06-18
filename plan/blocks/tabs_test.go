package blocks_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/blocks"
)

func TestBlocks_Tabs(t *testing.T) {
	tb := &blocks.Tabs{Title: "Modes", Tabs: []blocks.Tab{
		{Label: "Unified", Body: "stacked diff"},
		{Label: "Split", Body: "side by side"},
	}}
	html := render(t, tb)
	for _, want := range []string{
		`class="block block-tabs"`,
		"Modes",
		"Unified", "Split", // labels
		"stacked diff", "side by side", // panels
		"tab-radio", "tab-label", "tab-panel",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("tabs missing %q\n%s", want, html)
		}
	}
	// Exactly one radio is checked by default (the first).
	if n := strings.Count(html, " checked"); n != 1 {
		t.Errorf("expected exactly one checked tab, got %d", n)
	}
	// No JS — pure CSS.
	if strings.Contains(strings.ToLower(html), "<script") || strings.Contains(html, "onclick") {
		t.Errorf("tabs must be JS-free:\n%s", html)
	}
}

func TestBlocks_Tabs_DistinctGroups(t *testing.T) {
	// Two tabs blocks with different labels must get different radio group names
	// so they don't cross-toggle on the same page.
	a := render(t, &blocks.Tabs{Tabs: []blocks.Tab{{Label: "X", Body: "1"}}})
	b := render(t, &blocks.Tabs{Tabs: []blocks.Tab{{Label: "Y", Body: "2"}}})
	re := regexp.MustCompile(`name="(tabs-[a-z0-9]+)"`)
	ga, gb := re.FindStringSubmatch(a), re.FindStringSubmatch(b)
	if ga == nil || gb == nil {
		t.Fatalf("could not find radio group names")
	}
	if ga[1] == gb[1] {
		t.Errorf("distinct tabs blocks must have distinct group names, both = %q", ga[1])
	}
}

func TestBlocks_Tabs_SeedDisambiguatesIdenticalLabels(t *testing.T) {
	// Two tabs blocks with IDENTICAL labels must still get distinct radio groups
	// when given distinct seeds (e.g. their slice block anchors), so selecting a
	// tab in one block doesn't toggle the other.
	labels := []blocks.Tab{{Label: "A", Body: "1"}, {Label: "B", Body: "2"}}
	a := render(t, &blocks.Tabs{Tabs: labels, Seed: "slice-1-block-tabs-1"})
	b := render(t, &blocks.Tabs{Tabs: labels, Seed: "slice-1-block-tabs-2"})
	re := regexp.MustCompile(`name="(tabs-[a-z0-9]+)"`)
	ga, gb := re.FindStringSubmatch(a), re.FindStringSubmatch(b)
	if ga == nil || gb == nil {
		t.Fatalf("could not find group names")
	}
	if ga[1] == gb[1] {
		t.Errorf("same-labelled tabs with distinct seeds must differ, both = %q", ga[1])
	}
}

func TestBlocks_Tabs_EscapesContent(t *testing.T) {
	tb := &blocks.Tabs{Tabs: []blocks.Tab{{Label: `<b>x</b>`, Body: `<script>bad</script>`}}}
	html := render(t, tb)
	if strings.Contains(html, "<script>bad</script>") || strings.Contains(html, "<b>x</b>") {
		t.Errorf("tab label/body must be escaped:\n%s", html)
	}
}
