package blocks_test

import (
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/blocks"
	"github.com/shakestzd/wipnote/plan/blocks/svg"
)

func TestBlocks_Diagram_Horizontal(t *testing.T) {
	d := &blocks.Diagram{Title: "Pipeline", Steps: []string{"Collect", "Render", "Commit"}}
	html := render(t, d)
	for _, want := range []string{
		`class="block block-diagram"`,
		"diagram-lr", // default direction
		"Pipeline",
		"Collect", "Render", "Commit",
		"<svg",
		"<rect",
		"<path",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("diagram missing %q\n%s", want, html)
		}
	}
	// 3 steps → 2 drawn edges (plus the shared arrowhead's own triangle path,
	// which is why this counts the diagram-edge class, not "<path" overall).
	if n := strings.Count(html, "diagram-edge"); n != 2 {
		t.Errorf("expected 2 drawn edges for 3 steps, got %d\n%s", n, html)
	}
}

func TestBlocks_Diagram_Vertical(t *testing.T) {
	d := &blocks.Diagram{Steps: []string{"A", "B"}, Direction: "tb"}
	html := render(t, d)
	if !strings.Contains(html, "diagram-tb") {
		t.Errorf("expected vertical flow class:\n%s", html)
	}
	if strings.Contains(html, "diagram-lr") {
		t.Errorf("vertical diagram must not be marked lr:\n%s", html)
	}
}

func TestBlocks_Diagram_EscapesSteps(t *testing.T) {
	d := &blocks.Diagram{Steps: []string{`<script>alert(1)</script>`}}
	html := render(t, d)
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Errorf("step content must be escaped:\n%s", html)
	}
}

func TestBlocks_Diagram_NoSteps(t *testing.T) {
	d := &blocks.Diagram{Steps: nil}
	html := render(t, d)
	if !strings.Contains(html, `class="block block-diagram"`) {
		t.Errorf("expected diagram wrapper even with no steps:\n%s", html)
	}
	if strings.Contains(html, "<svg") {
		t.Errorf("expected no svg canvas for an empty diagram:\n%s", html)
	}
}

// TestDiagram_DrawsEdges is the slice-9 (feat-47793a68) contract: a diagram's
// edges must be drawn <path> elements terminated in the shared arrowhead
// marker from plan/blocks/svg — never the old Unicode arrow glyph.
func TestDiagram_DrawsEdges(t *testing.T) {
	d := &blocks.Diagram{Steps: []string{"Collect", "Render", "Commit"}}
	html := render(t, d)

	if !strings.Contains(html, "<path") {
		t.Fatalf("expected drawn <path> edges:\n%s", html)
	}
	wantMarkerRef := `marker-end="url(#` + svg.ArrowheadMarkerID + `)"`
	if !strings.Contains(html, wantMarkerRef) {
		t.Errorf("expected edges to reference the shared arrowhead marker %q:\n%s", wantMarkerRef, html)
	}
	if n := strings.Count(html, "<marker"); n != 1 {
		t.Errorf("expected exactly one shared marker definition, found %d:\n%s", n, html)
	}
	for _, glyph := range []string{"&#8594;", "&#8595;", "\u2192", "\u2193"} {
		if strings.Contains(html, glyph) {
			t.Errorf("expected no arrow glyph characters, found %q:\n%s", glyph, html)
		}
	}
	// 3 steps -> 2 drawn edges (the marker triangle is also a <path, so this
	// counts the diagram-edge class rather than "<path" overall).
	if n := strings.Count(html, "diagram-edge"); n != 2 {
		t.Errorf("expected 2 drawn edges for 3 steps, got %d\n%s", n, html)
	}
}
