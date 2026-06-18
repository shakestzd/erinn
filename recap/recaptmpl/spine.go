package recaptmpl

import (
	"html/template"
	"io"
)

var lineageSpineTmpl = template.Must(
	template.ParseFS(templateFS, "templates/lineage_spine.gohtml"),
)

// spineRef is a non-pivot station on the causal spine: an ancestry node above
// the pivot, or a produced/related node below it. Glyph is the kind-shaped inline
// SVG (kind is encoded by shape, never color).
type spineRef struct {
	Glyph template.HTML
	Kind  string
	ID    string
	Title string
	Edge  string // edge label riding the connector (e.g. "part of", "produced")
}

// spineCommit is a produced commit station.
type spineCommit struct {
	Glyph   template.HTML
	Hash    string
	Message string
	When    string
}

// spineFile is a produced file station whose collapsible body embeds the file's
// annotated diff (rendered hunks-only — the file row is the <summary>).
type spineFile struct {
	Change  string
	Path    string
	Added   int
	Removed int
	Diff    template.HTML // pre-rendered embedded AnnotatedDiff (hunks only)
}

// LineageSpine is the unified recap zone. It reads top-to-bottom as causation:
// ancestry (where the work came from, muted) → the pivot (the work item) →
// produced (the commits and files it changed, with diffs embedded inline) →
// related downstream nodes. It replaces the former separate file-tree,
// annotated-diff, and lineage-table zones so a reviewer never has to map a
// detached diff back to the work that caused it.
//
// When the recap is ungrounded (a bare git range with no work item), Pivot and
// Ancestors are empty and the spine renders only the produced commits/files —
// still a valid, useful view.
type LineageSpine struct {
	Pivot     *spineRef
	Ancestors []spineRef    // origin-first (deepest ancestor at the top)
	Commits   []spineCommit // produced commits
	Files     []spineFile   // produced files, each with an embedded diff
	Related   []spineRef    // direct downstream nodes (plan, track, the recap itself)
}

// has reports whether the spine has anything to render.
func (s *LineageSpine) has() bool {
	return s != nil && (s.Pivot != nil || len(s.Ancestors) > 0 ||
		len(s.Commits) > 0 || len(s.Files) > 0 || len(s.Related) > 0)
}

// Render writes the spine zone HTML to w.
func (s *LineageSpine) Render(w io.Writer) error {
	return lineageSpineTmpl.Execute(w, s)
}

// glyphFor returns the kind-shaped inline SVG for a node type. Shape encodes
// kind; color is reserved for causal direction. Unknown kinds get a neutral dot.
func glyphFor(kind string) template.HTML {
	switch normalizeKind(kind) {
	case "session":
		return svgRing
	case "plan":
		return svgHexagon
	case "track":
		return svgFrame
	case "feature", "bug", "spike":
		return svgDiamond
	case "commit":
		return svgDot
	case "recap":
		return svgFramedSquare
	default:
		return svgSmallDot
	}
}

// normalizeKind maps id prefixes and raw type strings to a canonical kind.
func normalizeKind(kind string) string {
	switch kind {
	case "feature", "feat":
		return "feature"
	case "bug":
		return "bug"
	case "spike", "spk":
		return "spike"
	case "track", "trk":
		return "track"
	case "plan":
		return "plan"
	case "session", "sess":
		return "session"
	case "recap":
		return "recap"
	default:
		return kind
	}
}

// Inline SVG glyphs (currentColor so direction styling tints them).
const (
	svgRing         template.HTML = `<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="12" cy="12" r="2.6" fill="currentColor"/></svg>`
	svgHexagon      template.HTML = `<svg viewBox="0 0 24 24"><polygon points="12,3 20,7.5 20,16.5 12,21 4,16.5 4,7.5" fill="none" stroke="currentColor" stroke-width="2"/></svg>`
	svgFrame        template.HTML = `<svg viewBox="0 0 24 24"><rect x="4" y="6" width="16" height="12" rx="2" fill="none" stroke="currentColor" stroke-width="2"/></svg>`
	svgDiamond      template.HTML = `<svg viewBox="0 0 24 24"><rect x="6.5" y="6.5" width="11" height="11" transform="rotate(45 12 12)" fill="currentColor"/></svg>`
	svgDot          template.HTML = `<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="6" fill="currentColor"/></svg>`
	svgSmallDot     template.HTML = `<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="4" fill="currentColor"/></svg>`
	svgFramedSquare template.HTML = `<svg viewBox="0 0 24 24"><rect x="4.5" y="4.5" width="15" height="15" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><rect x="9" y="9" width="6" height="6" fill="currentColor"/></svg>`
)
