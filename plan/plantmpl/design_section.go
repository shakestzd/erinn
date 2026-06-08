package plantmpl

import (
	"html/template"
	"io"
)

var designTmpl = template.Must(
	template.ParseFS(templateFS, "templates/design_section.gohtml"),
)

// DesignSection renders the design rationale zone containing
// architecture notes and design decisions.
type DesignSection struct {
	Content template.HTML
}

// Render writes the design section HTML.
func (d *DesignSection) Render(w io.Writer) error {
	return designTmpl.Execute(w, d)
}
