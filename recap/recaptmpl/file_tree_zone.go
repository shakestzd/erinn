package recaptmpl

import (
	"html/template"
	"io"
	"strings"

	"github.com/shakestzd/wipnote/internal/recap"
	"github.com/shakestzd/wipnote/plan/blocks"
)

var fileTreeZoneTmpl = template.Must(
	template.ParseFS(templateFS, "templates/file_tree_zone.gohtml"),
)

// FileTreeZone renders the recap's changed-file overview. It delegates the inner
// list markup to the shared plan/blocks.FileTree renderer so plan and recap show
// identical file-tree structure, then wraps it in the recap zone shell.
type FileTreeZone struct {
	Files []recap.FileChange
}

// Block converts the collected file changes into the shared block value.
func (z *FileTreeZone) block() *blocks.FileTree {
	entries := make([]blocks.FileNode, 0, len(z.Files))
	for _, f := range z.Files {
		entries = append(entries, blocks.FileNode{
			Path:   f.Path,
			Change: string(f.Change),
		})
	}
	return &blocks.FileTree{Entries: entries}
}

// Render writes the file-tree zone HTML to w.
func (z *FileTreeZone) Render(w io.Writer) error {
	var inner template.HTML
	if b := z.block(); b != nil {
		var sb strings.Builder
		if err := b.Render(&sb); err != nil {
			return err
		}
		inner = template.HTML(sb.String())
	}
	return fileTreeZoneTmpl.Execute(w, struct {
		Count int
		Inner template.HTML
	}{Count: len(z.Files), Inner: inner})
}
