package planyaml

import (
	"strings"
	"testing"
)

// fiftyCharsNotes is a >=50 char placeholder used to satisfy the
// triage-gated decisions_notes requirement in test fixtures.
const fiftyCharsNotes = "Rationale captured during the staged interview phase."

// validPlan returns a fully-populated valid plan for use in tests.
func validPlan() *PlanYAML {
	return &PlanYAML{
		Meta: PlanMeta{
			ID:     "plan-abc12345",
			Title:  "Test Plan",
			Status: "draft",
		},
		Design: PlanDesign{
			Problem:     "A real problem to solve.",
			Goals:       []string{"Goal 1"},
			Constraints: []string{"Constraint 1"},
		},
		Slices: []PlanSlice{
			{
				Num:            1,
				What:           "Build the thing.",
				Why:            "Because it matters.",
				Files:          []string{"internal/foo/bar.go"},
				DoneWhen:       []string{"Tests pass"},
				Tests:          "Unit: it works",
				Effort:         "S",
				Risk:           "Low",
				Deps:           []int{},
				DecisionsNotes: fiftyCharsNotes,
			},
			{
				Num:            2,
				What:           "Integrate the thing.",
				Why:            "Because end-to-end matters.",
				Files:          []string{"internal/foo/baz.go"},
				DoneWhen:       []string{"Integration test passes"},
				Tests:          "Integration: full flow works",
				Effort:         "M",
				Risk:           "Med",
				Deps:           []int{1},
				DecisionsNotes: fiftyCharsNotes,
			},
		},
		Questions: []PlanQuestion{
			{
				Text:        "Which approach?",
				Description: "We need to decide between A and B.",
				Recommended: "opt-a",
				Options: []QuestionOption{
					{Key: "opt-a", Label: "Option A"},
					{Key: "opt-b", Label: "Option B"},
				},
			},
		},
	}
}

func TestValidate_ValidPlan(t *testing.T) {
	plan := validPlan()
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidate_EmptyPlan_NoSlices(t *testing.T) {
	// A plan with no slices and no questions is valid as long as meta/design are okay.
	plan := &PlanYAML{
		Meta: PlanMeta{
			ID:     "plan-empty123",
			Title:  "Empty Plan",
			Status: "draft",
		},
		Design: PlanDesign{
			Problem:     "A problem.",
			Goals:       []string{"Goal 1"},
			Constraints: []string{"Constraint 1"},
		},
		Slices:    []PlanSlice{},
		Questions: []PlanQuestion{},
	}
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("expected no errors for empty plan, got: %v", errs)
	}
}

func TestValidate_MissingMetaID(t *testing.T) {
	plan := validPlan()
	plan.Meta.ID = ""
	errs := Validate(plan)
	assertContainsError(t, errs, "meta.id")
}

func TestValidate_MissingMetaTitle(t *testing.T) {
	plan := validPlan()
	plan.Meta.Title = ""
	errs := Validate(plan)
	assertContainsError(t, errs, "meta.title")
}

func TestValidate_InvalidMetaStatus(t *testing.T) {
	plan := validPlan()
	plan.Meta.Status = "pending"
	errs := Validate(plan)
	assertContainsError(t, errs, "meta.status")
}

func TestValidate_ValidMetaStatuses(t *testing.T) {
	for _, status := range []string{"draft", "review", "finalized"} {
		plan := validPlan()
		plan.Meta.Status = status
		errs := Validate(plan)
		for _, e := range errs {
			if strings.Contains(e, "meta.status") {
				t.Errorf("status %q should be valid, got error: %s", status, e)
			}
		}
	}
}

func TestValidate_MissingDesignProblem(t *testing.T) {
	plan := validPlan()
	plan.Design.Problem = ""
	errs := Validate(plan)
	assertContainsError(t, errs, "design.problem")
}

func TestValidate_MissingDesignGoals(t *testing.T) {
	plan := validPlan()
	plan.Design.Goals = []string{}
	errs := Validate(plan)
	assertContainsError(t, errs, "design.goals")
}

