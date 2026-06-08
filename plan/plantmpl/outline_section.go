package plantmpl

import (
	"html/template"
	"io"
)

var outlineTmpl = template.Must(
	template.ParseFS(templateFS, "templates/outline_section.gohtml"),
)

// OutlineSection renders the plan outline zone containing
// the high-level implementation plan narrative.
type OutlineSection struct {
	Content template.HTML
}

// Render writes the outline section HTML.
func (o *OutlineSection) Render(w io.Writer) error {
	return outlineTmpl.Execute(w, o)
}
