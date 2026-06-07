package plantmpl_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

// ---------------------------------------------------------------------------
// FinalizePreview.Render — structural output
// ---------------------------------------------------------------------------

func TestFinalizePreviewRenderContainsSectionCard(t *testing.T) {
	fp := &plantmpl.FinalizePreview{}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `id="finalize-preview"`) {
		t.Error(`output missing id="finalize-preview"`)
	}
	if !strings.Contains(html, `class="section-card"`) {
		t.Error(`output missing class="section-card"`)
	}
}

// ---------------------------------------------------------------------------
// ApprovedCount helper
// ---------------------------------------------------------------------------

func TestApprovedCountAllApproved(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		Features: []plantmpl.PreviewFeature{
			{Name: "Auth", Approved: true},
			{Name: "Search", Approved: true},
		},
	}
	if got := fp.ApprovedCount(); got != 2 {
		t.Errorf("ApprovedCount: got %d, want 2", got)
	}
}

func TestApprovedCountNoneApproved(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		Features: []plantmpl.PreviewFeature{
			{Name: "Auth", Approved: false},
			{Name: "Search", Approved: false},
		},
	}
	if got := fp.ApprovedCount(); got != 0 {
		t.Errorf("ApprovedCount: got %d, want 0", got)
	}
}

func TestApprovedCountMixed(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		Features: []plantmpl.PreviewFeature{
			{Name: "Auth", Approved: true},
			{Name: "Search", Approved: false},
			{Name: "Cache", Approved: true},
		},
	}
	if got := fp.ApprovedCount(); got != 2 {
		t.Errorf("ApprovedCount: got %d, want 2", got)
	}
}

func TestApprovedCountEmpty(t *testing.T) {
	fp := &plantmpl.FinalizePreview{}
	if got := fp.ApprovedCount(); got != 0 {
		t.Errorf("ApprovedCount empty: got %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// FinalizePreview.Render — table with feature rows
// ---------------------------------------------------------------------------

func TestFinalizePreviewRenderFeatureNames(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		Features: []plantmpl.PreviewFeature{
			{Name: "Auth middleware", Deps: "feat-001", Approved: true},
			{Name: "User profile", Deps: "", Approved: false},
		},
	}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Auth middleware") {
		t.Error("output missing feature name 'Auth middleware'")
	}
	if !strings.Contains(html, "User profile") {
		t.Error("output missing feature name 'User profile'")
	}
}

func TestFinalizePreviewRenderFeatureDeps(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		Features: []plantmpl.PreviewFeature{
			{Name: "Auth", Deps: "feat-001,feat-002", Approved: true},
		},
	}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "feat-001,feat-002") {
		t.Error("output missing dependencies 'feat-001,feat-002'")
	}
}

func TestFinalizePreviewRenderApprovedStatus(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		Features: []plantmpl.PreviewFeature{
			{Name: "Auth", Approved: true},
		},
	}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Approved") {
		t.Error("output missing 'Approved' status for approved feature")
	}
	if !strings.Contains(html, "badge-approved") {
		t.Error("output missing 'badge-approved' class for approved feature")
	}
}

// ---------------------------------------------------------------------------
// FinalizePreview.Render — unapproved features get strikethrough
// ---------------------------------------------------------------------------

func TestFinalizePreviewRenderUnapprovedStrikethrough(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		Features: []plantmpl.PreviewFeature{
			{Name: "Skipped feature", Approved: false},
		},
	}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "line-through") {
		t.Error("unapproved feature should have line-through styling")
	}
	if !strings.Contains(html, "Skipped") {
		t.Error("unapproved feature should show 'Skipped' status")
	}
}

// ---------------------------------------------------------------------------
// FinalizePreview.Render — empty Features list shows placeholder
// ---------------------------------------------------------------------------

func TestFinalizePreviewRenderEmptyFeaturesPlaceholder(t *testing.T) {
	fp := &plantmpl.FinalizePreview{}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "No features to preview") {
		t.Error("empty Features should show placeholder message")
	}
	// Table should not appear when no features
	if strings.Contains(html, "<table") {
		t.Error("empty Features should not render a table")
	}
}

// ---------------------------------------------------------------------------
// FinalizePreview.Render — TrackID rendering
// ---------------------------------------------------------------------------

func TestFinalizePreviewRenderTrackIDPresent(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		TrackID: "trk-abc123",
	}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "trk-abc123") {
		t.Error("output missing TrackID 'trk-abc123'")
	}
	if !strings.Contains(html, "<code>") {
		t.Error("TrackID should be wrapped in a code element")
	}
}

func TestFinalizePreviewRenderTrackIDAbsent(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		TrackID: "",
	}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, "Track:") {
		t.Error("empty TrackID should not render Track label")
	}
}

// ---------------------------------------------------------------------------
// FinalizePreview.Render — summary badge count
// ---------------------------------------------------------------------------

func TestFinalizePreviewRenderSummaryBadge(t *testing.T) {
	fp := &plantmpl.FinalizePreview{
		Features: []plantmpl.PreviewFeature{
			{Name: "Auth", Approved: true},
			{Name: "Search", Approved: false},
			{Name: "Cache", Approved: true},
		},
	}

	var buf bytes.Buffer
	if err := fp.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	// Should show "2/3 approved"
	if !strings.Contains(html, "2/3 approved") {
		t.Errorf("summary badge should show '2/3 approved', got: %s", html)
	}
}