func TestValidate_MissingDesignConstraints(t *testing.T) {
	plan := validPlan()
	plan.Design.Constraints = []string{}
	errs := Validate(plan)
	assertContainsError(t, errs, "design.constraints")
}

func TestValidate_SliceMissingWhat(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].What = ""
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].what")
}

func TestValidate_SliceMissingWhy(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].Why = ""
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].why")
}

func TestValidate_SliceMissingFiles(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].Files = []string{}
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].files")
}

func TestValidate_SliceMissingDoneWhen(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].DoneWhen = []string{}
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].done_when")
}

func TestValidate_SliceMissingTests(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].Tests = ""
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].tests")
}

func TestValidate_SliceInvalidEffort(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].Effort = "XL"
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].effort")
}

func TestValidate_SliceValidEfforts(t *testing.T) {
	for _, effort := range []string{"S", "M", "L"} {
		plan := validPlan()
		plan.Slices[0].Effort = effort
		errs := Validate(plan)
		for _, e := range errs {
			if strings.Contains(e, "slices[0].effort") {
				t.Errorf("effort %q should be valid, got error: %s", effort, e)
			}
		}
	}
}

func TestValidate_SliceInvalidRisk(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].Risk = "Critical"
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].risk")
}

func TestValidate_SliceValidRisks(t *testing.T) {
	for _, risk := range []string{"Low", "Med", "High"} {
		plan := validPlan()
		plan.Slices[0].Risk = risk
		errs := Validate(plan)
		for _, e := range errs {
			if strings.Contains(e, "slices[0].risk") {
				t.Errorf("risk %q should be valid, got error: %s", risk, e)
			}
		}
	}
}

func TestValidate_DuplicateSliceNums(t *testing.T) {
	plan := validPlan()
	plan.Slices[1].Num = 1 // duplicate of slice[0].Num
	errs := Validate(plan)
	assertContainsError(t, errs, "duplicate")
}

func TestValidate_SliceDepsNonexistentNum(t *testing.T) {
	plan := validPlan()
	plan.Slices[1].Deps = []int{99} // num 99 doesn't exist
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[1].deps")
}

func TestValidate_SliceSelfReferencingDep(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].Deps = []int{1} // slice num=1 referencing itself
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].deps")
}

func TestValidate_QuestionMissingText(t *testing.T) {
	plan := validPlan()
	plan.Questions[0].Text = ""
	errs := Validate(plan)
	assertContainsError(t, errs, "questions[0].text")
}

func TestValidate_QuestionMissingDescription(t *testing.T) {
	plan := validPlan()
	plan.Questions[0].Description = ""
	errs := Validate(plan)
	assertContainsError(t, errs, "questions[0].description")
}

func TestValidate_QuestionFewerThanTwoOptions(t *testing.T) {
	plan := validPlan()
	plan.Questions[0].Options = []QuestionOption{
		{Key: "opt-a", Label: "Option A"},
	}
	errs := Validate(plan)
	assertContainsError(t, errs, "questions[0].options")
}

func TestValidate_QuestionNoOptions(t *testing.T) {
	plan := validPlan()
	plan.Questions[0].Options = []QuestionOption{}
	errs := Validate(plan)
	assertContainsError(t, errs, "questions[0].options")
}

func TestValidate_QuestionInvalidRecommended(t *testing.T) {
	plan := validPlan()
	plan.Questions[0].Recommended = "nonexistent-key"
	errs := Validate(plan)
	assertContainsError(t, errs, "questions[0].recommended")
}

func TestValidate_QuestionEmptyRecommendedIsValid(t *testing.T) {
	plan := validPlan()
	plan.Questions[0].Recommended = ""
	errs := Validate(plan)
	for _, e := range errs {
		if strings.Contains(e, "questions[0].recommended") {
			t.Errorf("empty recommended should be valid, got error: %s", e)
		}
	}
}

// assertContainsError checks that at least one error message contains the given substring.
func assertContainsError(t *testing.T, errs []string, substr string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return
		}
	}
	t.Errorf("expected an error containing %q, got: %v", substr, errs)
}

// ---- v2 slice-card tests ----

