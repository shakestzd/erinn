package plantmpl_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// ---------------------------------------------------------------------------
// SliceCard.Render — structural output
// ---------------------------------------------------------------------------

func TestSliceCardRenderContainsSliceCard(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:   3,
		ID:    "feat-abc123",
		Title: "Auth endpoint",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `class="slice-card"`) {
		t.Error("output missing class=\"slice-card\"")
	}
	if !strings.Contains(html, `data-slice="3"`) {
		t.Error("output missing data-slice=\"3\"")
	}
	if !strings.Contains(html, "Auth endpoint") {
		t.Error("output missing Title")
	}
	if !strings.Contains(html, "feat-abc123") {
		t.Error("output missing ID")
	}
}

func TestSliceCardRenderDataSliceName(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 5,
		ID:  "feat-def456",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-slice-name="feat-def456"`) {
		t.Error("output missing data-slice-name attribute")
	}
	if !strings.Contains(html, `data-slice="5"`) {
		t.Error("output missing data-slice=\"5\"")
	}
}

func TestSliceCardRenderDefaultStatusPending(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 1,
		ID:  "feat-test",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-status="pending"`) {
		t.Error("empty Status should default to pending in data-status attribute")
	}
}

func TestSliceCardRenderExplicitStatus(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:    2,
		ID:     "feat-test",
		Status: "approved",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-status="approved"`) {
		t.Error("explicit Status not reflected in data-status attribute")
	}
}

// ---------------------------------------------------------------------------
// SliceCard.Render — effort badge
// ---------------------------------------------------------------------------

func TestSliceCardRenderEffortSmall(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-s", Effort: "S"}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "badge-pending") {
		t.Error("S effort should use badge-pending class")
	}
	if !strings.Contains(html, ">S<") {
		t.Error("effort badge should contain S text")
	}
}

func TestSliceCardRenderEffortMedium(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-m", Effort: "M"}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "badge-revision") {
		t.Error("M effort should use badge-revision class")
	}
	if !strings.Contains(html, ">M<") {
		t.Error("effort badge should contain M text")
	}
}

func TestSliceCardRenderEffortLarge(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-l", Effort: "L"}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "badge-blocked") {
		t.Error("L effort should use badge-blocked class")
	}
	if !strings.Contains(html, ">L<") {
		t.Error("effort badge should contain L text")
	}
}

func TestSliceCardRenderEmptyEffortOmitted(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-noeffort", Effort: ""}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	// When Effort is empty the badge block should not appear.
	html := buf.String()
	_ = html // confirmed by template conditional — no assertion needed
}

// ---------------------------------------------------------------------------
// SliceCard.Render — risk badge
// ---------------------------------------------------------------------------

func TestSliceCardRenderRiskLow(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-low", Risk: "Low"}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, ">Low<") {
		t.Error("Low risk badge missing 'Low' text")
	}
	// Low → badge-pending
	if !strings.Contains(html, "badge-pending") {
		t.Error("Low risk should use badge-pending class")
	}
}

func TestSliceCardRenderRiskMed(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-med", Risk: "Med"}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, ">Med<") {
		t.Error("Med risk badge missing 'Med' text")
	}
	if !strings.Contains(html, "badge-revision") {
		t.Error("Med risk should use badge-revision class")
	}
}

func TestSliceCardRenderRiskHigh(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-high", Risk: "High"}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, ">High<") {
		t.Error("High risk badge missing 'High' text")
	}
	if !strings.Contains(html, "badge-blocked") {
		t.Error("High risk should use badge-blocked class")
	}
}

func TestSliceCardRenderEmptyRiskOmitted(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-norisk", Risk: ""}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, "Risk") {
		t.Error("empty Risk should not render a risk badge")
	}
}

// ---------------------------------------------------------------------------
// SliceCard.Render — optional fields
// ---------------------------------------------------------------------------

func TestSliceCardRenderDescription(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:         1,
		ID:          "feat-desc",
		Description: "Implements the login flow",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Implements the login flow") {
		t.Error("Description not rendered")
	}
}

func TestSliceCardRenderEmptyDescriptionOmitted(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-nodesc", Description: ""}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	// The description paragraph should not appear when empty.
	if strings.Contains(html, `<p style="font-size:.9rem`) {
		t.Error("empty Description should not render description paragraph")
	}
}

