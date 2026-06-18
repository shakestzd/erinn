package plantmpl

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/planyaml"
)

// anchorRe is the slice-8 dashboard annotation contract: every plan block
// element must carry an id/anchor of this exact shape so the annotation dropdown
// can target it.
var anchorRe = regexp.MustCompile(`^slice-\d+-block-[a-z0-9-]+-\d+$`)

func sliceWithBlocks() planyaml.PlanSlice {
	return planyaml.PlanSlice{
		Num:   3,
		Title: "Block-bearing slice",
		Blocks: []planyaml.SliceBlock{
			{Type: "data-model", Fields: map[string]string{"name": "RecapData"}, Rows: []map[string]string{
				{"name": "Outcome", "type": "string"},
			}},
			{Type: "api-endpoint", Fields: map[string]string{"method": "POST", "path": "/api/recaps/{id}"}},
			{Type: "file-tree", Entries: []string{"plan/blocks/blocks.go", "plan/plantmpl/plantmpl.go"}},
			{Type: "wireframe", Title: "After", Fields: map[string]string{
				"html": `<div style="color:var(--wf-fg);background:var(--wf-surface)">Sidebar</div>`,
			}},
		},
	}
}

func renderCard(t *testing.T, sc SliceCard) string {
	t.Helper()
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

// TestPlanPage_BlocksZone — a slice WITH blocks renders a Blocks zone via the
// shared renderers; a slice WITHOUT blocks renders no zone (back-compat).
func TestPlanPage_BlocksZone(t *testing.T) {
	withBlocks := renderCard(t, SliceCardFromPlanSlice(sliceWithBlocks()))
	if !strings.Contains(withBlocks, "slice-blocks") {
		t.Fatalf("expected Blocks zone for block-bearing slice:\n%s", withBlocks)
	}
	for _, want := range []string{
		`class="block block-data-model"`,
		`class="block block-api-endpoint"`,
		`class="block block-file-tree"`,
		`class="block block-wireframe"`,
		"RecapData", "/api/recaps/{id}", "plan/blocks/blocks.go", "Sidebar",
	} {
		if !strings.Contains(withBlocks, want) {
			t.Errorf("Blocks zone missing %q\n%s", want, withBlocks)
		}
	}

	// Back-compat: a slice without blocks emits NO Blocks zone.
	plain := planyaml.PlanSlice{Num: 1, Title: "No blocks", What: "do a thing"}
	noBlocks := renderCard(t, SliceCardFromPlanSlice(plain))
	if strings.Contains(noBlocks, "slice-blocks") {
		t.Errorf("legacy slice must not emit a Blocks zone:\n%s", noBlocks)
	}
}

// TestBlocksZone_AnchorContract — every rendered block carries a stable
// slice-<num>-block-<name>-<idx> anchor id (slice-8 contract).
func TestBlocksZone_AnchorContract(t *testing.T) {
	html := renderCard(t, SliceCardFromPlanSlice(sliceWithBlocks()))

	wantAnchors := []string{
		"slice-3-block-data-model-1",
		"slice-3-block-api-endpoint-1",
		"slice-3-block-file-tree-1",
		"slice-3-block-wireframe-1",
	}
	for _, a := range wantAnchors {
		if !anchorRe.MatchString(a) {
			t.Fatalf("test anchor %q does not match the slice-8 pattern", a)
		}
		if !strings.Contains(html, `id="`+a+`"`) {
			t.Errorf("expected block id %q\n%s", a, html)
		}
		if !strings.Contains(html, `data-block-anchor="`+a+`"`) {
			t.Errorf("expected data-block-anchor %q\n%s", a, html)
		}
	}
}

// TestBlocksZone_PerTypeIndexing — multiple blocks of the same type get
// monotonic 1-based indices so anchors stay unique.
func TestBlocksZone_PerTypeIndexing(t *testing.T) {
	s := planyaml.PlanSlice{
		Num: 5,
		Blocks: []planyaml.SliceBlock{
			{Type: "file-tree", Entries: []string{"a.go"}},
			{Type: "file-tree", Entries: []string{"b.go"}},
		},
	}
	html := renderCard(t, SliceCardFromPlanSlice(s))
	for _, a := range []string{"slice-5-block-file-tree-1", "slice-5-block-file-tree-2"} {
		if !strings.Contains(html, `id="`+a+`"`) {
			t.Errorf("expected per-type index anchor %q\n%s", a, html)
		}
	}
}

// TestBlocksZone_WireframeRejectsRawColors — the shared renderer rejects raw
// colors inside the plan Blocks zone too (consistent with the validator).
func TestBlocksZone_WireframeRejectsRawColors(t *testing.T) {
	s := planyaml.PlanSlice{
		Num: 2,
		Blocks: []planyaml.SliceBlock{
			{Type: "wireframe", Fields: map[string]string{"html": `<div style="color:#ff0000">x</div>`}},
		},
	}
	html := renderCard(t, SliceCardFromPlanSlice(s))
	if !strings.Contains(html, "wireframe-error") {
		t.Errorf("expected raw-color wireframe to be rejected in the Blocks zone:\n%s", html)
	}
}