// validV2Plan returns a fully-populated valid v2 plan with slice-local
// questions, critic_revisions, and lifecycle states.
func validV2Plan() *PlanYAML {
	return &PlanYAML{
		Meta: PlanMeta{
			ID:     "plan-v2test01",
			Title:  "V2 Slice-Card Test Plan",
			Status: "active",
		},
		Design: PlanDesign{
			Problem:     "A real problem to solve.",
			Goals:       []string{"Goal 1"},
			Constraints: []string{"Constraint 1"},
		},
		Slices: []PlanSlice{
			{
				Num:             1,
				What:            "Build the thing.",
				Why:             "Because it matters.",
				Files:           []string{"internal/foo/bar.go"},
				DoneWhen:        []string{"Tests pass"},
				Tests:           "Unit: it works",
				Effort:          "S",
				Risk:            "Low",
				Deps:            []int{},
				DecisionsNotes:  fiftyCharsNotes,
				ApprovalStatus:  "approved",
				ExecutionStatus: "done",
				Questions: []SliceQuestion{
					{
						ID:   "sq-1",
						Text: "Should we use interface{}?",
					},
				},
				CriticRevisions: []CriticRevision{
					{
						Source:   "haiku",
						Severity: "LOW",
						Summary:  "Minor style nit.",
					},
				},
			},
			{
				Num:             2,
				What:            "Integrate the thing.",
				Why:             "Because end-to-end matters.",
				Files:           []string{"internal/foo/baz.go"},
				DoneWhen:        []string{"Integration test passes"},
				Tests:           "Integration: full flow works",
				Effort:          "M",
				Risk:            "Med",
				Deps:            []int{1},
				DecisionsNotes:  fiftyCharsNotes,
				ApprovalStatus:  "pending",
				ExecutionStatus: "not_started",
				Questions:       []SliceQuestion{},
				CriticRevisions: []CriticRevision{},
			},
		},
		Questions: []PlanQuestion{
			{
				Text:        "Which approach?",
				Description: "We need to decide between A and B.",
				Recommended: "opt-a",
				Options: []QuestionOption{
					{Key: "opt-a", Label: "Option A"},
					{Key: "opt-b", Label: "Option B"},
				},
			},
		},
	}
}

func TestValidate_V2Plan_Valid(t *testing.T) {
	plan := validV2Plan()
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid v2 plan, got: %v", errs)
	}
}

func TestValidate_MetaStatusActive(t *testing.T) {
	plan := validPlan()
	plan.Meta.Status = "active"
	errs := Validate(plan)
	for _, e := range errs {
		if strings.Contains(e, "meta.status") {
			t.Errorf("status 'active' should be valid, got error: %s", e)
		}
	}
}

func TestValidate_MetaStatusCompleted(t *testing.T) {
	plan := validPlan()
	plan.Meta.Status = "completed"
	errs := Validate(plan)
	for _, e := range errs {
		if strings.Contains(e, "meta.status") {
			t.Errorf("status 'completed' should be valid, got error: %s", e)
		}
	}
}

func TestValidate_AllMetaStatuses(t *testing.T) {
	for _, status := range []string{"draft", "review", "finalized", "active", "completed"} {
		plan := validPlan()
		plan.Meta.Status = status
		errs := Validate(plan)
		for _, e := range errs {
			if strings.Contains(e, "meta.status") {
				t.Errorf("status %q should be valid, got error: %s", status, e)
			}
		}
	}
}

func TestValidate_DuplicateSliceIDs(t *testing.T) {
	plan := validPlan()
	plan.Slices[0].ID = "feat-duplicate"
	plan.Slices[1].ID = "feat-duplicate"
	errs := Validate(plan)
	assertContainsError(t, errs, "duplicate")
}

func TestValidate_DuplicateQuestionIDsWithinSlice(t *testing.T) {
	plan := validV2Plan()
	plan.Slices[0].Questions = []SliceQuestion{
		{ID: "sq-dup", Text: "First question"},
		{ID: "sq-dup", Text: "Second question"},
	}
	errs := Validate(plan)
	assertContainsError(t, errs, "duplicate")
}

