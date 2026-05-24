package plantmpl

import (
	"html/template"
	"io"
)

// DependencyGraph renders the interactive dependency graph zone showing
// slice relationships and approval status.
type DependencyGraph struct {
	Nodes []GraphNode
}

// GraphNode represents a single node in the dependency graph.
type GraphNode struct {
	Num       int
	Name      string
	Status    string // "pending", "approved", "revision", "discuss", "blocked"
	Deps      string // comma-separated dep numbers
	Files     int
	Issues    int // unresolved critic_revisions count
	Questions int // open questions count
}

// ApprovalStatusToGraphStatus maps a SliceCard approval status string to the
// STATUS_COLORS key used by the dagre-d3 JS renderer. Unknown values map to
// "pending" so the graph is never left with an unrecognized color key.
func ApprovalStatusToGraphStatus(approvalStatus string) string {
	switch approvalStatus {
	case "approved":
		return "approved"
	case "revision", "changes_requested", "rejected":
		return "revision"
	case "discuss":
		return "discuss"
	case "blocked":
		return "blocked"
	default:
		return "pending"
	}
}

var depGraphTmpl = template.Must(
	template.ParseFS(templateFS, "templates/dependency_graph.gohtml"),
)

// Render writes the dependency graph zone HTML to w.
func (g *DependencyGraph) Render(w io.Writer) error {
	return depGraphTmpl.Execute(w, g)
}