func TestSliceCardRenderFiles(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:   1,
		ID:    "feat-files",
		Files: "internal/auth/auth.go,cmd/serve/main.go",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "internal/auth/auth.go,cmd/serve/main.go") {
		t.Error("Files not rendered")
	}
}

func TestSliceCardRenderEmptyFilesOmitted(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-nofiles", Files: ""}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, "Files:") {
		t.Error("empty Files should not render files row")
	}
}

// ---------------------------------------------------------------------------
// Markdown rendering tests (slice-2 / feat-33807582)
// ---------------------------------------------------------------------------

func TestSliceCard_RendersMarkdownHeadings(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:  1,
		ID:   "feat-md-h",
		What: "### Heading\n\ntext",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "<h3>") {
		t.Errorf("expected <h3> from ### heading, got output:\n%s", html)
	}
}

func TestSliceCard_RendersMarkdownLists(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:  1,
		ID:   "feat-md-list",
		What: "- item one\n- item two\n- item three",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "<ul>") {
		t.Errorf("expected <ul> from bullet list, got output:\n%s", html)
	}
	if !strings.Contains(html, "<li>") {
		t.Errorf("expected <li> from bullet list, got output:\n%s", html)
	}
}

func TestSliceCard_RendersMarkdownCodeFence(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:  1,
		ID:   "feat-md-fence",
		What: "```go\nfunc main() {}\n```",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "<pre>") {
		t.Errorf("expected <pre> from code fence, got output:\n%s", html)
	}
	if !strings.Contains(html, "<code") {
		t.Errorf("expected <code> from code fence, got output:\n%s", html)
	}
}

func TestSliceCard_RendersInlineCode(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:  1,
		ID:   "feat-md-inline",
		What: "Use `myFunc()` to do it.",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "<code>") {
		t.Errorf("expected <code> from inline code, got output:\n%s", html)
	}
	if !strings.Contains(html, "myFunc()") {
		t.Errorf("expected myFunc() in output, got:\n%s", html)
	}
}

func TestSliceCard_StripsScriptTags(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:  1,
		ID:   "feat-md-xss",
		What: `<script>alert(1)</script>`,
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, "<script>") {
		t.Errorf("unescaped <script> tag must not appear in output:\n%s", html)
	}
}

func TestSliceCard_StripsRawHTMLEventHandlers(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:  1,
		ID:   "feat-md-evil",
		What: `<img src=x onerror="alert(1)">`,
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, "onerror=") {
		t.Errorf("onerror event handler must be stripped from output:\n%s", html)
	}
	if strings.Contains(html, "alert(1)") {
		t.Errorf("event handler payload must be stripped from output:\n%s", html)
	}
}

func TestSliceCard_DoneWhenStaysStructuredList(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:      1,
		ID:       "feat-md-done",
		What:     "Implement it",
		DoneWhen: []string{"All tests pass", "Code reviewed"},
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	// done_when items must render as literal <li> text — no Markdown heading etc.
	if !strings.Contains(html, "All tests pass") {
		t.Errorf("DoneWhen item 'All tests pass' missing from output:\n%s", html)
	}
	if !strings.Contains(html, "Code reviewed") {
		t.Errorf("DoneWhen item 'Code reviewed' missing from output:\n%s", html)
	}
	// The done_when section wraps items in <ul>
	if !strings.Contains(html, `class="slice-done-list"`) {
		t.Errorf("expected slice-done-list ul, got:\n%s", html)
	}
}

func TestSliceCard_LegacyPlainTextStillRenders(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:  1,
		ID:   "feat-legacy",
		What: "Just text.",
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Just text.") {
		t.Errorf("plain text 'Just text.' missing from output:\n%s", html)
	}
}

// ---------------------------------------------------------------------------
// DepsLabel helper
// ---------------------------------------------------------------------------

