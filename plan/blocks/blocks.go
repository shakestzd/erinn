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
	"embed"
	"html/template"
	"io"
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

// APIEndpoint renders an HTTP route (method + path) with optional request /
// response parameters. It is the wipnote-native "api-endpoint" block.
type APIEndpoint struct {
	Method string
	Path   string
	Params []Column
}

// Render writes the api-endpoint block HTML to w.
func (a *APIEndpoint) Render(w io.Writer) error {
	return blockTmpl.ExecuteTemplate(w, "api_endpoint", a)
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
