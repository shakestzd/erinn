package plantmpl_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

// ---------------------------------------------------------------------------
// AssetRegistry
// ---------------------------------------------------------------------------

func TestAssetRegistryCollectsCSS(t *testing.T) {
	ar := &plantmpl.AssetRegistry{}
	ar.AddCSS(".plan { color: red; }")
	ar.AddCSS("body { margin: 0; }")

	got := ar.CSS()
	if len(got) != 2 {
		t.Fatalf("CSS count: got %d, want 2", len(got))
	}
	if got[0] != ".plan { color: red; }" {
		t.Errorf("CSS[0]: got %q", got[0])
	}
}

func TestAssetRegistryCollectsJS(t *testing.T) {
	ar := &plantmpl.AssetRegistry{}
	ar.AddJS("console.log('a');")
	ar.AddJS("console.log('b');")

	got := ar.JS()
	if len(got) != 2 {
		t.Fatalf("JS count: got %d, want 2", len(got))
	}
}

func TestAssetRegistryEmptyByDefault(t *testing.T) {
	ar := &plantmpl.AssetRegistry{}
	if len(ar.CSS()) != 0 {
		t.Errorf("CSS should be empty, got %d", len(ar.CSS()))
	}
	if len(ar.JS()) != 0 {
		t.Errorf("JS should be empty, got %d", len(ar.JS()))
	}
}

// ---------------------------------------------------------------------------
// PlanPage.Render — basic output
// ---------------------------------------------------------------------------

func TestPlanPageRenderContainsPlanID(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID:    "plan-abc123",
		FeatureID: "feat-def456",
		Title:     "Authentication Plan",
		Status:    "draft",
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "plan-abc123") {
		t.Error("output missing PlanID")
	}
	if !strings.Contains(html, "feat-def456") {
		t.Error("output missing FeatureID")
	}
	if !strings.Contains(html, "Authentication Plan") {
		t.Error("output missing Title")
	}
}

