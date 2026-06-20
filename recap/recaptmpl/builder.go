package recaptmpl

import (
	"bytes"
	"html/template"
	"path"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/internal/lineage"
	"github.com/shakestzd/wipnote/internal/recap"
)

// Build assembles a RecapPage from collected RecapData. It is a pure, stateless
// transform: same input always yields the same page. The recap has two zones —
// the outcome narrative and the unified lineage spine, which embeds the changed
// files and their diffs as the "produced" half of the causal chain.
func Build(data recap.RecapData) *RecapPage {
	return &RecapPage{
		Title:    titleFor(data),
		Outcome:  data.Outcome,
		Input:    data.Provenance.Input,
		Kind:     string(data.Provenance.Kind),
		GitRange: data.Provenance.GitRange,
		Grounded: data.Provenance.Grounded,
		Spine:    buildSpine(data),
	}
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

// buildSpine assembles the unified lineage spine: ancestry above the pivot, then
// the produced commits and files (with diffs embedded), then direct downstream
// nodes. Returns nil when there is nothing to show.
func buildSpine(data recap.RecapData) *LineageSpine {
	s := &LineageSpine{}

	// The pivot anchors any grounded recap (a work item or session), even when
	// the graph walk found no neighbors — losing the anchor would make a grounded
	// recap indistinguishable from a bare range. Ancestry/related are populated
	// independently, only when lineage nodes actually exist.
	if data.Provenance.Grounded && data.Provenance.Input != "" {
		s.Pivot = &spineRef{
			Glyph: glyphFor(kindFromID(data.Provenance.Input)),
			Kind:  kindFromID(data.Provenance.Input),
			ID:    data.Provenance.Input,
			Title: data.Outcome,
		}
	}
	if len(data.LineageChain) > 0 {
		s.Ancestors = collectAncestors(data.LineageChain)
		s.Related = collectRelated(data.LineageChain)
	}

	// Produced commits.
	for _, c := range data.Commits {
		s.Commits = append(s.Commits, spineCommit{
			Glyph:   svgDot,
			Hash:    shortHash(c.Hash),
			Message: firstLine(c.Message),
			When:    c.Timestamp,
		})
	}

	// Produced files, each carrying its embedded diff (hunks only).
	for _, f := range data.Files {
		added, removed := countChanges(f.Hunks)
		ad := AnnotatedDiff{File: f, Mode: DiffUnified, Language: languageFor(f.Path), Embedded: true}
		var buf bytes.Buffer
		var diff template.HTML
		if err := ad.Render(&buf); err == nil {
			diff = template.HTML(buf.String())
		}
		s.Files = append(s.Files, spineFile{
			Change:  string(f.Change),
			Path:    f.Path,
			Added:   added,
			Removed: removed,
			Diff:    diff,
		})
	}

	if !s.has() {
		return nil
	}
	return s
}

// collectAncestors returns ancestor nodes ordered origin-first (deepest at the
// top), so ancestry reads as flowing down into the pivot.
func collectAncestors(chain []lineage.Node) []spineRef {
	type withDepth struct {
		ref   spineRef
		depth int
	}
	var items []withDepth
	for _, n := range chain {
		// Direct ancestry only (depth 1): the work item's own track, plan,
		// blockers, and session. Deeper hops pull in sibling features via
		// track→contains and read as noise in a recap; the full graph stays
		// available through `wipnote lineage`.
		if n.Direction != "ancestor" || n.Depth != 1 {
			continue
		}
		items = append(items, withDepth{refFromNode(n), n.Depth})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].depth > items[b].depth })
	out := make([]spineRef, len(items))
	for i, it := range items {
		out[i] = it.ref
	}
	return out
}

// collectRelated returns only direct (depth-1) downstream nodes, avoiding the
// sibling explosion that deeper part_of→contains traversal produces.
func collectRelated(chain []lineage.Node) []spineRef {
	var out []spineRef
	for _, n := range chain {
		if n.Direction == "descendant" && n.Depth == 1 {
			out = append(out, refFromNode(n))
		}
	}
	return out
}

func refFromNode(n lineage.Node) spineRef {
	return spineRef{
		Glyph: glyphFor(n.Type),
		Kind:  n.Type,
		ID:    n.ID,
		Title: n.Title,
		Edge:  humanEdge(n.EdgeType),
	}
}

// countChanges sums TRUE added/removed line counts across a file's hunks,
// counting only +/- lines (not context). It prefers the kind-tagged Hunk.Lines
// and falls back to Before/After lengths for fixtures that predate Lines (those
// carry no context, so the lengths are already correct).
func countChanges(hunks []recap.Hunk) (added, removed int) {
	for _, h := range hunks {
		if len(h.Lines) > 0 {
			for _, l := range h.Lines {
				switch l.Kind {
				case recap.DiffAdd:
					added++
				case recap.DiffDel:
					removed++
				}
			}
			continue
		}
		added += len(h.After)
		removed += len(h.Before)
	}
	return added, removed
}

// kindFromID maps a work-item id prefix to a canonical kind.
func kindFromID(id string) string {
	switch {
	case strings.HasPrefix(id, "feat-"):
		return "feature"
	case strings.HasPrefix(id, "bug-"):
		return "bug"
	case strings.HasPrefix(id, "spk-"):
		return "spike"
	case strings.HasPrefix(id, "trk-"):
		return "track"
	case strings.HasPrefix(id, "plan-"):
		return "plan"
	case strings.HasPrefix(id, "recap-"):
		return "recap"
	case strings.HasPrefix(id, "sess-"):
		return "session"
	default:
		return "item"
	}
}

// humanEdge turns a graph edge_type into a short connector label.
func humanEdge(edge string) string {
	return strings.ReplaceAll(edge, "_", " ")
}

// shortHash truncates a commit hash to 9 chars for display.
func shortHash(h string) string {
	if len(h) > 9 {
		return h[:9]
	}
	return h
}

// firstLine returns the subject line of a commit message.
func firstLine(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return strings.TrimSpace(msg[:i])
	}
	return strings.TrimSpace(msg)
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