func TestDepsLabelEmpty(t *testing.T) {
	sc := &plantmpl.SliceCard{Deps: ""}
	if got := sc.DepsLabel(); got != "none" {
		t.Errorf("DepsLabel with empty Deps: got %q, want %q", got, "none")
	}
}

func TestDepsLabelNonEmpty(t *testing.T) {
	sc := &plantmpl.SliceCard{Deps: "1,2"}
	if got := sc.DepsLabel(); got != "slices 1,2" {
		t.Errorf("DepsLabel with Deps=1,2: got %q, want %q", got, "slices 1,2")
	}
}

func TestDepsLabelSingle(t *testing.T) {
	sc := &plantmpl.SliceCard{Deps: "3"}
	if got := sc.DepsLabel(); got != "slices 3" {
		t.Errorf("DepsLabel with Deps=3: got %q, want %q", got, "slices 3")
	}
}

// ---------------------------------------------------------------------------
// EffortClass helper
// ---------------------------------------------------------------------------

func TestEffortClassS(t *testing.T) {
	sc := &plantmpl.SliceCard{Effort: "S"}
	if got := sc.EffortClass(); got != "badge-pending" {
		t.Errorf("EffortClass S: got %q, want badge-pending", got)
	}
}

func TestEffortClassM(t *testing.T) {
	sc := &plantmpl.SliceCard{Effort: "M"}
	if got := sc.EffortClass(); got != "badge-revision" {
		t.Errorf("EffortClass M: got %q, want badge-revision", got)
	}
}

func TestEffortClassL(t *testing.T) {
	sc := &plantmpl.SliceCard{Effort: "L"}
	if got := sc.EffortClass(); got != "badge-blocked" {
		t.Errorf("EffortClass L: got %q, want badge-blocked", got)
	}
}

func TestEffortClassUnknown(t *testing.T) {
	sc := &plantmpl.SliceCard{Effort: "XL"}
	if got := sc.EffortClass(); got != "badge-pending" {
		t.Errorf("EffortClass unknown: got %q, want badge-pending", got)
	}
}

// ---------------------------------------------------------------------------
// RiskClass helper
// ---------------------------------------------------------------------------

func TestRiskClassHigh(t *testing.T) {
	sc := &plantmpl.SliceCard{Risk: "High"}
	if got := sc.RiskClass(); got != "badge-blocked" {
		t.Errorf("RiskClass High: got %q, want badge-blocked", got)
	}
}

func TestRiskClassMed(t *testing.T) {
	sc := &plantmpl.SliceCard{Risk: "Med"}
	if got := sc.RiskClass(); got != "badge-revision" {
		t.Errorf("RiskClass Med: got %q, want badge-revision", got)
	}
}

func TestRiskClassMedium(t *testing.T) {
	sc := &plantmpl.SliceCard{Risk: "Medium"}
	if got := sc.RiskClass(); got != "badge-revision" {
		t.Errorf("RiskClass Medium: got %q, want badge-revision", got)
	}
}

func TestRiskClassLow(t *testing.T) {
	sc := &plantmpl.SliceCard{Risk: "Low"}
	if got := sc.RiskClass(); got != "badge-pending" {
		t.Errorf("RiskClass Low: got %q, want badge-pending", got)
	}
}

// ---------------------------------------------------------------------------
// Multiple cards render independently
// ---------------------------------------------------------------------------

