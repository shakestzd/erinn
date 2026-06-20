package blocks

import "io"

// Diagram renders a flow diagram: an ordered sequence of steps connected by
// arrows. It is deliberately Mermaid-free and uses pure HTML/CSS (no client-side
// layout engine, no SVG text-measurement), so it renders deterministically
// server-side and degrades to a readable list when unstyled. Direction is "lr"
// (left-to-right, default) or "tb" (top-to-bottom).
type Diagram struct {
	Title     string
	Steps     []string
	Direction string
}

// Render writes the diagram block HTML to w.
func (d *Diagram) Render(w io.Writer) error {
	dir := d.Direction
	if dir != "tb" {
		dir = "lr"
	}
	return blockTmpl.ExecuteTemplate(w, "diagram", struct {
		Title    string
		Steps    []string
		Dir      string
		Vertical bool
	}{Title: d.Title, Steps: d.Steps, Dir: dir, Vertical: dir == "tb"})
}
