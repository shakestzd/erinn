package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPlanFeedback_OutputStructure(t *testing.T) {
	const planID = "plan-test1234"
	dir := writeTempPlanHTML(t, planID)

	// Insert approval rows.
	if err := storePlanFeedback(dir, planID, "slice-1", "approve", "true", ""); err != nil {
		t.Fatalf("store approve slice-1: %v", err)
	}
	if err := storePlanFeedback(dir, planID, "slice-2", "approve", "false", ""); err != nil {
		t.Fatalf("store approve slice-2: %v", err)
	}
	if err := storePlanFeedback(dir, planID, "slice-2", "comment", "needs rework", ""); err != nil {
		t.Fatalf("store comment slice-2: %v", err)
	}

	// Insert answer rows.
	if err := storePlanFeedback(dir, planID, "questions", "answer", "lazy", "q-caching"); err != nil {
		t.Fatalf("store answer q-caching: %v", err)
	}

	// Insert amendment row.
	amendJSON := `{"slice_num":1,"field":"what","operation":"set","content":"new description"}`
	if err := storePlanFeedback(dir, planID, "amendment", "accepted", amendJSON, ""); err != nil {
		t.Fatalf("store amendment: %v", err)
	}

	// Insert chat messages row.
	msgsJSON := `[{"role":"user","content":"hello","timestamp":"2026-04-12T00:00:00Z"}]`
	if err := storePlanFeedback(dir, planID, "chat", "messages", msgsJSON, ""); err != nil {
		t.Fatalf("store chat messages: %v", err)
	}

	// Redirect stdout to capture output.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	runErr := planFeedback(dir, planID)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)

	if runErr != nil {
		t.Fatalf("planFeedback: %v", runErr)
	}

	var out planFeedbackJSON
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal output: %v\nraw: %s", err, buf.String())
	}

	// plan_id
	if out.PlanID != planID {
		t.Errorf("plan_id: got %q, want %q", out.PlanID, planID)
	}

	// approvals
	if a, ok := out.Approvals["slice-1"]; !ok || !a.Approved {
		t.Errorf("slice-1 approval: got %+v, want approved=true", out.Approvals["slice-1"])
	}
	if a, ok := out.Approvals["slice-2"]; !ok || a.Approved {
		t.Errorf("slice-2 approval: got %+v, want approved=false", out.Approvals["slice-2"])
	}
	if out.Approvals["slice-2"].Comment != "needs rework" {
		t.Errorf("slice-2 comment: got %q, want %q", out.Approvals["slice-2"].Comment, "needs rework")
	}

	// answers
	if v, ok := out.Answers["q-caching"]; !ok || v != "lazy" {
		t.Errorf("q-caching answer: got %q, want %q", out.Answers["q-caching"], "lazy")
	}

	// amendments
	if len(out.Amendments) != 1 {
		t.Fatalf("amendments count: got %d, want 1", len(out.Amendments))
	}
	a := out.Amendments[0]
	if a.Field != "what" || a.Op != "set" || a.Value != "new description" || a.Slice != 1 {
		t.Errorf("amendment: got %+v, want field=what op=set value='new description' slice=1", a)
	}

	// chat_messages
	if len(out.ChatMessages) != 1 {
		t.Fatalf("chat_messages count: got %d, want 1", len(out.ChatMessages))
	}
	msg := out.ChatMessages[0]
	if msg.Role != "user" || msg.Content != "hello" {
		t.Errorf("chat message: got %+v", msg)
	}
	if !strings.HasPrefix(msg.Timestamp, "2026") {
		t.Errorf("chat message timestamp: got %q", msg.Timestamp)
	}
}

func TestPlanFeedback_EmptyPlan(t *testing.T) {
	dir := writeTempPlanHTML(t, "plan-empty0000")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	runErr := planFeedback(dir, "plan-empty0000")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)

	if runErr != nil {
		t.Fatalf("planFeedback: %v", runErr)
	}

	var out planFeedbackJSON
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}

	if out.PlanID != "plan-empty0000" {
		t.Errorf("plan_id: got %q", out.PlanID)
	}
	if len(out.Approvals) != 0 {
		t.Errorf("approvals should be empty, got %v", out.Approvals)
	}
	if len(out.Answers) != 0 {
		t.Errorf("answers should be empty, got %v", out.Answers)
	}
	if len(out.Amendments) != 0 {
		t.Errorf("amendments should be empty, got %v", out.Amendments)
	}
	if len(out.ChatMessages) != 0 {
		t.Errorf("chat_messages should be empty, got %v", out.ChatMessages)
	}
}