func TestValidate_CriticRevisionMissingSource(t *testing.T) {
	plan := validV2Plan()
	plan.Slices[0].CriticRevisions = []CriticRevision{
		{Source: "", Severity: "HIGH", Summary: "A summary"},
	}
	errs := Validate(plan)
	assertContainsError(t, errs, "source")
}

func TestValidate_CriticRevisionMissingSeverity(t *testing.T) {
	plan := validV2Plan()
	plan.Slices[0].CriticRevisions = []CriticRevision{
		{Source: "haiku", Severity: "", Summary: "A summary"},
	}
	errs := Validate(plan)
	assertContainsError(t, errs, "severity")
}

func TestValidate_CriticRevisionMissingSummary(t *testing.T) {
	plan := validV2Plan()
	plan.Slices[0].CriticRevisions = []CriticRevision{
		{Source: "haiku", Severity: "HIGH", Summary: ""},
	}
	errs := Validate(plan)
	assertContainsError(t, errs, "summary")
}

func TestValidate_InvalidApprovalStatus(t *testing.T) {
	plan := validV2Plan()
	plan.Slices[0].ApprovalStatus = "unknown-status"
	errs := Validate(plan)
	assertContainsError(t, errs, "approval_status")
}

func TestValidate_ValidApprovalStatuses(t *testing.T) {
	for _, status := range []string{"", "pending", "approved", "rejected", "changes_requested"} {
		plan := validV2Plan()
		plan.Slices[0].ApprovalStatus = status
		errs := Validate(plan)
		for _, e := range errs {
			if strings.Contains(e, "approval_status") {
				t.Errorf("approval_status %q should be valid, got error: %s", status, e)
			}
		}
	}
}

func TestValidate_InvalidExecutionStatus(t *testing.T) {
	plan := validV2Plan()
	plan.Slices[0].ExecutionStatus = "running"
	errs := Validate(plan)
	assertContainsError(t, errs, "execution_status")
}

func TestValidate_ValidExecutionStatuses(t *testing.T) {
	for _, status := range []string{"", "not_started", "promoted", "in_progress", "done", "blocked", "superseded"} {
		plan := validV2Plan()
		plan.Slices[0].ExecutionStatus = status
		errs := Validate(plan)
		for _, e := range errs {
			if strings.Contains(e, "execution_status") {
				t.Errorf("execution_status %q should be valid, got error: %s", status, e)
			}
		}
	}
}

// ---- triage-gated complexity validator tests ----

// minimalPlan returns a non-finalized plan with a single slice in the given
// complexity tier, with only the unconditionally-required fields populated.
// Helper for triage-gated validator tests.
func minimalPlan(_ string, slice PlanSlice) *PlanYAML {
	return &PlanYAML{
		Meta: PlanMeta{
			ID:     "plan-triage001",
			Title:  "Triage Test Plan",
			Status: "draft",
		},
		Design: PlanDesign{
			Problem:     "A problem.",
			Goals:       []string{"Goal 1"},
			Constraints: []string{"Constraint 1"},
		},
		Slices:    []PlanSlice{slice},
		Questions: []PlanQuestion{},
	}
}

func TestValidate_TrivialSlice_MinimalFieldsOK(t *testing.T) {
	// Trivial slice with only the unconditional fields (title/why/files/
	// effort/risk) validates clean — no what/done_when/tests/decisions_notes.
	plan := minimalPlan("trivial", PlanSlice{
		Num:        1,
		Title:      "Trivial change",
		Why:        "Quick polish.",
		Files:      []string{"internal/foo/bar.go"},
		Effort:     "S",
		Risk:       "Low",
		Complexity: "trivial",
		Deps:       []int{},
	})
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("trivial slice with minimal fields should validate clean, got: %v", errs)
	}
}

