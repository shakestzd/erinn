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

// ---------------------------------------------------------------------------
// Slice-4: feedback POST error-handling script hooks (bug-29d285b2)
// ---------------------------------------------------------------------------

// TestPlanPage_FeedbackError_ToastFunction asserts that the rendered page
// contains the showFeedbackError helper, which surfaces non-2xx responses to
// the user via a visible toast.
func TestPlanPage_FeedbackError_ToastFunction(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-err-toast",
		Title:  "Error Toast Test",
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "showFeedbackError") {
		t.Error("rendered page missing showFeedbackError function — non-2xx responses will be silent")
	}
	// The toast must carry an ARIA role so screen-readers and automated
	// tests can locate it.
	if !strings.Contains(html, `role','alert'`) && !strings.Contains(html, `role="alert"`) {
		t.Error("showFeedbackError toast missing role=alert")
	}
}

// TestPlanPage_FeedbackError_RevertApproval asserts that the rendered page
// contains the revertApproval helper, which restores control state after a
// failed POST.
func TestPlanPage_FeedbackError_RevertApproval(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-err-revert",
		Title:  "Revert Approval Test",
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "revertApproval") {
		t.Error("rendered page missing revertApproval function — failed POSTs will not revert the control")
	}
}

// TestPlanPage_FeedbackError_ApprovalServerState asserts that the rendered
// page tracks approvalServerState to record the last server-confirmed value,
// enabling accurate reverts.
func TestPlanPage_FeedbackError_ApprovalServerState(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-err-serverstate",
		Title:  "Server State Test",
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "approvalServerState") {
		t.Error("rendered page missing approvalServerState tracking — revert baseline will always be 'pending'")
	}
}

// TestPlanPage_FeedbackError_LoadingFlagSupressesPosts asserts that the
// rendered page sets _loadingFeedback during hydration so that synthetic
// change events from loadExistingFeedback do not re-POST server state back.
func TestPlanPage_FeedbackError_LoadingFlagSupressesPosts(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-err-loadflag",
		Title:  "Loading Flag Test",
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "_loadingFeedback") {
		t.Error("rendered page missing _loadingFeedback guard — hydration will echo server state as new POSTs")
	}
}

// TestPlanPage_FeedbackError_FetchHandlesNon2xx asserts that all three
// feedback fetch paths (approval, answer, comment) use .then/.catch rather
// than fire-and-forget, ensuring non-2xx responses are handled.
func TestPlanPage_FeedbackError_FetchHandlesNon2xx(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-err-fetch",
		Title:  "Fetch Error Test",
		Slices: []plantmpl.SliceCard{
			{Num: 1, ID: "feat-s1", Title: "Slice 1"},
		},
		IsV2: true,
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	// Verify that fetch calls chain .then() for non-2xx detection. A bare
	// fire-and-forget fetch() would have no .then( immediately after the
	// closing paren of the options object.
	if !strings.Contains(html, ".then(function(r)") {
		t.Error("feedback fetch calls missing .then(function(r)) — non-2xx responses will be swallowed")
	}
	// Verify console.error is emitted on failure paths.
	if !strings.Contains(html, "console.error('[wipnote]") {
		t.Error("feedback error paths missing console.error — failures will be completely silent")
	}
	// Verify stale-HTML hydration clears sections the server reports as pending.
	if !strings.Contains(html, "approvalServerState[sec]='pending'") {
		t.Error("loadExistingFeedback missing pending-section clearing — stale baked HTML can show wrong state")
	}
}

func TestPlanPage_ApprovalPostsSelectedValue(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-approval-value",
		Title:  "Approval Value Test",
		Slices: []plantmpl.SliceCard{{Num: 1, ID: "feat-s1", Title: "Slice 1"}},
		IsV2:   true,
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, `value:(val==='approved'?'true':'false')`) {
		t.Error("approval POST still collapses segmented states to boolean strings")
	}
	if !strings.Contains(html, "value:val") {
		t.Error("approval POST does not persist the selected segmented value")
	}
}

func TestPlanPage_RadioPendingClearsSelectionAndChatPanelExists(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID: "plan-radio-pending",
		Title:  "Radio Pending Test",
		Slices: []plantmpl.SliceCard{{Num: 1, ID: "feat-s1", Title: "Slice 1"}},
		IsV2:   true,
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	clearSnippet := `document.querySelectorAll('input[type="radio"][data-section="'+sec+'"][data-action="approve"]').forEach`
	if !strings.Contains(html, clearSnippet) {
		t.Error("rendered page does not clear radio approvals when reverting or hydrating pending state")
	}
	if !strings.Contains(html, `id="chat-panel"`) || !strings.Contains(html, `id="chat-messages"`) {
		t.Error("rendered page is missing chat panel markup targeted by navigation and chat script")
	}
}
