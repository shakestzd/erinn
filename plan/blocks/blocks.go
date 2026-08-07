// Package blocks holds shared, self-contained HTML block renderers used by both
// the plan renderer (plan/plantmpl) and the recap renderer (recap/recaptmpl).
//
// It is a LEAF package: it depends on nothing inside the wipnote tree (only the
// standard library), so both higher-level renderers can import it without
// creating an import cycle. Consumers adapt their own domain types (e.g.
// planyaml.SliceBlock or recap.FileChange) into the neutral block values defined
// here, then call Render.
//
// All inner block markup is produced with html/template, so every dynamic value
// is contextually auto-escaped — these renderers are safe to feed user/agent
// content. Markup deliberately mirrors the wipnote dashboard/plan CSS vocabulary
// (block/table/badge classes) so the same output looks correct in a committed
// standalone artifact and when injected into the dashboard.
package blocks

import (
	"bytes"
	"embed"
	"html/template"
	"io"
	"strings"

	"github.com/shakestzd/wipnote/plan/blocks/svg"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

// blockTmpl parses all block templates once at init. Each block type renders via
// its named template (data_model, api_endpoint, file_tree).
var blockTmpl = template.Must(template.ParseFS(templateFS, "templates/*.gohtml"))

// Block is anything that can render itself into an HTML fragment. It mirrors the
// Component contract used by plantmpl/recaptmpl so blocks compose into zones.
type Block interface {
	Render(w io.Writer) error
}

// Column is one named, typed field in a data-model or one parameter of an
// api-endpoint. Note is an optional free-form annotation (e.g. "sorted by path",
// "nullable").
type Column struct {
	Name string
	Type string
	Note string
}

// DataModel renders an entity/table with named, typed columns. It is the
// wipnote-native "data-model" block.
type DataModel struct {
	Name    string
	Columns []Column
}

// Render writes the data-model block HTML to w.
func (d *DataModel) Render(w io.Writer) error {
	return blockTmpl.ExecuteTemplate(w, "data_model", d)
}

// FileNode is one entry in a FileTree. Change is optional: when empty (plan-time
// file lists) no change badge is rendered; when set (recap diffs) it carries a
// "add" / "modify" / "delete" classification that drives a colored badge.
type FileNode struct {
	Path   string
	Change string
}

// FileTree renders an ordered list of file paths the change set (or slice)
// touches. It is the wipnote-native "file-tree" block, shared verbatim between
// the recap file-tree zone and plan slice file lists.
type FileTree struct {
	Entries []FileNode
}

// Render writes the file-tree block HTML to w.
func (f *FileTree) Render(w io.Writer) error {
	return blockTmpl.ExecuteTemplate(w, "file_tree", f)
}

// fileTreeIndentUnit is the per-ancestor-level width, in SVG user units (px),
// of a file-tree row's depth gutter (see fileTreeGutter). fileTreeRowHeight is
// the gutter's own fixed height — deliberately independent of the host page's
// line-height so a row never needs pixel-perfect coordination with a
// stylesheet outside this package (font-size, line-height, etc. all live in
// the plan/recap page shells, not here).
const (
	fileTreeIndentUnit = 14.0
	fileTreeRowHeight  = 18.0
)

// fileTreeRow is one entry as the template sees it: its data plus a
// precomputed depth "gutter" (indentation and ancestor guide lines drawn as a
// small self-contained SVG), so the template itself stays a plain
// data-driven range with no layout logic of its own. Mirrors the
// computed-method pattern Wireframe uses for its own trusted fragments
// (RawColors/SafeBody/Tokens).
type fileTreeRow struct {
	Path   string
	Change string
	Gutter template.HTML
}

// Rows adapts Entries into their renderable form. A top-level entry (no "/"
// in its path) gets an empty Gutter, so a flat file-tree renders exactly as
// it always has — no gutter markup, no visual change. This is what stops a
// tree of 3 files and a tree of 30 from rendering identically flat
// (feat-47793a68): nested entries get a real, visible depth indicator.
func (f *FileTree) Rows() ([]fileTreeRow, error) {
	rows := make([]fileTreeRow, len(f.Entries))
	for i, e := range f.Entries {
		gutter, err := fileTreeGutter(f.Entries, i)
		if err != nil {
			return nil, err
		}
		rows[i] = fileTreeRow{Path: e.Path, Change: e.Change, Gutter: gutter}
	}
	return rows, nil
}

// fileTreeGutter draws entries[i]'s depth indicator: empty for a top-level
// entry, otherwise a small SVG with one column per ancestor directory — a
// vertical guide line where a later entry still shares that ancestor, blank
// where it doesn't (the classic `tree`-command rule) — ending in an elbow
// that bends into the label, tee'd downward when a later entry shares the
// same immediate parent.
func fileTreeGutter(entries []FileNode, i int) (template.HTML, error) {
	segs := strings.Split(entries[i].Path, "/")
	depth := len(segs) - 1
	if depth <= 0 {
		return "", nil
	}
	width := float64(depth) * fileTreeIndentUnit
	var buf bytes.Buffer
	if err := svg.WriteOpen(&buf, svg.Root{Width: width, Height: fileTreeRowHeight, Class: "file-tree-gutter"}); err != nil {
		return "", err
	}
	if err := writeFileTreeGuides(&buf, entries, i, segs, depth, width); err != nil {
		return "", err
	}
	if err := svg.WriteClose(&buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// writeFileTreeGuides draws one column per ancestor level of entries[i] into
// the already-open gutter SVG at w.
func writeFileTreeGuides(w io.Writer, entries []FileNode, i int, segs []string, depth int, width float64) error {
	const mid = fileTreeRowHeight / 2
	for level := 0; level < depth; level++ {
		x := (float64(level) + 0.5) * fileTreeIndentUnit
		continues := fileTreeHasLaterSibling(entries, i, segs, level)
		if level < depth-1 {
			if !continues {
				continue // blank column: no later entry shares this ancestor
			}
			if err := svg.WriteLine(w, svg.Line{X1: x, Y1: 0, X2: x, Y2: fileTreeRowHeight, Stroke: "var(--border)"}); err != nil {
				return err
			}
			continue
		}
		// Immediate-parent column: elbow up-then-right, continuing straight
		// down (a tee) only when a later entry shares this same parent.
		bottom := mid
		if continues {
			bottom = fileTreeRowHeight
		}
		if err := svg.WriteLine(w, svg.Line{X1: x, Y1: 0, X2: x, Y2: bottom, Stroke: "var(--border)"}); err != nil {
			return err
		}
		if err := svg.WriteLine(w, svg.Line{X1: x, Y1: mid, X2: width, Y2: mid, Stroke: "var(--border)"}); err != nil {
			return err
		}
	}
	return nil
}

// fileTreeHasLaterSibling reports whether some entry after i shares the
// ancestor directory entries[i] passes through at the given 0-based level
// (the directory formed by its first level+1 path segments). This drives the
// tree-guide rule: a column's line continues past this row only if there is
// more to draw below it.
func fileTreeHasLaterSibling(entries []FileNode, i int, segs []string, level int) bool {
	prefix := strings.Join(segs[:level+1], "/") + "/"
	for j := i + 1; j < len(entries); j++ {
		if strings.HasPrefix(entries[j].Path, prefix) {
			return true
		}
	}
	return false
}
