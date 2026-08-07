package plantmpl

import (
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/shakestzd/wipnote/plan/blocks"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

var blocksZoneTmpl = template.Must(
	template.ParseFS(templateFS, "templates/blocks_zone.gohtml"),
)

// renderedBlock pairs one block's anchor id with its rendered HTML. The anchor
// follows the slice-8 dashboard contract (slice-<num>-block-<name>-<idx>) so the
// annotation dropdown can target individual plan blocks.
type renderedBlock struct {
	Anchor string
	Kind   string
	HTML   template.HTML
}

// BlocksZone renders a slice's structured visual blocks (data-model,
// api-endpoint, file-tree, wireframe) by adapting each planyaml.SliceBlock into
// the matching shared plan/blocks renderer. Plan and recap therefore share ONE
// block render code path — this zone contains no block markup of its own beyond
// the per-block anchor wrapper.
//
// The zone renders nothing when SliceNum has no blocks, preserving back-compat:
// legacy slices emit no Blocks zone at all.
type BlocksZone struct {
	SliceNum int
	Blocks   []planyaml.SliceBlock
}

// HasBlocks reports whether this zone has any block to render.
func (z *BlocksZone) HasBlocks() bool { return len(z.Blocks) > 0 }

// blockAnchor builds the stable annotation anchor for one block. idx is the
// 1-based position of this block among siblings OF THE SAME TYPE, matching the
// slice-8 pattern slice-\d+-block-[a-z0-9-]+-\d+.
func blockAnchor(sliceNum int, blockType string, idx int) string {
	return fmt.Sprintf("slice-%d-block-%s-%d", sliceNum, blockType, idx)
}

// adapt converts one SliceBlock into the shared blocks.Block renderer. The
// per-block annotation anchor is stamped uniformly by the zone template wrapper
// (see blocks_zone.gohtml) for every block type, so the shared leaf renderers do
// not need anchor awareness — the wireframe renderer's own Anchor field is left
// empty here and is exercised by recap reuse instead. Returns nil for unknown
// types so the zone can skip them gracefully (the validator rejects unknown
// types at author time).
func adapt(b planyaml.SliceBlock) blocks.Block {
	switch b.Type {
	case "data-model":
		return &blocks.DataModel{Name: b.Fields["name"], Columns: rowsToColumns(b.Rows)}
	case "file-tree":
		entries := make([]blocks.FileNode, 0, len(b.Entries))
		for _, e := range b.Entries {
			entries = append(entries, blocks.FileNode{Path: e})
		}
		return &blocks.FileTree{Entries: entries}
	case "wireframe":
		return &blocks.Wireframe{Title: b.Title, Body: b.Fields["html"]}
	case "diagram":
		return &blocks.Diagram{Title: b.Title, Steps: b.Entries, Direction: b.Fields["direction"]}
	case "tabs":
		tabs := make([]blocks.Tab, 0, len(b.Rows))
		for _, r := range b.Rows {
			tabs = append(tabs, blocks.Tab{Label: r["label"], Body: r["body"]})
		}
		return &blocks.Tabs{Title: b.Title, Tabs: tabs}
	default:
		return nil
	}
}

// rowsToColumns maps the generic Rows (name/type/note maps) into the shared
// Column value used by data-model renderer.
func rowsToColumns(rows []map[string]string) []blocks.Column {
	if len(rows) == 0 {
		return nil
	}
	cols := make([]blocks.Column, 0, len(rows))
	for _, r := range rows {
		cols = append(cols, blocks.Column{Name: r["name"], Type: r["type"], Note: r["note"]})
	}
	return cols
}

// render builds the per-block rendered fragments with their anchors.
func (z *BlocksZone) rendered() ([]renderedBlock, error) {
	out := make([]renderedBlock, 0, len(z.Blocks))
	perType := map[string]int{}
	for _, b := range z.Blocks {
		perType[b.Type]++
		anchor := blockAnchor(z.SliceNum, b.Type, perType[b.Type])
		blk := adapt(b)
		if blk == nil {
			continue
		}
		// Seed pure-CSS tabs with the per-block anchor so two tabs blocks with
		// identical labels get distinct radio groups (no cross-toggling).
		if t, ok := blk.(*blocks.Tabs); ok {
			t.Seed = anchor
		}
		var sb strings.Builder
		if err := blk.Render(&sb); err != nil {
			return nil, err
		}
		out = append(out, renderedBlock{Anchor: anchor, Kind: b.Type, HTML: template.HTML(sb.String())})
	}
	return out, nil
}

// Render writes the blocks zone HTML to w. It emits nothing when the slice has
// no blocks (back-compat for legacy slices).
func (z *BlocksZone) Render(w io.Writer) error {
	if !z.HasBlocks() {
		return nil
	}
	rs, err := z.rendered()
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		return nil
	}
	return blocksZoneTmpl.ExecuteTemplate(w, "blocks_zone", struct{ Blocks []renderedBlock }{Blocks: rs})
}