func TestValidate_TrivialSlice_MissingWhatOK(t *testing.T) {
	plan := minimalPlan("trivial", PlanSlice{
		Num:        1,
		Why:        "Quick polish.",
		Files:      []string{"internal/foo/bar.go"},
		Effort:     "S",
		Risk:       "Low",
		Complexity: "trivial",
	})
	errs := Validate(plan)
	for _, e := range errs {
		if strings.Contains(e, "what") {
			t.Errorf("trivial slice should not require what, got error: %s", e)
		}
	}
}

func TestValidate_EmptyComplexity_DefaultsToStandard(t *testing.T) {
	// A slice with unset Complexity (empty string) should get standard
	// rules — missing `what` errors as it would for an explicit "standard".
	plan := minimalPlan("", PlanSlice{
		Num:            1,
		Why:            "Why this matters.",
		Files:          []string{"internal/foo/bar.go"},
		DoneWhen:       []string{"It works"},
		Tests:          "Unit: works",
		Effort:         "S",
		Risk:           "Low",
		DecisionsNotes: fiftyCharsNotes,
		// What is omitted — should error under standard rules.
	})
	errs := Validate(plan)
	assertContainsError(t, errs, "slices[0].what")
}

func TestValidate_StandardSlice_MissingDecisionsNotes_NotFinalized_Errors(t *testing.T) {
	plan := minimalPlan("standard", PlanSlice{
		Num:        1,
		What:       "Build the standard thing.",
		Why:        "Standard reason.",
		Files:      []string{"internal/foo/bar.go"},
		DoneWhen:   []string{"It works"},
		Tests:      "Unit: works",
		Effort:     "M",
		Risk:       "Med",
		Complexity: "standard",
	})
	errs := Validate(plan)
	assertContainsError(t, errs, "decisions_notes")
}

func TestValidate_StandardSlice_MissingDecisionsNotes_Finalized_OK(t *testing.T) {
	// Back-compat exemption: a finalized plan without decisions_notes is
	// still valid (existing finalized plans pre-date the requirement).
	plan := minimalPlan("standard", PlanSlice{
		Num:        1,
		What:       "Build the standard thing.",
		Why:        "Standard reason.",
		Files:      []string{"internal/foo/bar.go"},
		DoneWhen:   []string{"It works"},
		Tests:      "Unit: works",
		Effort:     "M",
		Risk:       "Med",
		Complexity: "standard",
	})
	plan.Meta.Status = "finalized"
	errs := Validate(plan)
	for _, e := range errs {
		if strings.Contains(e, "decisions_notes") {
			t.Errorf("finalized plan should not require decisions_notes, got error: %s", e)
		}
	}
}

func TestValidate_ComplexSlice_DoneWhenLengthOne_Errors(t *testing.T) {
	plan := minimalPlan("complex", PlanSlice{
		Num:            1,
		What:           "Build the complex thing.",
		Why:            "Complex reason.",
		Files:          []string{"internal/foo/bar.go"},
		DoneWhen:       []string{"Only one criterion"},
		Tests:          "Unit: works",
		Effort:         "L",
		Risk:           "High",
		Complexity:     "complex",
		DecisionsNotes: fiftyCharsNotes,
		Questions: []SliceQuestion{
			{ID: "sq-1", Text: "Which approach?", Answer: "option-a"},
		},
	})
	errs := Validate(plan)
	assertContainsError(t, errs, "done_when must have at least 2")
}

func TestValidate_ComplexSlice_NoAnsweredQuestion_Errors(t *testing.T) {
	plan := minimalPlan("complex", PlanSlice{
		Num:            1,
		What:           "Build the complex thing.",
		Why:            "Complex reason.",
		Files:          []string{"internal/foo/bar.go"},
		DoneWhen:       []string{"Criterion 1", "Criterion 2"},
		Tests:          "Unit: works",
		Effort:         "L",
		Risk:           "High",
		Complexity:     "complex",
		DecisionsNotes: fiftyCharsNotes,
		Questions: []SliceQuestion{
			{ID: "sq-1", Text: "Which approach?", Answer: ""},
		},
	})
	errs := Validate(plan)
	assertContainsError(t, errs, "non-empty answer")
}

