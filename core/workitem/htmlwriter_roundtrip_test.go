package workitem

import (
	"reflect"
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
// Deliberately excluded: PlanTaskID, SpecRequirements, the Handoff* fields,
// RequiredCapabilities, CapabilityTags, and the Context* fields. None of
// these has ANY writer or parser support today (verified by grep across
// htmlwriter.go, node.gohtml, and parser.go) — that is a separate,
// pre-existing gap unrelated to this bug, not something to paper over by
// leaving them unset and calling it coverage.
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
		// attribute-safe string values (the three live SetProperty call
		// sites) with non-string values that must take the JSON escape
		// hatch to preserve their Go type.
		Properties: map[string]any{
			"standalone_reason":  "pre-enforcement",
			"created_in_session": "019ebc63ba7ae905adb1f8db7504",
			"retry_count":        float64(2),
			"is_flaky":           true,
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