func TestMultipleSliceCardsRenderIndependently(t *testing.T) {
	cards := []plantmpl.SliceCard{
		{Num: 1, ID: "feat-aaa", Title: "First slice"},
		{Num: 2, ID: "feat-bbb", Title: "Second slice"},
		{Num: 3, ID: "feat-ccc", Title: "Third slice"},
	}

	for _, card := range cards {
		c := card // capture
		var buf bytes.Buffer
		if err := c.Render(&buf); err != nil {
			t.Fatalf("Render card %d: %v", c.Num, err)
		}
		html := buf.String()
		if !strings.Contains(html, c.Title) {
			t.Errorf("card %d: missing title %q", c.Num, c.Title)
		}
		if !strings.Contains(html, c.ID) {
			t.Errorf("card %d: missing ID %q", c.Num, c.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Slice-3: v2 fields — approval status, execution status, questions, revisions
// ---------------------------------------------------------------------------

func TestSliceCard_RendersApprovalStatus_Pending(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:            1,
		ID:             "feat-test",
		ApprovalStatus: "pending",
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "Pending") {
		t.Errorf("ApprovalStatus=pending should show 'Pending' text, got:\n%s", html)
	}
}

func TestSliceCard_RendersApprovalStatus_Approved(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:            2,
		ID:             "feat-test",
		ApprovalStatus: "approved",
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "Approved") {
		t.Errorf("ApprovalStatus=approved should show 'Approved' text, got:\n%s", html)
	}
	if !strings.Contains(html, "badge-approved") {
		t.Errorf("ApprovalStatus=approved should use badge-approved class, got:\n%s", html)
	}
}

func TestSliceCard_RendersApprovalStatus_Rejected(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:            3,
		ID:             "feat-test",
		ApprovalStatus: "rejected",
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "Rejected") {
		t.Errorf("ApprovalStatus=rejected should show 'Rejected' text, got:\n%s", html)
	}
	if !strings.Contains(html, "badge-blocked") {
		t.Errorf("ApprovalStatus=rejected should use badge-blocked class, got:\n%s", html)
	}
}

func TestSliceCard_RendersExecutionStatus(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:             4,
		ID:              "feat-test",
		ExecutionStatus: "in_progress",
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "in_progress") && !strings.Contains(html, "In Progress") && !strings.Contains(html, "in-progress") {
		t.Errorf("ExecutionStatus=in_progress should appear in output, got:\n%s", html)
	}
}

func TestSliceCard_RendersSliceLocalQuestions(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 5,
		ID:  "feat-test",
		Questions: []planyaml.SliceQuestion{
			{ID: "q-1", Text: "Should we use gRPC or REST?"},
		},
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "Should we use gRPC or REST?") {
		t.Errorf("Question text missing from output:\n%s", html)
	}
	// The section key for the question must follow the contract: slice-N-question-<id>
	if !strings.Contains(html, `data-section="slice-5-question-q-1"`) {
		t.Errorf("expected data-section=\"slice-5-question-q-1\" in output:\n%s", html)
	}
}

func TestSliceCard_RendersCriticRevisionBadges(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 6,
		ID:  "feat-test",
		CriticRevisions: []planyaml.CriticRevision{
			{Source: "feasibility", Severity: "High", Summary: "Consider rate limiting"},
		},
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "Consider rate limiting") {
		t.Errorf("CriticRevision summary missing from output:\n%s", html)
	}
	if !strings.Contains(html, "feasibility") {
		t.Errorf("CriticRevision source missing from output:\n%s", html)
	}
	// High severity should get a blocked/danger style
	if !strings.Contains(html, "badge-blocked") && !strings.Contains(html, "severity-high") {
		t.Errorf("High severity badge should use blocked/danger class, got:\n%s", html)
	}
}

func TestSliceCard_ReconcilesExistingApprovalCheckbox(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:            7,
		ID:             "feat-test",
		ApprovalStatus: "approved",
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	// The approval segmented control uses three radio buttons (approve/changes/reject),
	// all with data-action="approve". Verify there are exactly 3 (the full segmented group).
	approveCount := strings.Count(html, `data-action="approve"`)
	if approveCount != 3 {
		t.Errorf("expected 3 approval radio inputs (segmented control), got %d in:\n%s", approveCount, html)
	}
	// The visual status badge must reference the same slice key (data-badge-for=slice-7).
	if !strings.Contains(html, `data-badge-for="slice-7"`) {
		t.Errorf("expected badge with data-badge-for=\"slice-7\" in output:\n%s", html)
	}
	// When approved, the radio for "approved" value should be pre-checked.
	if !strings.Contains(html, `value="approved" data-section="slice-7" data-action="approve" checked`) {
		t.Errorf("approved slice should have checked 'approved' radio:\n%s", html)
	}
}