func TestValidate_ComplexSlice_Valid(t *testing.T) {
	plan := minimalPlan("complex", PlanSlice{
		Num:            1,
		What:           "Build the complex thing.",
		Why:            "Complex reason.",
		Files:          []string{"internal/foo/bar.go"},
		DoneWhen:       []string{"Criterion 1", "Criterion 2"},
		Tests:          "Unit: works",
		Effort:         "L",
		Risk:           "High",
		Complexity:     "complex",
		DecisionsNotes: fiftyCharsNotes,
		Questions: []SliceQuestion{
			{ID: "sq-1", Text: "Which approach?", Answer: "option-a"},
		},
	})
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("valid complex slice should validate clean, got: %v", errs)
	}
}

func TestValidate_InvalidComplexity_Errors(t *testing.T) {
	plan := minimalPlan("medium", PlanSlice{
		Num:            1,
		What:           "Build the thing.",
		Why:            "Reason.",
		Files:          []string{"internal/foo/bar.go"},
		DoneWhen:       []string{"It works"},
		Tests:          "Unit: works",
		Effort:         "S",
		Risk:           "Low",
		Complexity:     "medium",
		DecisionsNotes: fiftyCharsNotes,
	})
	errs := Validate(plan)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "complexity") && strings.Contains(e, "medium") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error mentioning complexity and value 'medium', got: %v", errs)
	}
}

func TestValidate_ExistingFinalizedPlan_1c14d560(t *testing.T) {
	// Regression: the on-disk plan-1c14d560.yaml (status=finalized) must
	// continue to validate clean after the triage-gated rules ship — it
	// pre-dates decisions_notes and ships finalized.
	plan, err := Load("../../.wipnote/plans/plan-1c14d560.yaml")
	if err != nil {
		t.Fatalf("failed to load plan-1c14d560.yaml: %v", err)
	}
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("plan-1c14d560.yaml (finalized) must validate clean, got: %v", errs)
	}
}

func TestValidate_LegacyDraftPlan_NoComplexity_NoDecisionsNotes(t *testing.T) {
	// Legacy non-finalized plans that predate the complexity field must not be
	// required to supply decisions_notes. When slice.Complexity == "" the
	// decisions_notes gate is skipped entirely, preserving back-compat.
	plan := &PlanYAML{
		Meta: PlanMeta{
			ID:     "plan-legacy02",
			Title:  "Legacy Draft Plan",
			Status: "draft",
		},
		Design: PlanDesign{
			Problem:     "An old problem.",
			Goals:       []string{"Legacy goal"},
			Constraints: []string{"Legacy constraint"},
		},
		Slices: []PlanSlice{
			{
				Num:      1,
				What:     "Do the legacy thing.",
				Why:      "Because legacy.",
				Files:    []string{"internal/legacy/foo.go"},
				DoneWhen: []string{"It works"},
				Tests:    "Manual: smoke test",
				Effort:   "M",
				Risk:     "High",
				Deps:     []int{},
				// Complexity and DecisionsNotes intentionally absent — legacy plan.
			},
		},
		Questions: []PlanQuestion{},
	}
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("legacy draft plan without complexity/decisions_notes should validate clean, got: %v", errs)
	}
}

// ---- schema_version=v3 strict-model tests ----

// v3MinimalPlan returns a non-finalized plan with schema_version=v3 and a
// single slice whose Complexity is intentionally left empty (defaults to
// "standard" via effectiveComplexity). All standard mandatory fields except
// decisions_notes are populated so tests can toggle that field independently.
func v3MinimalPlan(decisionsNotes string) *PlanYAML {
	return &PlanYAML{
		Meta: PlanMeta{
			ID:            "plan-v3test01",
			Title:         "V3 Strict Test Plan",
			Status:        "draft",
			SchemaVersion: "v3",
		},
		Design: PlanDesign{
			Problem:     "A problem.",
			Goals:       []string{"Goal 1"},
			Constraints: []string{"Constraint 1"},
		},
		Slices: []PlanSlice{
			{
				Num:            1,
				What:           "Build the thing.",
				Why:            "Because it matters.",
				Files:          []string{"internal/foo/bar.go"},
				DoneWhen:       []string{"Tests pass"},
				Tests:          "Unit: it works",
				Effort:         "S",
				Risk:           "Low",
				Deps:           []int{},
				DecisionsNotes: decisionsNotes,
				// Complexity intentionally absent — defaults to "standard".
			},
		},
		Questions: []PlanQuestion{},
	}
}

