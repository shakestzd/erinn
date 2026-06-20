package blocks_test

import (
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/blocks"
)

func TestBlocks_Diagram_Horizontal(t *testing.T) {
	d := &blocks.Diagram{Title: "Pipeline", Steps: []string{"Collect", "Render", "Commit"}}
	html := render(t, d)
	for _, want := range []string{
		`class="block block-diagram"`,
		"diagram-lr", // default direction
		"Pipeline",
		"Collect", "Render", "Commit",
		"diagram-node",
		"diagram-arrow", // arrows between steps
	} {
		if !strings.Contains(html, want) {
			t.Errorf("diagram missing %q\n%s", want, html)
		}
	}
	// 3 steps → 2 arrows.
	if n := strings.Count(html, "diagram-arrow"); n != 2 {
		t.Errorf("expected 2 arrows for 3 steps, got %d", n)
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
