package main

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/interview"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

func TestWritePlanIntake_PopulatesDesign(t *testing.T) {
	dir, planID, _ := seedPlanForElicit(t) // non-git temp; commitPlanChange no-ops
	planPath := filepath.Join(dir, "plans", planID+".yaml")

	err := writePlanIntake(planPath, interview.IntakeResult{
		Complexity:  "complex",
		Problem:     "cross-harness plan review is missing",
		Goals:       []string{"ship the web form", "stay portable"},
		Constraints: []string{"no new runtime deps"},
	})
	if err != nil {
		t.Fatalf("writePlanIntake: %v", err)
	}

	plan, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if plan.Design.Problem != "cross-harness plan review is missing" {
		t.Errorf("problem not written: %q", plan.Design.Problem)
	}
	if len(plan.Design.Goals) != 2 || len(plan.Design.Constraints) != 1 {
		t.Errorf("goals/constraints not written: %#v / %#v", plan.Design.Goals, plan.Design.Constraints)
	}
	if !strings.Contains(plan.Design.Comment, "complexity: complex") {
		t.Errorf("assessed complexity not recorded in Comment: %q", plan.Design.Comment)
	}
}

func TestSliceOpenQuestions_MapsUnansweredOnly(t *testing.T) {
	slice := planyaml.PlanSlice{
		Questions: []planyaml.SliceQuestion{
			{ID: "sq-1", Text: "GraphQL or REST?", Options: []planyaml.QuestionOption{{Key: "gql", Label: "GraphQL"}, {Key: "rest", Label: "REST"}}},
			{ID: "sq-2", Text: "Already decided", Answer: "yes"},     // answered → skipped
			{ID: "sq-3", Text: "Cache TTL?", Description: "seconds"}, // free text
		},
	}
	got := sliceOpenQuestions(slice)
	if len(got) != 2 {
		t.Fatalf("got %d questions, want 2 (answered one skipped)", len(got))
	}
	if got[0].Type != interview.Choice || len(got[0].Options) != 2 {
		t.Errorf("sq-1 should be a 2-option choice; got %+v", got[0])
	}
	if got[1].Type != interview.Text || got[1].ID != "slicequestion.sq-3" {
		t.Errorf("sq-3 should be free text with stable id; got %+v", got[1])
	}
	if got[1].Prompt != "Cache TTL? — seconds" {
		t.Errorf("description not folded into prompt: %q", got[1].Prompt)
	}
	// The built set must survive a JSON round-trip through ParseDefinition
	// (this is what `plan interview-questions` emits and `--questions -` reads).
	stages := interview.BuildForSlice("standard", got)
	blob, err := json.Marshal(interview.Definition{Stages: stages})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := interview.ParseDefinition(blob); err != nil {
		t.Errorf("emitted question set failed ParseDefinition round-trip: %v", err)
	}
}

func TestAttachBlockPrompts_BlocksFirstStagesCarryCatalogGroundedPrompts(t *testing.T) {
	// Complex slice -> all four stages; blocks-first attaches authoring prompts
	// to scope (file-tree), contract (data-model), and
	// requirements (wireframe/diagram). Descriptions come from BlockCatalog, not hardcode.
	stages := attachBlockPrompts(interview.BuildForSlice("complex", nil))

	byKey := map[string]interview.Stage{}
	for _, st := range stages {
		byKey[st.Key] = st
	}

	scope, ok := byKey["scope"]
	if !ok || len(scope.Blocks) != 1 || scope.Blocks[0].Type != "file-tree" {
		t.Fatalf("scope stage should carry a file-tree block prompt, got %+v", scope.Blocks)
	}
	if strings.TrimSpace(scope.Blocks[0].Description) == "" {
		t.Errorf("file-tree prompt should be annotated with the catalog description")
	}

	contract := byKey["contract"]
	types := []string{}
	for _, bp := range contract.Blocks {
		types = append(types, bp.Type)
		if strings.TrimSpace(bp.Description) == "" {
			t.Errorf("contract block %q missing catalog description", bp.Type)
		}
	}
	wantContract := map[string]bool{"data-model": false}
	for _, ty := range types {
		if _, ok := wantContract[ty]; ok {
			wantContract[ty] = true
		}
	}
	for ty, seen := range wantContract {
		if !seen {
			t.Errorf("contract stage missing %q block prompt; got %v", ty, types)
		}
	}

	// Done-when has no natural visual artifact -> no block prompts.
	if dw := byKey["donewhen"]; len(dw.Blocks) != 0 {
		t.Errorf("donewhen stage should carry no block prompts, got %+v", dw.Blocks)
	}

	// Every attached prompt's Type must be a real catalog key (no stale tags).
	known := map[string]bool{}
	for _, spec := range planyaml.BlockCatalog() {
		known[spec.Type] = true
	}
	for _, st := range stages {
		for _, bp := range st.Blocks {
			if !known[bp.Type] {
				t.Errorf("stage %q emitted non-catalog block type %q", st.Key, bp.Type)
			}
		}
	}
}

