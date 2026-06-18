package recaptmpl

import (
	"html/template"
	"io"

	"github.com/shakestzd/wipnote/internal/lineage"
)

var lineageChainTmpl = template.Must(
	template.ParseFS(templateFS, "templates/lineage_chain.gohtml"),
)

// LineageChain renders the recap's grounding chain as a STATIC, depth-indented
// table — not the dashboard's interactive D3 force graph, which needs the SPA
// runtime and cannot run in a committed file. Each node shows its depth, type,
// id, title, and the edge that reached it.
type LineageChain struct {
	Nodes []lineage.Node
}

// chainRow is the per-node render model. Indent is derived from BFS depth so the
// static table visually conveys the hop structure.
type chainRow struct {
	Depth    int
	Indent   int // pixels of left padding = Depth * step
	Type     string
	ID       string
	Title    string
	EdgeType string
	IsPivot  bool
}

// Render writes the static lineage-chain HTML to w.
func (l *LineageChain) Render(w io.Writer) error {
	const step = 16
	rows := make([]chainRow, 0, len(l.Nodes))
	for _, n := range l.Nodes {
		rows = append(rows, chainRow{
			Depth:    n.Depth,
			Indent:   n.Depth * step,
			Type:     n.Type,
			ID:       n.ID,
			Title:    n.Title,
			EdgeType: n.EdgeType,
			IsPivot:  n.Depth == 0,
		})
	}
	return lineageChainTmpl.Execute(w, struct {
		Count int
		Rows  []chainRow
	}{Count: len(rows), Rows: rows})
}
