package interview

import "testing"

func TestForComplexity_StageCounts(t *testing.T) {
	cases := map[string]int{
		"trivial":  0,
		"standard": 3,
		"complex":  4,
		"":         3, // unset defaults to standard (mirrors effectiveComplexity)
		"bogus":    3, // unknown defaults to standard
	}
	for complexity, want := range cases {
		got := len(ForComplexity(complexity))
		if got != want {
			t.Errorf("ForComplexity(%q): got %d stages, want %d", complexity, got, want)
		}
	}
}

func TestStageBlockPlan_MapsStagesToBlockTypes(t *testing.T) {
	cases := map[string][]string{
		"scope":        {"file-tree"},
		"contract":     {"api-endpoint", "data-model"},
		"requirements": {"wireframe", "diagram"},
		"donewhen":     nil,
		"unknown":      nil,
	}
	for key, want := range cases {
		got := StageBlockPlan(key)
		if len(got) != len(want) {
			t.Errorf("StageBlockPlan(%q): got %d prompts, want %d", key, len(got), len(want))
			continue
		}
		for i, ty := range want {
			if got[i].Type != ty {
				t.Errorf("StageBlockPlan(%q)[%d].Type = %q, want %q", key, i, got[i].Type, ty)
			}
			if got[i].Prompt == "" {
				t.Errorf("StageBlockPlan(%q)[%d] has empty prompt", key, i)
			}
		}
	}
}

func TestForComplexity_BucketsCoverThreeFields(t *testing.T) {
	// Complex runs all stages; every stage must target a known decisions bucket.
	valid := map[Bucket]bool{BucketScope: true, BucketDecisions: true, BucketContext: true}
	for _, st := range ForComplexity("complex") {
		if !valid[st.Bucket] {
			t.Errorf("stage %q has invalid bucket %q", st.Key, st.Bucket)
		}
		if len(st.Questions) == 0 {
			t.Errorf("stage %q has no questions", st.Key)
		}
	}
}

func TestCompose_RoutesAnswersIntoBuckets(t *testing.T) {
	stages := ForComplexity("complex")
	// Answer the first question of each stage with a deterministic value,
	// keyed by the stage/question id the form posts back.
	answers := map[string]string{}
	for _, st := range stages {
		answers[st.Questions[0].ID] = "ANS-" + string(st.Bucket)
		answers["note:"+st.Key] = "free text for " + st.Key
	}

	scope, decisions, contextStr := Compose(stages, answers)

	// Scope & state -> Scope bucket; Requirements + Contract -> Decisions;
	// Done-when -> Context. Each non-empty bucket must carry its answers.
	if scope == "" || decisions == "" || contextStr == "" {
		t.Fatalf("expected all three buckets populated; got scope=%q decisions=%q context=%q", scope, decisions, contextStr)
	}
	// The free-text note for a Scope-bucketed stage must land in scope.
	for _, st := range stages {
		if st.Bucket == BucketScope {
			if !contains(scope, "free text for "+st.Key) {
				t.Errorf("scope bucket missing free-text note for stage %q; scope=%q", st.Key, scope)
			}
		}
	}
}

func TestCompose_EmptyAnswersYieldEmptyBuckets(t *testing.T) {
	stages := ForComplexity("standard")
	scope, decisions, contextStr := Compose(stages, map[string]string{})
	if scope != "" || decisions != "" || contextStr != "" {
		t.Errorf("empty answers should yield empty buckets; got %q/%q/%q", scope, decisions, contextStr)
	}
}

func TestPlanIntakeStages_LeadsWithTriage(t *testing.T) {
	stages := PlanIntakeStages()
	if len(stages) == 0 || stages[0].Key != "triage" {
		t.Fatalf("intake must lead with a triage stage; got %+v", stages)
	}
	if stages[0].Questions[0].ID != QIntakeComplexity {
		t.Errorf("first question should be the complexity assessment, got %q", stages[0].Questions[0].ID)
	}
}

func TestComposeIntake_MapsAnswersToDesign(t *testing.T) {
	res := ComposeIntake(map[string]string{
		QIntakeComplexity:  "Complex — system design, multiple unknowns",
		QIntakeProblem:     "users can't review plans cross-harness",
		QIntakeGoals:       "ship web form\n keep it portable \n",
		QIntakeConstraints: "no new runtime deps",
	})
	if res.Complexity != "complex" {
		t.Errorf("complexity = %q, want complex", res.Complexity)
	}
	if res.Problem != "users can't review plans cross-harness" {
		t.Errorf("problem = %q", res.Problem)
	}
	if len(res.Goals) != 2 || res.Goals[0] != "ship web form" || res.Goals[1] != "keep it portable" {
		t.Errorf("goals not split/trimmed: %#v", res.Goals)
	}
	if len(res.Constraints) != 1 || res.Constraints[0] != "no new runtime deps" {
		t.Errorf("constraints = %#v", res.Constraints)
	}

	// "Skip" and unknown classify to empty (no forced complexity).
	if c := ComposeIntake(map[string]string{QIntakeComplexity: "Skip — I'll paste the spec"}).Complexity; c != "" {
		t.Errorf("skip should classify to empty, got %q", c)
	}
}

