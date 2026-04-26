package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
)

func TestPlanFeedback_OutputStructure(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".db"), 0o755); err != nil {
		t.Fatalf("mkdir .db: %v", err)
	}
	dbPath := filepath.Join(dir, ".db", "htmlgraph.db")
	t.Setenv("HTMLGRAPH_DB_PATH", dbPath)
	db, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	const planID = "plan-test1234"

	// Insert approval rows.
	err = dbpkg.StorePlanFeedback(db, planID, "slice-1", "approve", "true", "")
	if err != nil {
		t.Fatalf("store approve slice-1: %v", err)
	}
	err = dbpkg.StorePlanFeedback(db, planID, "slice-2", "approve", "false", "")
	if err != nil {
		t.Fatalf("store approve slice-2: %v", err)
	}
	err = dbpkg.StorePlanFeedback(db, planID, "slice-2", "comment", "needs rework", "")
	if err != nil {
		t.Fatalf("store comment slice-2: %v", err)
	}

	// Insert answer rows.
	err = dbpkg.StorePlanFeedback(db, planID, "questions", "answer", "lazy", "q-caching")
	if err != nil {
		t.Fatalf("store answer q-caching: %v", err)
	}

	// Insert amendment row.
	amendJSON := `{"slice_num":1,"field":"what","operation":"set","content":"new description"}`
	err = dbpkg.StorePlanFeedback(db, planID, "amendment", "accepted", amendJSON, "")
	if err != nil {
		t.Fatalf("store amendment: %v", err)
	}

	// Insert chat messages row.
	msgsJSON := `[{"role":"user","content":"hello","timestamp":"2026-04-12T00:00:00Z"}]`
	err = dbpkg.StorePlanFeedback(db, planID, "chat", "messages", msgsJSON, "")
	if err != nil {
		t.Fatalf("store chat messages: %v", err)
	}
	db.Close()

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
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plans"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create empty DB.
	db, err := dbpkg.Open(filepath.Join(dir, "htmlgraph.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.Close()

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
