package svg

import "unicode/utf8"

// FontFamily is the monospace font stack every text primitive in this
// package sets explicitly via the font-family attribute (see WriteText). It
// is declared once here — not per renderer — so the font Measure assumes and
// the font actually painted can never drift apart.
const FontFamily = `ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, monospace`

// DefaultFontSize is the font-size, in SVG user units (px), text primitives
// use unless a caller sets Text.FontSize explicitly.
const DefaultFontSize = 12.0

// MonoAdvance is the fixed per-character advance width of FontFamily,
// expressed as a fraction of font-size. 0.6 is the conventional advance/em
// ratio shared by monospace font stacks like this one. It is a constant, not
// a measurement: this package never loads a font file or consults a system
// font's metrics table, so Measure gives byte-for-byte identical results on
// every machine and CI runner.
const MonoAdvance = 0.6

// Measure returns the deterministic rendered width, in SVG user units, of s
// set in FontFamily at fontSize. Width is exact by construction:
// runeCount(s) × fontSize × MonoAdvance. It counts Unicode code points (via
// utf8.RuneCountInString), not bytes, so a multi-byte rune such as "é" still
// counts as one monospace cell — though for a true monospace font, cell
// count is what determines rendered width regardless of which code point
// occupies each cell.
func Measure(s string, fontSize float64) float64 {
	return float64(utf8.RuneCountInString(s)) * fontSize * MonoAdvance
}

// MeasureDefault is Measure at DefaultFontSize, for callers that don't
// override Text.FontSize.
func MeasureDefault(s string) float64 {
	return Measure(s, DefaultFontSize)
}
