package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/interview"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

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

func lineContaining(s, sub string) string {
	for _, ln := range strings.Split(s, "\n") {
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
