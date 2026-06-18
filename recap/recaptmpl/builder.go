package recaptmpl

import (
	"path"
	"strings"

	"github.com/shakestzd/wipnote/internal/recap"
)

// Build assembles a RecapPage from collected RecapData. It is a pure, stateless
// transform: same input always yields the same page. When the recap is
// ungrounded (LineageChain empty), the lineage zone is left nil so Render omits
// it entirely.
func Build(data recap.RecapData) *RecapPage {
	page := &RecapPage{
		Title:    titleFor(data),
		Outcome:  data.Outcome,
		Input:    data.Provenance.Input,
		Kind:     string(data.Provenance.Kind),
		GitRange: data.Provenance.GitRange,
		Grounded: data.Provenance.Grounded,
		Files:    buildFileTree(data.Files),
		Diff:     buildDiffZone(data.Files),
	}
	if len(data.LineageChain) > 0 {
		page.Lineage = &LineageChain{Nodes: data.LineageChain}
	}
	return page
}

// titleFor derives a short document title from the outcome, falling back to the
// provenance input when the outcome is empty.
func titleFor(data recap.RecapData) string {
	if t := strings.TrimSpace(data.Outcome); t != "" {
		return t
	}
	if data.Provenance.Input != "" {
		return data.Provenance.Input
	}
	return "Recap"
}

// buildFileTree converts the change set into the shared file-tree zone.
func buildFileTree(files []recap.FileChange) *FileTreeZone {
	if len(files) == 0 {
		return nil
	}
	return &FileTreeZone{Files: files}
}

// buildDiffZone builds one AnnotatedDiff per changed file. Mode defaults to
// unified (compact, degrade-safe); the dashboard injection path may re-render in
// split mode. Language is inferred from each file's extension.
func buildDiffZone(files []recap.FileChange) *DiffZone {
	if len(files) == 0 {
		return nil
	}
	diffs := make([]AnnotatedDiff, 0, len(files))
	for _, f := range files {
		diffs = append(diffs, AnnotatedDiff{
			File:     f,
			Mode:     DiffUnified,
			Language: languageFor(f.Path),
		})
	}
	return &DiffZone{Diffs: diffs}
}

// languageFor maps a file extension to a Prism language identifier. Unknown
// extensions yield "plaintext" so the code block still renders (and degrades)
// without a colorizer.
func languageFor(p string) string {
	switch strings.TrimPrefix(strings.ToLower(path.Ext(p)), ".") {
	case "go":
		return "go"
	case "js", "mjs", "cjs":
		return "javascript"
	case "ts":
		return "typescript"
	case "py":
		return "python"
	case "sh", "bash":
		return "bash"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "html", "gohtml":
		return "markup"
	case "css":
		return "css"
	case "md":
		return "markdown"
	default:
		return "plaintext"
	}
}
