package plantmpl

import (
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/shakestzd/wipnote/plan/blocks"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

var visualOverviewTmpl = template.Must(
	template.ParseFS(templateFS, "templates/visual_overview_zone.gohtml"),
)

// overviewBlock is one rendered block in the visual overview, carrying slice
// attribution and the stable anchor so the rendered block links down to its
// owning slice card.
type overviewBlock struct {
	// Anchor is the stable slice-<num>-block-<type>-<idx> id (same id used by
	// the per-slice BlocksZone), so the overview anchor links to the owning block.
	Anchor string
	// Kind is the block type (e.g. "data-model", "api-endpoint").
	Kind string
	// SliceNum is the ordinal number of the owning slice.
	SliceNum int
	// SliceTitle is the human-readable title of the owning slice.
	SliceTitle string
	// HTML is the pre-rendered block markup from the shared blocks package.
	HTML template.HTML
}

// overviewGroup collects all rendered blocks of the same type under one header.
type overviewGroup struct {
	// TypeLabel is the display label for the block type (e.g. "Data Models").
	TypeLabel string
	// Kind is the raw block type key.
	Kind   string
	Blocks []overviewBlock
}

// typeLabel converts a raw block type key into a human-readable plural heading.
func typeLabel(kind string) string {
	switch kind {
	case "data-model":
		return "Data Models"
	case "api-endpoint":
		return "API Endpoints"
	case "file-tree":
		return "File Trees"
	case "wireframe":
		return "Wireframes"
	case "diagram":
		return "Diagrams"
	case "tabs":
		return "Tabs"
	default:
		return strings.ReplaceAll(kind, "-", " ")
	}
}

// VisualOverviewZone aggregates every block from every slice into a single
// "headline" zone grouped by block type. It renders ABOVE the dependency graph
// so blocks are the first thing a reviewer sees when opening the plan.
//
// Grouping choice: by block type (all data-models together, all api-endpoints
// together, etc.). This lets reviewers scan the full API surface or entity
// catalog at a glance — a cross-slice concern that complements the per-slice
// detail view below.
//
// Back-compat: emits nothing when all slices have zero blocks, preserving
// legacy plan rendering unchanged.
type VisualOverviewZone struct {
	// Slices is the ordered list of plan slices. Blocks are extracted from each.
	Slices []planyaml.PlanSlice
}

// HasBlocks reports whether any slice carries at least one block.
func (z *VisualOverviewZone) HasBlocks() bool {
	for _, s := range z.Slices {
		if len(s.Blocks) > 0 {
			return true
		}
	}
	return false
}

// groups builds the ordered list of type-grouped overview blocks.
// Order follows the BlockCatalog declaration so the section sequence is stable.
func (z *VisualOverviewZone) groups() ([]overviewGroup, error) {
	// catalogOrder defines the section sequence — one group per block type.
	catalogOrder := make([]string, 0, len(planyaml.BlockCatalog()))
	for _, spec := range planyaml.BlockCatalog() {
		catalogOrder = append(catalogOrder, spec.Type)
	}

	byType := make(map[string][]overviewBlock, len(catalogOrder))

	for _, slice := range z.Slices {
		// perType tracks the 1-based index per block type within this slice,
		// matching the blockAnchor() logic in blocks_zone.go exactly.
		perType := map[string]int{}
		for _, b := range slice.Blocks {
			perType[b.Type]++
			anchor := blockAnchor(slice.Num, b.Type, perType[b.Type])
			blk := adapt(b)
			if blk == nil {
				continue
			}
			// Seed the overview copy of a tabs block with a DISTINCT seed so
			// its radio name/id values do not collide with the per-slice copy
			// rendered by BlocksZone lower on the same page. The data-block-anchor
			// and href link still point at the ORIGINAL slice anchor so the
			// overview card links down to the right block.
			if t, ok := blk.(*blocks.Tabs); ok {
				t.Seed = "overview-" + anchor
			}
			var sb strings.Builder
			if err := blk.Render(&sb); err != nil {
				return nil, fmt.Errorf("slice %d block %s: %w", slice.Num, b.Type, err)
			}
			ob := overviewBlock{
				Anchor:     anchor,
				Kind:       b.Type,
				SliceNum:   slice.Num,
				SliceTitle: slice.Title,
				HTML:       template.HTML(sb.String()),
			}
			byType[b.Type] = append(byType[b.Type], ob)
		}
	}

	result := make([]overviewGroup, 0, len(catalogOrder))
	for _, kind := range catalogOrder {
		blks := byType[kind]
		if len(blks) == 0 {
			continue
		}
		result = append(result, overviewGroup{
			TypeLabel: typeLabel(kind),
			Kind:      kind,
			Blocks:    blks,
		})
	}
	return result, nil
}

// Render writes the visual overview HTML to w. Emits nothing when there are no
// blocks across all slices (back-compat: legacy plans render unchanged).
func (z *VisualOverviewZone) Render(w io.Writer) error {
	if !z.HasBlocks() {
		return nil
	}
	gs, err := z.groups()
	if err != nil {
		return err
	}
	if len(gs) == 0 {
		return nil
	}
	return visualOverviewTmpl.ExecuteTemplate(w, "visual_overview_zone", struct {
		Groups []overviewGroup
	}{Groups: gs})
}
