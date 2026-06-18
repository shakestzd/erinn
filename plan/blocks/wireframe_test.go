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

func TestWireframe_SanitizesXSS(t *testing.T) {
	// None of these carry raw colors, so they pass the RawColors() gate and reach
	// SafeBody() — exactly the path that must sanitize. Each payload must be
	// neutralized while benign sibling content survives.
	cases := []struct {
		name    string
		body    string
		banned  []string
		survive string
	}{
		{"script", `<script>alert(1)</script><div style="color:var(--wf-fg)">ok</div>`, []string{"<script", "alert(1)"}, "ok"},
		{"event-handler", `<div onclick="steal()" style="color:var(--wf-fg)">cell</div>`, []string{"onclick", "steal()"}, "cell"},
		{"iframe", `<iframe src="https://evil.example"></iframe><p>body</p>`, []string{"<iframe", "evil.example"}, "body"},
		{"js-url", `<a href="javascript:alert(1)">link</a><span>txt</span>`, []string{"javascript:", "href"}, "txt"},
		{"style-url", `<div style="background:url(javascript:alert(1))">shown</div>`, []string{"url(", "javascript:"}, "shown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := &blocks.Wireframe{Body: tc.body}
			if wf.RawColors() {
				t.Fatalf("test payload unexpectedly tripped RawColors(); rewrite without raw colors: %q", tc.body)
			}
			html := render(t, wf)
			for _, banned := range tc.banned {
				if strings.Contains(html, banned) {
					t.Errorf("sanitized wireframe still contains %q\n%s", banned, html)
				}
			}
			if !strings.Contains(html, tc.survive) {
				t.Errorf("expected benign content %q to survive\n%s", tc.survive, html)
			}
		})
	}
}

func TestWireframe_AllowsGenericDesignTokens(t *testing.T) {
	// The plan validator accepts any var(--token) (it only rejects raw colors), so
	// the sanitizer must not silently strip non --wf-* tokens like var(--color-fg).
	wf := &blocks.Wireframe{
		Body: `<div style="color:var(--color-fg);background:var(--bg-card)">panel</div>`,
	}
	if wf.RawColors() {
		t.Fatalf("generic-token wireframe wrongly flagged as raw-color")
	}
	html := render(t, wf)
	for _, want := range []string{"var(--color-fg)", "var(--bg-card)", "panel"} {
		if !strings.Contains(html, want) {
			t.Errorf("sanitizer stripped a valid design token; missing %q\n%s", want, html)
		}
	}
}

func TestWireframe_NoUITakeover(t *testing.T) {
	// Stored wireframe markup must not be able to cover the surrounding chrome:
	// fixed/sticky positioning and viewport-sized overlays are stripped, and the
	// canvas itself is contained + clipped.
	wf := &blocks.Wireframe{
		Body: `<div style="position:fixed;top:0;left:0;width:100vw;height:100vh;z-index:9999">overlay</div>`,
	}
	html := render(t, wf)
	for _, banned := range []string{"position:fixed", "position: fixed", "100vw", "100vh", "sticky"} {
		if strings.Contains(html, banned) {
			t.Errorf("UI-takeover CSS survived sanitization: %q\n%s", banned, html)
		}
	}
	// The canvas must clip/contain its descendants regardless of external CSS.
	for _, want := range []string{"overflow:hidden", "contain:layout paint"} {
		if !strings.Contains(html, want) {
			t.Errorf("wireframe canvas missing containment %q\n%s", want, html)
		}
	}
	// Benign content still renders.
	if !strings.Contains(html, "overlay") {
		t.Errorf("expected body text to survive\n%s", html)
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
