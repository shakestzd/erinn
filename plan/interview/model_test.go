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
