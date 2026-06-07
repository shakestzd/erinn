package plantmpl

import (
	"html/template"
	"io"
)

var progressBarTmpl = template.Must(
	template.ParseFS(templateFS, "templates/progress_bar.gohtml"),
)

// ProgressBar renders the plan progress indicator showing
// approved vs pending vs total slice counts.
type ProgressBar struct {
	Approved int
	Total    int
	Pending  int
}

// Render writes the progress bar zone HTML.
func (pb *ProgressBar) Render(w io.Writer) error {
	return progressBarTmpl.Execute(w, pb)
}

// Percent returns the approval percentage (0-100).
func (pb *ProgressBar) Percent() int {
	if pb.Total == 0 {
		return 0
	}
	return pb.Approved * 100 / pb.Total
}
