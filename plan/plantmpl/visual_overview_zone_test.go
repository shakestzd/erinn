package plantmpl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/planyaml"
)

// multiSlicePlan returns two slices with blocks across multiple types.
func multiSlicePlan() []planyaml.PlanSlice {
	return []planyaml.PlanSlice{
		{
			Num:   1,
			Title: "Auth slice",
			Blocks: []planyaml.SliceBlock{
				{
					Type:   "data-model",
					Fields: map[string]string{"name": "User"},
					Rows:   []map[string]string{{"name": "id", "type": "uuid"}},
				},
				{
					Type:   "api-endpoint",
					Fields: map[string]string{"method": "POST", "path": "/api/login"},
				},
			},
		},
		{
			Num:   2,
			Title: "Storage slice",
			Blocks: []planyaml.SliceBlock{
				{
					Type:   "data-model",
					Fields: map[string]string{"name": "Session"},
					Rows:   []map[string]string{{"name": "token", "type": "string"}},
				},
				{
					Type:    "file-tree",
					Entries: []string{"internal/store/store.go", "internal/store/store_test.go"},
				},
			},
		},
	}
}

// renderOverview renders the VisualOverviewZone and returns the HTML string.
func renderOverview(t *testing.T, z *VisualOverviewZone) string {
	t.Helper()
	var buf bytes.Buffer
	if err := z.Render(&buf); err != nil {
		t.Fatalf("VisualOverviewZone.Render: %v", err)
	}
	return buf.String()
}

// TestVisualOverviewZone_MultiSliceRendersBlocks verifies that a plan with blocks
// across multiple slices renders the visual overview with all blocks grouped by
// type and with the slice attribution and anchors present.
func TestVisualOverviewZone_MultiSliceRendersBlocks(t *testing.T) {
	z := &VisualOverviewZone{Slices: multiSlicePlan()}
	html := renderOverview(t, z)

	// The overview container must be present.
	if !strings.Contains(html, `id="visual-overview"`) {
		t.Error("expected visual-overview section id")
	}

	// Both data-models must appear (grouped together).
	if !strings.Contains(html, "User") {
		t.Error("expected data-model 'User' from slice 1")
	}
	if !strings.Contains(html, "Session") {
		t.Error("expected data-model 'Session' from slice 2")
	}

	// The api-endpoint from slice 1 must appear.
	if !strings.Contains(html, "/api/login") {
		t.Error("expected api-endpoint '/api/login' from slice 1")
	}

	// The file-tree from slice 2 must appear.
	if !strings.Contains(html, "internal/store/store.go") {
		t.Error("expected file-tree entry from slice 2")
	}
}

// TestVisualOverviewZone_AnchorContract verifies that each block in the overview
// carries the same stable slice-<num>-block-<type>-<idx> anchor as the per-slice
// BlocksZone, so the annotation dropdown can target it.
func TestVisualOverviewZone_AnchorContract(t *testing.T) {
	z := &VisualOverviewZone{Slices: multiSlicePlan()}
	html := renderOverview(t, z)

	wantAnchors := []string{
		"slice-1-block-data-model-1",
		"slice-1-block-api-endpoint-1",
		"slice-2-block-data-model-1",
		"slice-2-block-file-tree-1",
	}
	for _, anchor := range wantAnchors {
		if !anchorRe.MatchString(anchor) {
			t.Fatalf("test anchor %q does not match slice-8 pattern", anchor)
		}
		// The anchor appears as the data-block-anchor attribute in the wrapper div.
		if !strings.Contains(html, `data-block-anchor="`+anchor+`"`) {
			t.Errorf("expected data-block-anchor=%q in overview:\n%s", anchor, html)
		}
		// The overview links back to the per-slice block via href=#anchor.
		if !strings.Contains(html, `href="#`+anchor+`"`) {
			t.Errorf("expected href link to #%s in overview:\n%s", anchor, html)
		}
	}
}

// TestVisualOverviewZone_SliceAttribution verifies that each rendered block
// shows which slice it belongs to (#num — title).
func TestVisualOverviewZone_SliceAttribution(t *testing.T) {
	z := &VisualOverviewZone{Slices: multiSlicePlan()}
	html := renderOverview(t, z)

	// Slice 1 attribution.
	if !strings.Contains(html, "#1") || !strings.Contains(html, "Auth slice") {
		t.Errorf("expected slice 1 attribution (#1 / Auth slice) in overview:\n%s", html)
	}
	// Slice 2 attribution.
	if !strings.Contains(html, "#2") || !strings.Contains(html, "Storage slice") {
		t.Errorf("expected slice 2 attribution (#2 / Storage slice) in overview:\n%s", html)
	}
}