func TestValidate_V3Plan_StandardSliceWithoutComplexity_RequiresDecisionsNotes(t *testing.T) {
	// v3 strict model: slice with no complexity field (defaults to standard)
	// and no decisions_notes must produce an error.
	plan := v3MinimalPlan("")
	errs := Validate(plan)
	assertContainsError(t, errs, "decisions_notes")
}

func TestValidate_V3Plan_StandardSliceWithoutComplexity_WithDecisionsNotes_Passes(t *testing.T) {
	// v3 strict model: slice with no complexity field and sufficient
	// decisions_notes must validate clean.
	plan := v3MinimalPlan(fiftyCharsNotes)
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("v3 plan with decisions_notes should validate clean, got: %v", errs)
	}
}

func TestValidate_V3Plan_ExplicitTrivialComplexity_NoDecisionsNotesRequired(t *testing.T) {
	// v3 strict model: trivial slices are always exempt from decisions_notes,
	// regardless of schema_version.
	plan := &PlanYAML{
		Meta: PlanMeta{
			ID:            "plan-v3triv01",
			Title:         "V3 Trivial Test",
			Status:        "draft",
			SchemaVersion: "v3",
		},
		Design: PlanDesign{
			Problem:     "A problem.",
			Goals:       []string{"Goal 1"},
			Constraints: []string{"Constraint 1"},
		},
		Slices: []PlanSlice{
			{
				Num:        1,
				Why:        "Quick polish.",
				Files:      []string{"internal/foo/bar.go"},
				Effort:     "S",
				Risk:       "Low",
				Complexity: "trivial",
				Deps:       []int{},
				// No decisions_notes — trivial is exempt.
			},
		},
		Questions: []PlanQuestion{},
	}
	errs := Validate(plan)
	for _, e := range errs {
		if strings.Contains(e, "decisions_notes") {
			t.Errorf("trivial slice in v3 plan should not require decisions_notes, got error: %s", e)
		}
	}
}

func TestValidate_InvalidSchemaVersion_Rejected(t *testing.T) {
	// Any non-empty schema_version other than "v3" must be rejected.
	for _, bad := range []string{"v2", "future", "1"} {
		plan := v3MinimalPlan(fiftyCharsNotes)
		plan.Meta.SchemaVersion = bad
		errs := Validate(plan)
		found := false
		for _, e := range errs {
			if strings.Contains(e, "schema_version") && strings.Contains(e, bad) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("schema_version %q should be rejected with a message containing the bad value; got: %v", bad, errs)
		}
	}
}

func TestValidate_LegacyPlanRegression(t *testing.T) {
	// A legacy plan (no v2 fields, status=finalized) should still validate
	// without errors. The triage-gated decisions_notes requirement is
	// exempted for finalized plans for back-compat with historical plans
	// that pre-date the field (e.g., plan-1c14d560).
	plan := &PlanYAML{
		Meta: PlanMeta{
			ID:     "plan-legacy01",
			Title:  "Legacy Plan",
			Status: "finalized",
		},
		Design: PlanDesign{
			Problem:     "An old problem.",
			Goals:       []string{"Legacy goal"},
			Constraints: []string{"Legacy constraint"},
		},
		Slices: []PlanSlice{
			{
				Num:      1,
				What:     "Do the legacy thing.",
				Why:      "Because legacy.",
				Files:    []string{"internal/legacy/foo.go"},
				DoneWhen: []string{"It works"},
				Tests:    "Manual: smoke test",
				Effort:   "M",
				Risk:     "High",
				Deps:     []int{},
			},
		},
		Questions: []PlanQuestion{},
	}
	errs := Validate(plan)
	if len(errs) != 0 {
		t.Errorf("legacy plan should validate without errors, got: %v", errs)
	}
}
