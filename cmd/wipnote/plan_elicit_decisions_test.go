package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
)

// seedPlanForElicit creates a temp project dir with a single-slice plan ready
// for elicitation tests.
func seedPlanForElicit(t *testing.T) (dir, planID string, sliceNum int) {
	t.Helper()
	dir = t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	planID = workitem.GenerateID("plan", "elicit test")
	plan := planyaml.NewPlan(planID, "Elicit Test", "")
	plan.Meta.Status = "active"
	plan.Design.Problem = "test"
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		ID:    workitem.GenerateID("slice", "one"),
		Num:   1,
		Title: "Slice One",
		What:  "Do the thing",
		Why:   "Because",
	})
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	return dir, planID, 1
}

// TestSliceSchema_DecisionsNotesField — round-trip preserves the field.
func TestSliceSchema_DecisionsNotesField(t *testing.T) {
	dir, planID, _ := seedPlanForElicit(t)
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	plan.Slices[0].DecisionsNotes = "### Scope\nA, B"
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Slices[0].DecisionsNotes != "### Scope\nA, B" {
		t.Errorf("DecisionsNotes = %q, want round-trip preserved", reloaded.Slices[0].DecisionsNotes)
	}
}

// TestSliceSchema_NoDecisionsNotes — legacy slice without the field validates
// and round-trips with the field empty.
func TestSliceSchema_NoDecisionsNotes(t *testing.T) {
	dir, planID, _ := seedPlanForElicit(t)
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if plan.Slices[0].DecisionsNotes != "" {
		t.Errorf("expected empty DecisionsNotes by default, got %q", plan.Slices[0].DecisionsNotes)
	}
	// Re-save and reload to confirm omitempty behavior.
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save: %v", err)
	}
	body, _ := os.ReadFile(planPath)
	if strings.Contains(string(body), "decisions_notes:") {
		t.Errorf("YAML should not contain decisions_notes when empty:\n%s", body)
	}
}

// TestElicitDecisions_FlagsForm — --scope/--decisions/--context flags write
// the expected combined Markdown blob.
func TestElicitDecisions_FlagsForm(t *testing.T) {
	dir, planID, sliceNum := seedPlanForElicit(t)

	err := elicitDecisionsForSlice(dir, planID, sliceNum, elicitInput{
		scope:     "what's in",
		decisions: "we picked X",
		context:   "see plan-foo",
	})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}

	plan, err := planyaml.Load(filepath.Join(dir, "plans", planID+".yaml"))
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	got := plan.Slices[0].DecisionsNotes
	for _, want := range []string{"### Scope", "what's in", "### Decisions", "we picked X", "### Context", "see plan-foo"} {
		if !strings.Contains(got, want) {
			t.Errorf("DecisionsNotes missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestElicitDecisions_StdinForm — --from-stdin reads YAML and writes the same
// blob structure.
func TestElicitDecisions_StdinForm(t *testing.T) {
	dir, planID, sliceNum := seedPlanForElicit(t)

	stdin := strings.NewReader("scope: what's in\ndecisions: we picked X\ncontext: see plan-foo\n")
	err := elicitDecisionsForSlice(dir, planID, sliceNum, elicitInput{
		fromStdin: true,
		stdin:     stdin,
	})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}

	plan, _ := planyaml.Load(filepath.Join(dir, "plans", planID+".yaml"))
	got := plan.Slices[0].DecisionsNotes
	for _, want := range []string{"### Scope", "what's in", "### Decisions", "we picked X", "### Context", "see plan-foo"} {
		if !strings.Contains(got, want) {
			t.Errorf("DecisionsNotes missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestElicitDecisions_AllEmpty — all three answers empty is rejected so we
// don't blow away decisions_notes by accident.
func TestElicitDecisions_AllEmpty(t *testing.T) {
	dir, planID, sliceNum := seedPlanForElicit(t)

	err := elicitDecisionsForSlice(dir, planID, sliceNum, elicitInput{})
	if err == nil {
		t.Fatal("expected error when all three answers empty, got nil")
	}
}

// TestElicitDecisions_AtomicWrite — concurrent elicitations on different
// slices in the same plan don't corrupt the YAML.
func TestElicitDecisions_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		_ = os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}
	planID := workitem.GenerateID("plan", "atomic")
	plan := planyaml.NewPlan(planID, "Atomic", "")
	plan.Meta.Status = "active"
	plan.Design.Problem = "x"
	for i := 1; i <= 4; i++ {
		plan.Slices = append(plan.Slices, planyaml.PlanSlice{
			ID:    workitem.GenerateID("slice", "s"),
			Num:   i,
			Title: "s",
			What:  "w",
			Why:   "y",
		})
	}
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(4)
	for i := 1; i <= 4; i++ {
		go func(n int) {
			defer wg.Done()
			_ = elicitDecisionsForSlice(dir, planID, n, elicitInput{
				decisions: "slice " + string(rune('A'+n-1)),
			})
		}(i)
	}
	wg.Wait()

	reloaded, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload after concurrent writes: %v (likely corrupted YAML)", err)
	}
	if len(reloaded.Slices) != 4 {
		t.Errorf("slice count = %d, want 4 (lost slice from race)", len(reloaded.Slices))
	}
}

// TestCombineDecisionsMarkdown_OmitsEmpty — empty subsections are dropped so
// the rendered blob never contains an empty heading.
func TestCombineDecisionsMarkdown_OmitsEmpty(t *testing.T) {
	out := combineDecisionsMarkdown("", "we picked X", "")
	if strings.Contains(out, "### Scope") {
		t.Errorf("empty Scope should be omitted: %q", out)
	}
	if strings.Contains(out, "### Context") {
		t.Errorf("empty Context should be omitted: %q", out)
	}
	if !strings.Contains(out, "### Decisions") || !strings.Contains(out, "we picked X") {
		t.Errorf("Decisions should be present: %q", out)
	}
}
