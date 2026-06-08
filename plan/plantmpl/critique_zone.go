package plantmpl

import (
	"html/template"
	"io"
)

// CritiqueZone renders the multi-model critique section containing
// assumption verification, model critiques, synthesis, and risk analysis.
type CritiqueZone struct {
	Assumptions     []AssumptionResult
	GeminiCritique  template.HTML
	GeminiTitle     string // e.g. "Haiku (design review)" — falls back to "Design Critique"
	CopilotCritique template.HTML
	CopilotTitle    string // e.g. "Sonnet (feasibility)" — falls back to "Feasibility Critique"
	Synthesis       template.HTML
	RiskTable       []RiskRow
}

// AssumptionResult represents a verified or falsified assumption.
type AssumptionResult struct {
	Text     string
	Badge    string // "verified", "unknown", "falsified"
	Evidence string
}

// RiskRow represents a single row in the risk assessment table.
type RiskRow struct {
	Risk       string
	Severity   string
	Mitigation string
}

var critiqueTmpl = template.Must(
	template.ParseFS(templateFS, "templates/critique_zone.gohtml"),
)

// Render writes the critique zone HTML.
func (c *CritiqueZone) Render(w io.Writer) error {
	return critiqueTmpl.Execute(w, c)
}

// BadgeClass returns the CSS class for an assumption badge.
func (a AssumptionResult) BadgeClass() string {
	switch a.Badge {
	case "verified":
		return "badge-approved"
	case "falsified":
		return "badge-blocked"
	default:
		return "badge-pending"
	}
}

// SeverityClass returns the CSS class for a risk severity.
func (r RiskRow) SeverityClass() string {
	switch r.Severity {
	case "High":
		return "badge-blocked"
	case "Medium", "Med":
		return "badge-revision"
	default:
		return "badge-pending"
	}
}