func TestPlanPageRenderValidHTML5(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-test",
		Title:  "Test Plan",
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		"<!DOCTYPE html>",
		"<html lang=\"en\">",
		"<meta charset=\"UTF-8\">",
		"</html>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestPlanPageDefaultStatus(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-test",
		Title:  "Default Status Test",
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-status="draft"`) {
		t.Error("default status should be 'draft'")
	}
}

func TestPlanPageSidebarNavLinks(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-nav",
		Title:  "Nav Test",
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	for _, section := range []string{
		"Design", "Outline", "Slices", "Questions", "Critique", "Progress",
	} {
		if !strings.Contains(html, section) {
			t.Errorf("sidebar missing nav link for %q", section)
		}
	}
}

// ---------------------------------------------------------------------------
// PlanPage.Render — nil zone safety
// ---------------------------------------------------------------------------

func TestPlanPageNilZonesDoNotPanic(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-nil",
		Title:  "Nil Zones",
	}
	// All zone fields are nil — should not panic.
	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render with nil zones: %v", err)
	}
}

func TestPlanPageNilSlicesEmpty(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-empty-slices",
		Title:  "Empty Slices",
		Slices: nil,
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Zone stub Render methods
// ---------------------------------------------------------------------------

func TestDependencyGraphRender(t *testing.T) {
	g := &plantmpl.DependencyGraph{
		Nodes: []plantmpl.GraphNode{
			{Num: 1, Name: "Auth", Status: "pending"},
		},
	}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("DependencyGraph.Render: %v", err)
	}
	if !strings.Contains(buf.String(), "dependency-graph") {
		t.Error("expected dependency-graph placeholder")
	}
}

func TestDesignSectionRender(t *testing.T) {
	d := &plantmpl.DesignSection{Content: "<p>Design notes</p>"}

	var buf bytes.Buffer
	if err := d.Render(&buf); err != nil {
		t.Fatalf("DesignSection.Render: %v", err)
	}
	if !strings.Contains(buf.String(), `id="design"`) {
		t.Error("expected design section output")
	}
}

func TestOutlineSectionRender(t *testing.T) {
	o := &plantmpl.OutlineSection{Content: "<p>Outline</p>"}

	var buf bytes.Buffer
	if err := o.Render(&buf); err != nil {
		t.Fatalf("OutlineSection.Render: %v", err)
	}
	if !strings.Contains(buf.String(), `id="outline"`) {
		t.Error("expected outline section output")
	}
}

func TestSliceCardRender(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:   1,
		ID:    "feat-abc123",
		Title: "Auth endpoint",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("SliceCard.Render: %v", err)
	}
	if !strings.Contains(buf.String(), "slice-card") {
		t.Error("expected slice-card placeholder")
	}
}

func TestQuestionsSectionRender(t *testing.T) {
	q := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{
			{ID: "q1", Text: "Which DB?"},
		},
	}

	var buf bytes.Buffer
	if err := q.Render(&buf); err != nil {
		t.Fatalf("QuestionsSection.Render: %v", err)
	}
	if !strings.Contains(buf.String(), `id="questions"`) {
		t.Error("expected questions section output")
	}
}

func TestCritiqueZoneRender(t *testing.T) {
	c := &plantmpl.CritiqueZone{}

	var buf bytes.Buffer
	if err := c.Render(&buf); err != nil {
		t.Fatalf("CritiqueZone.Render: %v", err)
	}
	if !strings.Contains(buf.String(), `id="critique"`) {
		t.Error("expected critique zone output")
	}
}

func TestFinalizePreviewRender(t *testing.T) {
	fp := &plantmpl.FinalizePreview{TrackID: "trk-test"}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("FinalizePreview.Render: %v", err)
	}
	if !strings.Contains(buf.String(), "finalize-preview") {
		t.Error("expected finalize-preview placeholder")
	}
}

func TestProgressBarRender(t *testing.T) {
	pb := &plantmpl.ProgressBar{Approved: 3, Total: 10, Pending: 7}

	var buf bytes.Buffer
	if err := pb.Render(&buf); err != nil {
		t.Fatalf("ProgressBar.Render: %v", err)
	}
	if !strings.Contains(buf.String(), "progress-bar") {
		t.Error("expected progress-bar placeholder")
	}
}

// ---------------------------------------------------------------------------
// Slice-3: PlanPage v2 vs legacy section visibility
// ---------------------------------------------------------------------------

func TestPlanPage_V2_HidesGlobalQuestionsAndCritique(t *testing.T) {
	// A v2 plan: has slices with Questions — global sections should be suppressed.
	page := &plantmpl.PlanPage{
		PlanID: "plan-v2-test",
		Title:  "V2 Plan",
		IsV2:   true,
		Slices: []plantmpl.SliceCard{
			{
				Num:   1,
				ID:    "feat-s1",
				Title: "Slice 1",
			},
		},
		// Provide a Questions zone — should be hidden for v2
		Questions: &plantmpl.QuestionsSection{
			Cards: []plantmpl.DecisionCard{
				{ID: "q1", Text: "Which approach?"},
			},
		},
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	// For v2, global questions section should not be rendered as the main review flow
	if strings.Contains(html, `id="questions"`) {
		t.Error("v2 plan: global questions section should be hidden")
	}
}

func TestPlanPage_Legacy_StillRendersGlobalSections(t *testing.T) {
	// A legacy plan: IsV2 is false — global Questions and Critique should still render.
	page := &plantmpl.PlanPage{
		PlanID: "plan-legacy-test",
		Title:  "Legacy Plan",
		IsV2:   false,
		Questions: &plantmpl.QuestionsSection{
			Cards: []plantmpl.DecisionCard{
				{ID: "q1", Text: "Which approach?"},
			},
		},
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	// Legacy plan must still render global questions
	if !strings.Contains(html, `id="questions"`) {
		t.Error("legacy plan: global questions section should still be rendered")
	}
}
