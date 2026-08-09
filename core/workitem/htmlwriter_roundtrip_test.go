package workitem

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// TestWriteNodeHTML_FullRoundTrip is the generic regression test called for by
// bug-c65a5f4e's investigation: plan edges going unscanned by reindexEdges,
// edge properties never being rendered, and now node properties never being
// rendered/parsed were each invisible until someone wrote a test that
// round-tripped a fully-populated value through disk and compared. This is
// that test for models.Node — it constructs a Node with every field the
// writer+parser pair currently supports, writes it, parses it back, and
// asserts deep equality, so the *next* field added to models.Node (with a
// setter but no writer/parser wiring — exactly the shape of this bug) fails
// loudly here instead of silently dropping data on first rewrite.
//
// Deliberately excluded — each with a documented reason on its field in
// core/models/node.go, audited in bug-e5c04997: SpecRequirements (the live
// spec-generation path computes its own requirements elsewhere and never
// reads or writes this field), the Handoff* fields (superseded by the
// working handoff mechanism on models.Session), RequiredCapabilities and
// CapabilityTags (no routing logic exists yet to consume them), and the
// Context* fields (superseded by the observe/otel/* pipeline's per-signal
// tracking). None of these has ANY writer or reader anywhere in the
// codebase — wiring HTML persistence for them now would be speculative
// plumbing with nothing to round-trip. This exclusion list should only ever
// shrink, and only when a field gains a real producer or consumer.
//
// PlanTaskID is NOT excluded (fixed in bug-e5c04997, alongside this test):
// it already had a parser half with no writer half — the same shape of bug
// as Properties — so it now round-trips like TrackID.
func TestWriteNodeHTML_FullRoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	node := &models.Node{
		ID:       "feat-fullroundtrip",
		Title:    "Full round-trip smoke test",
		Type:     "feature",
		Status:   models.StatusInProgress,
		Priority: models.PriorityHigh,

		CreatedAt: ts,
		UpdatedAt: ts,

		// map[string]any — the bug this test exists to catch. Mixes
		// attribute-safe string values (the live SetProperty call sites, plus
		// the rollup keys) with non-string values that must take the JSON
		// escape hatch to preserve their Go type.
		//
		// The rollup keys (feat-7ee73444) belong here rather than in a test of
		// their own: outcome rollups are written through Properties precisely
		// so they inherit a proven round-trip, and if that ever stops holding,
		// a completed work item silently loses its numbers on the next
		// rewrite. Hyphenated keys are also the only attribute-safe keys in
		// this map, so they pin that half of the key charset.
		Properties: map[string]any{
			"standalone_reason":            "pre-enforcement",
			"created_in_session":           "019ebc63ba7ae905adb1f8db7504",
			"retry_count":                  float64(2),
			"is_flaky":                     true,
			"rollup-failure-rate":          "0.3333",
			"rollup-failure-rate-source":   "otel_success",
			"rollup-failure-rate-coverage": "3/5",
			"rollup-cost-usd":              "1.2500",
			"rollup-cost-usd-source":       "otel_cost_usd:degraded_under_report",
			"rollup-telemetry":             "unavailable",
			"rollup-computed-at":           "2026-08-09T00:00:00Z",
		},

		Edges: map[string][]models.Edge{
			"blocked_by": {{
				TargetID:     "feat-blocker0",
				Relationship: models.RelationshipType("blocked_by"),
				Title:        "Blocker",
				Since:        ts,
				Properties:   map[string]string{"origin": "plan_slice_deps"},
			}},
		},

		Steps: []models.Step{
			{
				StepID:              "step-1",
				Description:         "First step",
				Completed:           true,
				Agent:               "claude-code",
				CreatedByModel:      "claude-opus-4-7",
				CreatedByRole:       "architect-coder",
				CreatedByCLIVersion: "1.2.3",
			},
			{
				StepID:      "step-2",
				Description: "Second step",
				Completed:   false,
				Agent:       "claude-code",
				DependsOn:   []string{"step-1"},
			},
		},

		Content: "Full round-trip smoke test body.",

		AgentAssigned:    "claude-code",
		ClaimedAt:        "2026-08-06T12:00:00",
		ClaimedBySession: "019ebc63ba7ae905adb1f8db7504",

		TrackID:      "trk-fullroundtrip",
		PlanTaskID:   "plan-fullroundtrip",
		SpikeSubtype: "research",

		CreatedByAgent:      "claude-code",
		CreatedByModel:      "claude-opus-4-7",
		CreatedByRole:       "architect-coder",
		CreatedByCLIVersion: "1.2.3",
	}

	dir := t.TempDir()
	path, err := WriteNodeHTML(dir, node)
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}
	parsed, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if !reflect.DeepEqual(parsed, node) {
		t.Errorf("round-trip mismatch:\n got  %#v\nwant %#v", parsed, node)
	}
}

// TestWriteNodeHTML_PlanTaskID pins the specific fix in bug-e5c04997: the
// parser has read data-plan-task-id since it was added, but nothing ever
// wrote it — so it always came back empty. This checks both the positive
// (rendered and parsed back) and negative (omitted, not a stray empty
// attribute) cases, matching the pattern already pinned for TrackID.
func TestWriteNodeHTML_PlanTaskID(t *testing.T) {
	base := func(planTaskID string) *models.Node {
		return &models.Node{
			ID:         "feat-plantask",
			Title:      "Plan task ID round-trip",
			Type:       "feature",
			Status:     models.StatusTodo,
			Priority:   models.PriorityMedium,
			CreatedAt:  time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC),
			UpdatedAt:  time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC),
			PlanTaskID: planTaskID,
		}
	}

	dir := t.TempDir()
	path, err := WriteNodeHTML(dir, base("plan-1a2b3c4d"))
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}
	html, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(html, `data-plan-task-id="plan-1a2b3c4d"`) {
		t.Errorf("output missing data-plan-task-id\n--- html ---\n%s", html)
	}
	parsed, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if parsed.PlanTaskID != "plan-1a2b3c4d" {
		t.Errorf("PlanTaskID lost on round-trip: got %q", parsed.PlanTaskID)
	}

	dir2 := t.TempDir()
	path2, err := WriteNodeHTML(dir2, base(""))
	if err != nil {
		t.Fatalf("WriteNodeHTML (empty): %v", err)
	}
	html2, err := readFile(path2)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(html2, "data-plan-task-id") {
		t.Errorf("empty PlanTaskID should omit the attribute entirely\n--- html ---\n%s", html2)
	}
}
