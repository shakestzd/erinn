package plantmpl

import (
	"html/template"
	"io"
)

var questionsTmpl = template.Must(
	template.ParseFS(templateFS, "templates/questions_section.gohtml"),
)

// QuestionsSection renders the open questions and decision cards zone.
type QuestionsSection struct {
	Cards []DecisionCard
}

// DecisionCard represents a single decision point requiring human input.
type DecisionCard struct {
	ID          string
	Text        string
	Options     []string
	Selected    string // only set when human has explicitly chosen
	Recommended string // highlighted but not pre-selected
	Rationale   string
}

// Render writes the questions section zone HTML.
func (q *QuestionsSection) Render(w io.Writer) error {
	return questionsTmpl.Execute(w, q)
}