func TestBuildForSlice_AppendsOpenQuestions(t *testing.T) {
	base := len(ForComplexity("complex"))
	open := []Question{{ID: "slicequestion.sq-1", Header: "sq-1", Prompt: "GraphQL or REST?", Type: Text}}
	stages := BuildForSlice("complex", open)
	if len(stages) != base+1 {
		t.Fatalf("got %d stages, want %d (template + 1 open-questions stage)", len(stages), base+1)
	}
	last := stages[len(stages)-1]
	if last.Key != "slice-questions" || last.Bucket != BucketDecisions || len(last.Questions) != 1 {
		t.Errorf("open-questions stage wrong: %+v", last)
	}
	// No open questions → identical to ForComplexity.
	if len(BuildForSlice("standard", nil)) != len(ForComplexity("standard")) {
		t.Error("BuildForSlice with no open questions should equal ForComplexity")
	}
}

func TestParseDefinition_ValidRoundTripsAndComposes(t *testing.T) {
	js := `{"stages":[
	  {"key":"req","title":"Requirements","bucket":"Decisions","questions":[
	    {"id":"req.0","header":"Goal","prompt":"What are we building?","type":"choice",
	     "options":[{"label":"A","description":"first"},{"label":"B"}]}
	  ]},
	  {"key":"acc","title":"Done-when","bucket":"Context","questions":[
	    {"id":"acc.0","header":"Acceptance","prompt":"How verified?","type":"text","placeholder":"e.g. unit test"}
	  ]}
	]}`
	stages, err := ParseDefinition([]byte(js))
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2", len(stages))
	}
	if stages[0].Bucket != BucketDecisions || stages[1].Bucket != BucketContext {
		t.Errorf("buckets not parsed: %q %q", stages[0].Bucket, stages[1].Bucket)
	}
	// The parsed stages must compose like any other interview.
	_, decisions, contextStr := Compose(stages, map[string]string{"req.0": "A", "acc.0": "covered"})
	if !contains(decisions, "Goal: A") || !contains(contextStr, "Acceptance: covered") {
		t.Errorf("compose routed wrong: decisions=%q context=%q", decisions, contextStr)
	}
}

func TestParseDefinition_Rejects(t *testing.T) {
	cases := map[string]string{
		"no stages":        `{"stages":[]}`,
		"bad bucket":       `{"stages":[{"key":"k","title":"T","bucket":"Nope","questions":[{"id":"k.0","prompt":"p","options":[{"label":"A"}]}]}]}`,
		"no questions":     `{"stages":[{"key":"k","title":"T","bucket":"Scope","questions":[]}]}`,
		"missing id":       `{"stages":[{"key":"k","title":"T","bucket":"Scope","questions":[{"prompt":"p","options":[{"label":"A"}]}]}]}`,
		"duplicate id":     `{"stages":[{"key":"k","title":"T","bucket":"Scope","questions":[{"id":"d","prompt":"p","options":[{"label":"A"}]},{"id":"d","prompt":"q","options":[{"label":"B"}]}]}]}`,
		"choice no option": `{"stages":[{"key":"k","title":"T","bucket":"Scope","questions":[{"id":"k.0","prompt":"p","type":"choice"}]}]}`,
		"unknown type":     `{"stages":[{"key":"k","title":"T","bucket":"Scope","questions":[{"id":"k.0","prompt":"p","type":"slider"}]}]}`,
		"bad json":         `{not json`,
	}
	for name, js := range cases {
		if _, err := ParseDefinition([]byte(js)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestParseDefinition_DefaultsTypeToChoice(t *testing.T) {
	js := `{"stages":[{"key":"k","title":"T","bucket":"Scope","questions":[{"id":"k.0","prompt":"p","options":[{"label":"A"}]}]}]}`
	stages, err := ParseDefinition([]byte(js))
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	if stages[0].Questions[0].Type != Choice {
		t.Errorf("type default = %q, want choice", stages[0].Questions[0].Type)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