// TestVisualOverviewZone_GroupByType verifies that blocks are grouped under
// type headers (Data Models, API Endpoints, File Trees) rather than
// interleaved without structure.
func TestVisualOverviewZone_GroupByType(t *testing.T) {
	z := &VisualOverviewZone{Slices: multiSlicePlan()}
	html := renderOverview(t, z)

	for _, header := range []string{"Data Models", "API Endpoints", "File Trees"} {
		if !strings.Contains(html, header) {
			t.Errorf("expected group header %q in overview:\n%s", header, html)
		}
	}
}

// TestVisualOverviewZone_NoBlocks_EmitsNothing verifies that a plan with no
// blocks across all slices renders an empty string (back-compat: legacy plans
// must not emit a visual overview zone).
func TestVisualOverviewZone_NoBlocks_EmitsNothing(t *testing.T) {
	noBlockSlices := []planyaml.PlanSlice{
		{Num: 1, Title: "Plain slice", What: "do a thing"},
		{Num: 2, Title: "Another slice", What: "do another thing"},
	}
	z := &VisualOverviewZone{Slices: noBlockSlices}
	html := renderOverview(t, z)

	if html != "" {
		t.Errorf("expected empty output for plan with no blocks, got:\n%s", html)
	}
}

// TestVisualOverviewZone_EmptySlices_EmitsNothing verifies that an empty slice
// list also produces no output.
func TestVisualOverviewZone_EmptySlices_EmitsNothing(t *testing.T) {
	z := &VisualOverviewZone{Slices: nil}
	html := renderOverview(t, z)

	if html != "" {
		t.Errorf("expected empty output for nil slices, got:\n%s", html)
	}
}

// TestVisualOverviewZone_PlanPage_WithBlocks verifies that when PlanPage has a
// VisualOverviewZone with blocks, the full page render includes the overview
// section above the dependency graph.
func TestVisualOverviewZone_PlanPage_WithBlocks(t *testing.T) {
	page := &PlanPage{
		PlanID: "plan-visual-test",
		Title:  "Visual Overview Test",
		VisualOverview: &VisualOverviewZone{
			Slices: multiSlicePlan(),
		},
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("PlanPage.Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `id="visual-overview"`) {
		t.Error("page render missing visual-overview section")
	}

	// The visual overview must appear BEFORE the dependency graph.
	overviewIdx := strings.Index(html, `id="visual-overview"`)
	depGraphIdx := strings.Index(html, `data-zone="dependency-graph"`)
	if overviewIdx < 0 {
		t.Fatal("visual-overview section not found")
	}
	if depGraphIdx < 0 {
		t.Fatal("dependency-graph section not found")
	}
	if overviewIdx > depGraphIdx {
		t.Error("visual-overview must appear BEFORE dependency-graph in the page")
	}
}

// TestVisualOverviewZone_PlanPage_NoBlocks verifies that a PlanPage with a
// VisualOverviewZone but no block-bearing slices does not emit the overview
// section in the page output.
func TestVisualOverviewZone_PlanPage_NoBlocks(t *testing.T) {
	page := &PlanPage{
		PlanID: "plan-no-blocks",
		Title:  "No Blocks Test",
		VisualOverview: &VisualOverviewZone{
			Slices: []planyaml.PlanSlice{
				{Num: 1, Title: "Legacy slice", What: "do stuff"},
			},
		},
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("PlanPage.Render: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, `id="visual-overview"`) {
		t.Error("page render must not include visual-overview section when no blocks exist")
	}
}

// TestVisualOverviewZone_PlanPage_NilOverview verifies that PlanPage.Render
// does not panic when VisualOverview is nil (back-compat).
func TestVisualOverviewZone_PlanPage_NilOverview(t *testing.T) {
	page := &PlanPage{
		PlanID:         "plan-nil-overview",
		Title:          "Nil Overview Test",
		VisualOverview: nil,
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("PlanPage.Render with nil VisualOverview: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, `id="visual-overview"`) {
		t.Error("page must not emit visual-overview when VisualOverview is nil")
	}
}