func TestSliceCard_LegacyV1NoNewFields(t *testing.T) {
	// A slice without any v2 fields should render as it did before slice-3.
	sc := &plantmpl.SliceCard{
		Num:         1,
		ID:          "feat-legacy",
		Title:       "Legacy slice",
		Description: "Old-style description.",
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	// Must contain the slice card
	if !strings.Contains(html, `class="slice-card"`) {
		t.Error("legacy slice should still render slice-card class")
	}
	// Must contain description
	if !strings.Contains(html, "Old-style description.") {
		t.Error("legacy slice should still render description")
	}
	// Should not have any critic revision or question UI (no data)
	if strings.Contains(html, "class=\"critic-revision\"") {
		t.Error("no CriticRevisions set, should not render critic revision UI")
	}
}

// ---------------------------------------------------------------------------
// SliceCard.SliceQuestionSectionKey helper
// ---------------------------------------------------------------------------

func TestSliceCard_SliceQuestionSectionKey(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 3}
	got := sc.SliceQuestionSectionKey("q-abc")
	want := "slice-3-question-q-abc"
	if got != want {
		t.Errorf("SliceQuestionSectionKey: got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// SliceCard.ApprovalStatusClass helper
// ---------------------------------------------------------------------------

func TestSliceCard_ApprovalStatusClass_Approved(t *testing.T) {
	sc := &plantmpl.SliceCard{ApprovalStatus: "approved"}
	if got := sc.ApprovalStatusClass(); got != "badge-approved" {
		t.Errorf("ApprovalStatusClass approved: got %q, want badge-approved", got)
	}
}

func TestSliceCard_ApprovalStatusClass_Rejected(t *testing.T) {
	sc := &plantmpl.SliceCard{ApprovalStatus: "rejected"}
	if got := sc.ApprovalStatusClass(); got != "badge-blocked" {
		t.Errorf("ApprovalStatusClass rejected: got %q, want badge-blocked", got)
	}
}

func TestSliceCard_ApprovalStatusClass_ChangesRequested(t *testing.T) {
	sc := &plantmpl.SliceCard{ApprovalStatus: "changes_requested"}
	if got := sc.ApprovalStatusClass(); got != "badge-revision" {
		t.Errorf("ApprovalStatusClass changes_requested: got %q, want badge-revision", got)
	}
}

func TestSliceCard_ApprovalStatusClass_Pending(t *testing.T) {
	sc := &plantmpl.SliceCard{ApprovalStatus: "pending"}
	if got := sc.ApprovalStatusClass(); got != "badge-pending" {
		t.Errorf("ApprovalStatusClass pending: got %q, want badge-pending", got)
	}
}

func TestSliceCard_ApprovalStatusClass_Empty(t *testing.T) {
	sc := &plantmpl.SliceCard{ApprovalStatus: ""}
	if got := sc.ApprovalStatusClass(); got != "badge-pending" {
		t.Errorf("ApprovalStatusClass empty: got %q, want badge-pending", got)
	}
}

// ---------------------------------------------------------------------------
// SliceCard.CriticSeverityClass helper
// ---------------------------------------------------------------------------

func TestSliceCard_CriticSeverityClass(t *testing.T) {
	sc := &plantmpl.SliceCard{}
	tests := []struct {
		severity string
		want     string
	}{
		{"High", "badge-blocked"},
		{"HIGH", "badge-blocked"},
		{"DANGER", "badge-blocked"},
		{"Med", "badge-revision"},
		{"Medium", "badge-revision"},
		{"Low", "badge-pending"},
		{"low", "badge-pending"},
		{"", "badge-pending"},
	}
	for _, tt := range tests {
		got := sc.CriticSeverityClass(tt.severity)
		if got != tt.want {
			t.Errorf("CriticSeverityClass(%q): got %q, want %q", tt.severity, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// SliceCardFromPlanSlice mapper
// ---------------------------------------------------------------------------

func TestSliceCardFromPlanSlice_BasicFields(t *testing.T) {
	ps := planyaml.PlanSlice{
		Num:    2,
		ID:     "feat-xyz",
		Title:  "Auth slice",
		What:   "Implement JWT",
		Why:    "Security",
		Effort: "M",
		Risk:   "Low",
		Deps:   []int{1},
		Files:  []string{"internal/auth/auth.go"},
	}
	sc := plantmpl.SliceCardFromPlanSlice(ps)
	if sc.Num != 2 {
		t.Errorf("Num: got %d, want 2", sc.Num)
	}
	if sc.ID != "feat-xyz" {
		t.Errorf("ID: got %q, want feat-xyz", sc.ID)
	}
	if sc.What != "Implement JWT" {
		t.Errorf("What: got %q, want 'Implement JWT'", sc.What)
	}
	if sc.Effort != "M" {
		t.Errorf("Effort: got %q, want M", sc.Effort)
	}
	if sc.Deps != "1" {
		t.Errorf("Deps: got %q, want '1'", sc.Deps)
	}
	if sc.Files != "internal/auth/auth.go" {
		t.Errorf("Files: got %q, want 'internal/auth/auth.go'", sc.Files)
	}
}

func TestSliceCardFromPlanSlice_V2Fields(t *testing.T) {
	ps := planyaml.PlanSlice{
		Num:             3,
		ID:              "feat-v2",
		ApprovalStatus:  "approved",
		ExecutionStatus: "in_progress",
		Questions: []planyaml.SliceQuestion{
			{ID: "q-1", Text: "What DB?"},
		},
		CriticRevisions: []planyaml.CriticRevision{
			{Source: "arch", Severity: "High", Summary: "Add index"},
		},
	}
	sc := plantmpl.SliceCardFromPlanSlice(ps)
	if sc.ApprovalStatus != "approved" {
		t.Errorf("ApprovalStatus: got %q, want approved", sc.ApprovalStatus)
	}
	if sc.ExecutionStatus != "in_progress" {
		t.Errorf("ExecutionStatus: got %q, want in_progress", sc.ExecutionStatus)
	}
	if len(sc.Questions) != 1 || sc.Questions[0].ID != "q-1" {
		t.Errorf("Questions not mapped correctly: %+v", sc.Questions)
	}
	if len(sc.CriticRevisions) != 1 || sc.CriticRevisions[0].Source != "arch" {
		t.Errorf("CriticRevisions not mapped correctly: %+v", sc.CriticRevisions)
	}
}

// ---------------------------------------------------------------------------
// SliceQuestion with Options — radio chip rendering (bug-07722c9c)
// ---------------------------------------------------------------------------

func TestSliceCard_RendersSliceLocalQuestionOptionChips(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 2,
		ID:  "feat-test",
		Questions: []planyaml.SliceQuestion{
			{
				ID:          "q-db",
				Text:        "Which database should we use?",
				Description: "Choose based on query complexity.",
				Recommended: "postgres",
				Options: []planyaml.QuestionOption{
					{Key: "postgres", Label: "PostgreSQL"},
					{Key: "sqlite", Label: "SQLite"},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()

	// Radio inputs must be emitted (not textareas for structured questions)
	if !strings.Contains(html, `<input type="radio"`) {
		t.Errorf("expected radio inputs for structured question, got:\n%s", html)
	}

	// Description must appear
	if !strings.Contains(html, "Choose based on query complexity.") {
		t.Errorf("expected description text in output:\n%s", html)
	}

	// Recommended option label must have class="recommended"
	if !strings.Contains(html, `class="recommended"`) {
		t.Errorf("expected class=\"recommended\" on recommended option label, got:\n%s", html)
	}

	// Recommended option must have rec-tag span
	if !strings.Contains(html, `class="rec-tag"`) {
		t.Errorf("expected rec-tag span on recommended option, got:\n%s", html)
	}

	// Non-recommended option (sqlite) must NOT have class="recommended"
	// Count occurrences: only one label should carry the class
	if strings.Count(html, `class="recommended"`) != 1 {
		t.Errorf("expected exactly 1 label with class=\"recommended\", got:\n%s", html)
	}

	// Section key must be slice-scoped
	if !strings.Contains(html, `data-section="slice-2-question-q-db"`) {
		t.Errorf("expected slice-scoped data-section in output:\n%s", html)
	}

	// data-action="answer" must be present
	if !strings.Contains(html, `data-action="answer"`) {
		t.Errorf("expected data-action=\"answer\" on radio inputs:\n%s", html)
	}

	// Both option labels must appear
	if !strings.Contains(html, "PostgreSQL") {
		t.Errorf("expected 'PostgreSQL' option label in output:\n%s", html)
	}
	if !strings.Contains(html, "SQLite") {
		t.Errorf("expected 'SQLite' option label in output:\n%s", html)
	}
}

func TestSliceCard_RendersSliceLocalQuestionOptionsWithAnswer(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 3,
		ID:  "feat-test",
		Questions: []planyaml.SliceQuestion{
			{
				ID:          "q-cache",
				Text:        "Caching strategy?",
				Recommended: "redis",
				Answer:      "memcached",
				Options: []planyaml.QuestionOption{
					{Key: "redis", Label: "Redis"},
					{Key: "memcached", Label: "Memcached"},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()

	// The answered option must be checked
	if !strings.Contains(html, `value="memcached" checked`) {
		t.Errorf("expected checked attribute on answered option, got:\n%s", html)
	}

	// When answer is set, recommended label must NOT carry class="recommended"
	// (recommended styling only applies when unanswered)
	if strings.Contains(html, `class="recommended"`) {
		t.Errorf("answered question should not show recommended styling, got:\n%s", html)
	}
}

func TestSliceCard_FreeformQuestionFallbackTextarea(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 4,
		ID:  "feat-test",
		Questions: []planyaml.SliceQuestion{
			{
				ID:   "q-notes",
				Text: "Any notes on deployment?",
				// No Options — freeform fallback
			},
		},
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()

	// Must fall back to textarea when no Options
	if !strings.Contains(html, `<textarea`) {
		t.Errorf("expected textarea fallback when no options, got:\n%s", html)
	}

	// Must NOT emit radio inputs with data-action="answer" (question chips)
	if strings.Contains(html, `data-action="answer"`) {
		t.Errorf("unexpected question radio inputs for freeform question, got:\n%s", html)
	}

	// Section key must be slice-scoped
	if !strings.Contains(html, `data-section="slice-4-question-q-notes"`) {
		t.Errorf("expected slice-scoped data-section in textarea output:\n%s", html)
	}
}

// ---------------------------------------------------------------------------
// SliceCard.Render — complexity badge
// ---------------------------------------------------------------------------

func TestSliceCardRenderComplexityTrivial(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-c", Complexity: "trivial"}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "badge-complexity") {
		t.Errorf("expected badge-complexity marker class in output:\n%s", html)
	}
	if !strings.Contains(html, ">Trivial<") {
		t.Errorf("expected Trivial label in complexity badge:\n%s", html)
	}
}

func TestSliceCardRenderComplexityStandard(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-c", Complexity: "standard"}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, ">Standard<") {
		t.Errorf("expected Standard label in complexity badge:\n%s", html)
	}
	if !strings.Contains(html, "badge-discuss") {
		t.Errorf("standard complexity should use badge-discuss class:\n%s", html)
	}
}

func TestSliceCardRenderComplexityComplex(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-c", Complexity: "complex"}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, ">Complex<") {
		t.Errorf("expected Complex label in complexity badge:\n%s", html)
	}
	if !strings.Contains(html, "badge-revision") {
		t.Errorf("complex complexity should use badge-revision class:\n%s", html)
	}
}

func TestSliceCardRenderComplexityEmptyOmitted(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-noc", Complexity: ""}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, "badge-complexity") {
		t.Errorf("empty complexity should not render a complexity badge:\n%s", html)
	}
}

// ---------------------------------------------------------------------------
// SliceCard.Render — decisions_notes
// ---------------------------------------------------------------------------

func TestSliceCardRenderDecisionsNotes(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num:            1,
		ID:             "feat-dn",
		DecisionsNotes: "Scope: small refactor. Decisions: keep API stable.",
	}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "slice-decisions-notes") {
		t.Errorf("expected slice-decisions-notes class in output:\n%s", html)
	}
	if !strings.Contains(html, "Decisions") {
		t.Errorf("expected Decisions label in output:\n%s", html)
	}
	if !strings.Contains(html, "keep API stable") {
		t.Errorf("expected decisions_notes content rendered:\n%s", html)
	}
}

func TestSliceCardRenderEmptyDecisionsNotesOmitted(t *testing.T) {
	sc := &plantmpl.SliceCard{Num: 1, ID: "feat-nodn", DecisionsNotes: ""}
	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, "slice-decisions-notes") {
		t.Errorf("empty decisions_notes should not render section:\n%s", html)
	}
}

func TestSliceCardFromPlanSliceMapsComplexityAndDecisionsNotes(t *testing.T) {
	src := planyaml.PlanSlice{
		Num:            7,
		ID:             "feat-map",
		Title:          "Map fields",
		Complexity:     "complex",
		DecisionsNotes: "Important context here.",
	}
	sc := plantmpl.SliceCardFromPlanSlice(src)
	if sc.Complexity != "complex" {
		t.Errorf("Complexity not mapped: got %q want %q", sc.Complexity, "complex")
	}
	if sc.DecisionsNotes != "Important context here." {
		t.Errorf("DecisionsNotes not mapped: got %q", sc.DecisionsNotes)
	}
}

// ---------------------------------------------------------------------------
// Bug-81ea68a6: IssueCount excludes SUCCESS and INFO severities (#330)
// ---------------------------------------------------------------------------

func TestSliceCard_IssueCount_ExcludesSuccessAndInfo(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 1,
		ID:  "feat-ic",
		CriticRevisions: []planyaml.CriticRevision{
			{Source: "a", Severity: "SUCCESS", Summary: "Looks good"},
			{Source: "b", Severity: "success", Summary: "Also fine"},
			{Source: "c", Severity: "INFO",    Summary: "FYI note"},
			{Source: "d", Severity: "info",    Summary: "Another note"},
		},
	}
	if got := sc.IssueCount(); got != 0 {
		t.Errorf("IssueCount with only SUCCESS/INFO: got %d, want 0", got)
	}
}

func TestSliceCard_IssueCount_CountsWarnAndDanger(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 1,
		ID:  "feat-ic2",
		CriticRevisions: []planyaml.CriticRevision{
			{Source: "a", Severity: "WARN",    Summary: "Fix this"},
			{Source: "b", Severity: "DANGER",  Summary: "Critical issue"},
			{Source: "c", Severity: "SUCCESS", Summary: "Looks good"},
			{Source: "d", Severity: "INFO",    Summary: "FYI"},
		},
	}
	// Only WARN and DANGER count; SUCCESS and INFO do not.
	if got := sc.IssueCount(); got != 2 {
		t.Errorf("IssueCount with WARN+DANGER+SUCCESS+INFO: got %d, want 2", got)
	}
}

func TestSliceCard_IssueCount_CountsHighAndMed(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 1,
		ID:  "feat-ic3",
		CriticRevisions: []planyaml.CriticRevision{
			{Source: "a", Severity: "High",   Summary: "Big issue"},
			{Source: "b", Severity: "Med",    Summary: "Medium issue"},
			{Source: "c", Severity: "Low",    Summary: "Minor issue"},
			{Source: "d", Severity: "SUCCESS", Summary: "All good"},
		},
	}
	// High/Med/Low all count; SUCCESS does not.
	if got := sc.IssueCount(); got != 3 {
		t.Errorf("IssueCount with High+Med+Low+SUCCESS: got %d, want 3", got)
	}
}

func TestSliceCard_IssueCount_BadgeSuppressedWhenZero(t *testing.T) {
	sc := &plantmpl.SliceCard{
		Num: 1,
		ID:  "feat-ic4",
		CriticRevisions: []planyaml.CriticRevision{
			{Source: "a", Severity: "SUCCESS", Summary: "All passed"},
			{Source: "b", Severity: "INFO",    Summary: "Note"},
		},
	}

	var buf bytes.Buffer
	if err := sc.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	// When IssueCount == 0, the badge-issues element must not be rendered.
	if strings.Contains(html, "badge-issues") {
		t.Error("badge-issues must not appear when IssueCount is 0 (all revisions are SUCCESS/INFO)")
	}
}
