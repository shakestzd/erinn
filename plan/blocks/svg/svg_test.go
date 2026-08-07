package svg_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/blocks/svg"
)

// TestEmit_EscapesLabels guards the exact regression in bug-37801e41: the plan
// renderer once mangled literal angle brackets in slice titles. Every label
// passed to WriteText must come out HTML-escaped.
func TestEmit_EscapesLabels(t *testing.T) {
	var buf bytes.Buffer
	if err := svg.WriteText(&buf, svg.Text{Label: `<script>alert(1)</script>`}); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("label must be escaped, got raw markup:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("expected escaped label:\n%s", out)
	}
}

// TestEmit_GreppableLabels is the CRITICAL contract from the slice brief:
// `wipnote relevant` runs `rg --type html` over rendered plan HTML and
// `wipnote find plans` parses the DOM by text content. A label split into
// per-character <tspan>s would silently break phrase matching for every
// existing plan, with no error anywhere.
func TestEmit_GreppableLabels(t *testing.T) {
	var buf bytes.Buffer
	const label = "Render Diagram"
	if err := svg.WriteText(&buf, svg.Text{Label: label}); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "<tspan") {
		t.Errorf("label must not be split into per-character tspans:\n%s", out)
	}
	if n := strings.Count(out, "<text"); n != 1 {
		t.Errorf("expected exactly one <text> node, found %d:\n%s", n, out)
	}
	if !strings.Contains(out, ">"+label+"<") {
		t.Errorf("expected label as one contiguous text run:\n%s", out)
	}
}

// TestEmit_ArrowheadMarkerShared verifies there is exactly one canonical
// arrowhead marker definition, and that Line/Path both reference it by the
// same id rather than each hand-rolling their own marker.
func TestEmit_ArrowheadMarkerShared(t *testing.T) {
	var defs bytes.Buffer
	if err := svg.WriteArrowheadMarker(&defs); err != nil {
		t.Fatalf("WriteArrowheadMarker: %v", err)
	}
	out := defs.String()
	if !strings.Contains(out, "<marker") {
		t.Errorf("expected a <marker> element:\n%s", out)
	}
	if !strings.Contains(out, `id="`+svg.ArrowheadMarkerID+`"`) {
		t.Errorf("expected marker id %q:\n%s", svg.ArrowheadMarkerID, out)
	}
	if n := strings.Count(out, "<marker"); n != 1 {
		t.Errorf("expected exactly one <marker> definition, found %d:\n%s", n, out)
	}

	var line, path bytes.Buffer
	if err := svg.WriteLine(&line, svg.Line{X2: 10, Y2: 10, MarkerEnd: svg.ArrowheadMarkerRef}); err != nil {
		t.Fatalf("WriteLine: %v", err)
	}
	if err := svg.WritePath(&path, svg.Path{D: "M0 0 L10 10", MarkerEnd: svg.ArrowheadMarkerRef}); err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	want := `marker-end="url(#` + svg.ArrowheadMarkerID + `)"`
	if !strings.Contains(line.String(), want) {
		t.Errorf("line does not reference shared marker:\n%s", line.String())
	}
	if !strings.Contains(path.String(), want) {
		t.Errorf("path does not reference shared marker:\n%s", path.String())
	}
}

// TestMeasure_MonospaceExact locks the deterministic width formula: charCount
// × fontSize × MonoAdvance, with no font-file lookup involved.
func TestMeasure_MonospaceExact(t *testing.T) {
	cases := []struct {
		s        string
		fontSize float64
		want     float64
	}{
		{"abc", 10, 3 * 10 * svg.MonoAdvance},
		{"wipnote", 12, 7 * 12 * svg.MonoAdvance},
		{"", 12, 0},
	}
	for _, c := range cases {
		if got := svg.Measure(c.s, c.fontSize); got != c.want {
			t.Errorf("Measure(%q, %v) = %v, want %v", c.s, c.fontSize, got, c.want)
		}
	}
	if got, want := svg.MeasureDefault("abc"), svg.Measure("abc", svg.DefaultFontSize); got != want {
		t.Errorf("MeasureDefault(%q) = %v, want %v", "abc", got, want)
	}
	// Multi-byte runes count as one character each, not one per byte.
	if got, want := svg.Measure("café", 10), 4*10*svg.MonoAdvance; got != want {
		t.Errorf("Measure(%q, 10) = %v, want %v (rune count, not byte count)", "café", got, want)
	}
}

// TestEmit_ThemeVariables checks colors travel exclusively through CSS custom
// properties: the package's own built-in default (the arrowhead fill) is a
// var(--...) reference, a caller's var(--...) Fill is passed through intact,
// and a literal color is rejected outright rather than silently emitted.
func TestEmit_ThemeVariables(t *testing.T) {
	var marker bytes.Buffer
	if err := svg.WriteArrowheadMarker(&marker); err != nil {
		t.Fatalf("WriteArrowheadMarker: %v", err)
	}
	if !strings.Contains(marker.String(), "var(--") {
		t.Errorf("default marker fill must be a theme variable:\n%s", marker.String())
	}

	var rect bytes.Buffer
	if err := svg.WriteRect(&rect, svg.Rect{Width: 10, Height: 10, Fill: "var(--accent)"}); err != nil {
		t.Fatalf("var(--accent) fill should be accepted: %v", err)
	}
	if !strings.Contains(rect.String(), `fill="var(--accent)"`) {
		t.Errorf("expected theme variable fill preserved:\n%s", rect.String())
	}

	for _, bad := range []string{"#cdff00", "rgb(1,2,3)", "hsl(0, 0%, 0%)", "red", "var(--accent, #cdff00)"} {
		var buf bytes.Buffer
		if err := svg.WriteRect(&buf, svg.Rect{Width: 10, Height: 10, Fill: bad}); err == nil {
			t.Errorf("expected literal/fallback color %q to be rejected", bad)
		}
	}
}

// TestEmit_NoExternalRefs asserts a fully assembled document has no script,
// stylesheet, font, or network reference of any kind. The mandatory SVG
// xmlns namespace declaration is a fixed identifier, never fetched, so it is
// deliberately not flagged.
func TestEmit_NoExternalRefs(t *testing.T) {
	var buf bytes.Buffer
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("build document: %v", err)
		}
	}
	must(svg.WriteOpen(&buf, svg.Root{Width: 200, Height: 100}))
	must(svg.WriteArrowheadMarker(&buf))
	must(svg.WriteGroup(&buf, svg.Group{Class: "diagram-node"}, func(w io.Writer) error {
		return nil
	}))
	must(svg.WriteRect(&buf, svg.Rect{Width: 80, Height: 30, Fill: "var(--accent)"}))
	must(svg.WriteLine(&buf, svg.Line{X2: 100, Y2: 50, MarkerEnd: svg.ArrowheadMarkerRef}))
	must(svg.WriteText(&buf, svg.Text{Label: "Collect"}))
	must(svg.WriteClose(&buf))

	lower := strings.ToLower(buf.String())
	for _, forbidden := range []string{
		"<script", "<link", "@import",
		`href="http`, `src="http`, `url(http`, "url('http", `url("http`,
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("output contains forbidden external reference %q:\n%s", forbidden, buf.String())
		}
	}
}