func TestInterviewChatContext_IncludesFormState(t *testing.T) {
	stages := interview.ForComplexity("complex")
	slice := planyaml.PlanSlice{Num: 2, Title: "Apply guard profiles", What: "Wire guards into all gate sites."}
	answers := map[string]string{
		stages[0].Questions[0].ID: "New capability — feature add",
		"note:" + stages[1].Key:   "state lives in committed html",
	}
	ctx := interviewChatContext("plan-x", 2, slice, stages, answers, stages[1].Key)

	for _, want := range []string{
		"Apply guard profiles",          // slice title
		"Wire guards into all gate",     // slice intent
		"Stage 1 — Requirements",        // stage listing
		"the user is on this stage",     // current-stage marker
		"New capability",                // a rendered option/answer
		"state lives in committed html", // the free-text note
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("interview context missing %q\n---\n%s", want, ctx)
		}
	}

	// the marker must attach to the stage the user is actually on (scope), not stage 1
	scopeLine := lineContaining(ctx, stages[1].Title)
	if !strings.Contains(scopeLine, "the user is on this stage") {
		t.Errorf("marker not on the active stage line: %q", scopeLine)
	}
}

// TestInterviewTemplate_RendersBlockPrompts verifies that the interview web form
// renders Stage.Blocks before the stage questions so the blocks-first authoring
// step is visible to users on the web-form path. It parses and executes the
// embedded template directly, avoiding an HTTP server.
func TestInterviewTemplate_RendersBlockPrompts(t *testing.T) {
	stages := []interview.Stage{
		{
			Key:    "requirements",
			Title:  "Requirements",
			Bucket: interview.BucketDecisions,
			Blocks: []interview.BlockPrompt{
				{
					Type:        "wireframe",
					Description: "An HTML/CSS sketch built from design tokens (no raw hex/rgb colors).",
					Prompt:      "For UI/flow work, sketch the user-visible surface FIRST.",
				},
				{
					Type:        "diagram",
					Description: "A flow diagram: ordered steps connected by arrows.",
					Prompt:      "Author whichever fits the UI/flow.",
				},
			},
			Questions: []interview.Question{
				{ID: "requirements.0", Header: "Goal", Type: interview.Choice,
					Prompt:  "What's the user-visible behavior we're after?",
					Options: []interview.Option{{Label: "New capability"}},
				},
			},
		},
	}
	page := interviewPage{
		PlanID:     "plan-test",
		SliceNum:   1,
		SliceTitle: "Test slice",
		Stages:     stages,
	}
	var buf strings.Builder
	if err := interviewTmpl.Execute(&buf, page); err != nil {
		t.Fatalf("template.Execute: %v", err)
	}
	html := buf.String()

	// Block type labels must appear.
	if !strings.Contains(html, "wireframe") {
		t.Error("expected block type 'wireframe' in rendered HTML")
	}
	if !strings.Contains(html, "diagram") {
		t.Error("expected block type 'diagram' in rendered HTML")
	}
	// Block prompt text must appear.
	if !strings.Contains(html, "sketch the user-visible surface FIRST") {
		t.Error("expected wireframe prompt text in rendered HTML")
	}
	if !strings.Contains(html, "Author whichever fits the UI/flow") {
		t.Error("expected diagram prompt text in rendered HTML")
	}
	// Blocks must render BEFORE questions within the same stage section.
	// Use "blocks-first" container and the question header class as anchors
	// (avoiding the apostrophe in the question prompt, which html/template escapes).
	blocksIdx := strings.Index(html, "blocks-first")
	questionsIdx := strings.Index(html, `class="hdr"`)
	if blocksIdx < 0 {
		t.Fatal("blocks-first container not found")
	}
	if questionsIdx < 0 {
		t.Fatal("question header not found")
	}
	if blocksIdx > questionsIdx {
		t.Error("blocks must appear BEFORE questions in the stage section")
	}
}

func lineContaining(s, sub string) string {
	for ln := range strings.SplitSeq(s, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return ""
}

func TestCollectAnswers_FlattensFormIntoComposableMap(t *testing.T) {
	stages := interview.ForComplexity("complex")
	form := url.Values{}
	// single-choice answer for the first question of the first stage
	form.Set(stages[0].Questions[0].ID, "New capability — feature add")
	// multi-select style: two values under one question id
	form["contract.0"] = []string{"On every tool call", "On explicit CLI invocation only"}
	// a free-text note for one stage
	form.Set("note:"+stages[1].Key, "state lives in the canonical html store")
	// a blank value must be dropped, not stored as empty
	form["donewhen.0"] = []string{"  "}

	got := collectAnswers(form, stages)

	if got[stages[0].Questions[0].ID] != "New capability — feature add" {
		t.Errorf("single choice not captured: %q", got[stages[0].Questions[0].ID])
	}
	if v := got["contract.0"]; !strings.Contains(v, "every tool call") || !strings.Contains(v, "CLI invocation") {
		t.Errorf("multi-select not joined: %q", v)
	}
	if got["note:"+stages[1].Key] != "state lives in the canonical html store" {
		t.Errorf("note not captured: %q", got["note:"+stages[1].Key])
	}
	if _, ok := got["donewhen.0"]; ok {
		t.Errorf("blank answer should be dropped, got %q", got["donewhen.0"])
	}

	// End-to-end: the collected map must Compose into populated buckets.
	scope, decisions, _ := interview.Compose(stages, got)
	if scope == "" || decisions == "" {
		t.Errorf("expected scope+decisions populated; scope=%q decisions=%q", scope, decisions)
	}
}
