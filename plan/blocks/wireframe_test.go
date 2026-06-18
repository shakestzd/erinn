package blocks_test

import (
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/blocks"
)

// These tests cover the wireframe block renderer (slice-7). They reuse the
// shared render() helper defined in blocks_test.go (same package).

func TestWireframe_TokensOnly(t *testing.T) {
	good := &blocks.Wireframe{
		Title: "After",
		Body:  `<div style="color:var(--wf-fg);background:var(--wf-surface)">Sidebar</div>`,
	}
	html := render(t, good)
	if good.RawColors() {
		t.Fatalf("token-only wireframe wrongly flagged as raw-color")
	}
	for _, want := range []string{
		`class="block block-wireframe"`,
		"After",
		"Sidebar",
		"var(--wf-fg)",
		"--wf-bg:var(--bg-card",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("wireframe output missing %q\n%s", want, html)
		}
	}
	if strings.Contains(html, "wireframe-error") {
		t.Errorf("token-only wireframe must not render the error notice:\n%s", html)
	}
}

func TestWireframe_RejectsRawColors(t *testing.T) {
	cases := map[string]string{
		"hex":  `<div style="color:#ff0000">x</div>`,
		"hex3": `<div style="color:#f00">x</div>`,
		"rgb":  `<div style="color:rgb(255,0,0)">x</div>`,
		"rgba": `<div style="color:rgba(255,0,0,.5)">x</div>`,
		"hsl":  `<div style="color:hsl(0,100%,50%)">x</div>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			wf := &blocks.Wireframe{Body: body}
			if !wf.RawColors() {
				t.Fatalf("expected RawColors()=true for %q", body)
			}
			html := render(t, wf)
			if !strings.Contains(html, "wireframe-error") {
				t.Errorf("expected error notice for raw-color wireframe:\n%s", html)
			}
			// The raw markup must NOT be emitted when rejected.
			if strings.Contains(html, "wireframe-canvas") {
				t.Errorf("raw-color wireframe must not render its body:\n%s", html)
			}
		})
	}
}

func TestWireframe_AnchorStamped(t *testing.T) {
	wf := &blocks.Wireframe{
		Body:   `<div style="color:var(--wf-fg)">x</div>`,
		Anchor: "slice-3-block-wireframe-1",
	}
	html := render(t, wf)
	if !strings.Contains(html, `id="slice-3-block-wireframe-1"`) {
		t.Errorf("expected anchor id:\n%s", html)
	}
	if !strings.Contains(html, `data-block-anchor="slice-3-block-wireframe-1"`) {
		t.Errorf("expected data-block-anchor:\n%s", html)
	}
}
