package recaptmpl

import (
	"bytes"
	"html/template"
	"io"
	"strconv"

	"github.com/shakestzd/wipnote/internal/recap"
)

// DiffMode selects how an AnnotatedDiff lays out its hunks.
type DiffMode string

const (
	// DiffUnified stacks removed and added lines in one column (compact,
	// degrade-safe). Default for committed standalone artifacts.
	DiffUnified DiffMode = "unified"
	// DiffSplit renders before/after side by side. Used on the dashboard
	// injection path and for wide-screen review.
	DiffSplit DiffMode = "split"
)

var (
	diffZoneTmpl = template.Must(
		template.ParseFS(templateFS, "templates/diff_zone.gohtml"),
	)
	annotatedDiffTmpl = template.Must(
		template.ParseFS(templateFS, "templates/annotated_diff.gohtml"),
	)
)

// DiffZone wraps the per-file annotated diffs in the recap zone shell.
type DiffZone struct {
	Diffs []AnnotatedDiff
}

// Render writes the diff zone HTML, concatenating each file's annotated diff.
func (z *DiffZone) Render(w io.Writer) error {
	var inner template.HTML
	for i := range z.Diffs {
		var buf bytes.Buffer
		if err := z.Diffs[i].Render(&buf); err != nil {
			return err
		}
		inner += template.HTML(buf.String())
	}
	return diffZoneTmpl.Execute(w, struct {
		Count int
		Inner template.HTML
	}{Count: len(z.Diffs), Inner: inner})
}

// HunkView is the pre-computed render model for one hunk: its header context,
// add/remove counts, and the lines for unified or split layout.
type HunkView struct {
	Header  string
	Summary string // e.g. "2 added, 1 removed"
	Added   int
	Removed int
	Before  []string // removed lines (split left / unified top)
	After   []string // added lines (split right / unified bottom)
}

// AnnotatedDiff renders the diff for a single file in either unified or split
// mode, with a per-hunk summary line. Code is emitted inside
// <pre><code class="language-…"> so it renders as plain text without Prism and
// is colorized only where Prism is present (dashboard).
type AnnotatedDiff struct {
	File     recap.FileChange
	Mode     DiffMode
	Language string // Prism language id, e.g. "go"; empty => "plaintext"
}

// Render writes the annotated diff HTML for one file.
func (d *AnnotatedDiff) Render(w io.Writer) error {
	mode := d.Mode
	if mode == "" {
		mode = DiffUnified
	}
	lang := d.Language
	if lang == "" {
		lang = "plaintext"
	}
	return annotatedDiffTmpl.Execute(w, struct {
		Path     string
		Change   string
		Language string
		IsSplit  bool
		ModeName string
		Hunks    []HunkView
	}{
		Path:     d.File.Path,
		Change:   string(d.File.Change),
		Language: lang,
		IsSplit:  mode == DiffSplit,
		ModeName: string(mode),
		Hunks:    hunkViews(d.File.Hunks),
	})
}

// hunkViews precomputes the render model for each hunk, including the per-hunk
// add/remove summary.
func hunkViews(hunks []recap.Hunk) []HunkView {
	views := make([]HunkView, 0, len(hunks))
	for _, h := range hunks {
		added := len(h.After)
		removed := len(h.Before)
		views = append(views, HunkView{
			Header:  h.Header,
			Summary: summarize(added, removed),
			Added:   added,
			Removed: removed,
			Before:  h.Before,
			After:   h.After,
		})
	}
	return views
}

// summarize renders the add/remove counts for a hunk summary line. It always
// names both dimensions so callers/tests can rely on the wording.
func summarize(added, removed int) string {
	return plural(added, "added") + ", " + plural(removed, "removed")
}

func plural(n int, label string) string {
	return strconv.Itoa(n) + " " + label
}
