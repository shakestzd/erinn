package plantmpl

import (
	"html/template"
	"io"
)

var finalizePreviewTmpl = template.Must(
	template.ParseFS(templateFS, "templates/finalize_preview.gohtml"),
)

// FinalizePreview renders the finalization preview zone showing
// all features ready for dispatch with their approval status.
type FinalizePreview struct {
	Features []PreviewFeature
	TrackID  string
}

// PreviewFeature represents a single feature in the finalize preview.
type PreviewFeature struct {
	Name     string
	Deps     string
	Approved bool
}

// Render writes the finalize preview zone HTML.
func (fp *FinalizePreview) Render(w io.Writer) error {
	return finalizePreviewTmpl.Execute(w, fp)
}

// ApprovedCount returns the number of approved features.
func (fp *FinalizePreview) ApprovedCount() int {
	count := 0
	for _, f := range fp.Features {
		if f.Approved {
			count++
		}
	}
	return count
}
