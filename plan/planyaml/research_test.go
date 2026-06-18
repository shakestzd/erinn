package planyaml

import (
	"strings"
	"testing"
)

// validV4Plan returns a v4 plan that satisfies the research gate, for mutation.
func validV4Plan() *PlanYAML {
	p := validPlan()
	p.Meta.SchemaVersion = "v4"
	p.Design.Research = []ResearchSource{
		{URL: "https://example.com/docs", Claim: "design basis", Accessed: "2026-06-18"},
	}
	for i := range p.Slices {
		p.Slices[i].Research = []ResearchSource{
			{URL: "https://pkg.go.dev/example", Claim: "candidate package", Accessed: "2026-06-18"},
		}
	}
	return p
}

func errsContain(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestValidate_V4_BaselineValid(t *testing.T) {
	if errs := Validate(validV4Plan()); len(errs) != 0 {
		t.Fatalf("expected a research-complete v4 plan to be valid, got: %v", errs)
	}
}

func TestValidate_V4_SliceMissingResearchIsRejected(t *testing.T) {
	p := validV4Plan()
	p.Slices[0].Research = nil
	if !errsContain(Validate(p), "research must cite") {
		t.Errorf("v4 standard slice without research must be rejected; got %v", Validate(p))
	}
}

func TestValidate_V4_WaiverSatisfiesGate(t *testing.T) {
	p := validV4Plan()
	p.Slices[0].Research = nil
	p.Slices[0].ResearchWaiver = "stdlib only — no external dependency or standard applies"
	if errsContain(Validate(p), "research must cite") {
		t.Errorf("an explicit research_waiver must satisfy the gate; got %v", Validate(p))
	}
}

func TestValidate_V4_TrivialSliceExemptFromResearch(t *testing.T) {
	p := validV4Plan()
	p.Slices[0].Complexity = "trivial"
	p.Slices[0].Research = nil
	if errsContain(Validate(p), "research must cite") {
		t.Errorf("trivial slices are exempt from the research gate; got %v", Validate(p))
	}
}

func TestValidate_V4_DesignMissingResearchIsRejected(t *testing.T) {
	p := validV4Plan()
	p.Design.Research = nil
	if !errsContain(Validate(p), "design.research must cite") {
		t.Errorf("v4 plan without a design research basis must be rejected; got %v", Validate(p))
	}
}

func TestValidate_MalformedResearchURLIsRejected(t *testing.T) {
	p := validV4Plan()
	p.Slices[0].Research = []ResearchSource{{URL: "ftp://nope"}}
	if !errsContain(Validate(p), "must be an http(s) URL") {
		t.Errorf("non-http research URL must be rejected; got %v", Validate(p))
	}
}

func TestValidate_V3_ResearchNotHardEnforced(t *testing.T) {
	p := validPlan()
	p.Meta.SchemaVersion = "v3"
	// v3 keeps back-compat: a missing research basis must NOT be a hard error.
	if errsContain(Validate(p), "research must cite") || errsContain(Validate(p), "design.research must cite") {
		t.Errorf("v3 must not hard-enforce research; got %v", Validate(p))
	}
	// ...but advisories should still surface the gap so it's visible.
	if len(ValidateResearchAdvisories(p)) == 0 {
		t.Error("expected research advisories for a v3 plan with no research basis")
	}
}

func TestValidate_V4_NoResearchAdvisories(t *testing.T) {
	// v4 enforces in Validate, so the advisory channel stays silent for it.
	if adv := ValidateResearchAdvisories(validV4Plan()); len(adv) != 0 {
		t.Errorf("v4 plans should not emit advisories (the hard gate covers them); got %v", adv)
	}
}
