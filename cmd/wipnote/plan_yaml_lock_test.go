package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shakestzd/wipnote/plan/planyaml"
)

// TestUpdateSliceYAMLApproval_ConcurrentWritersNoLostUpdate is the regression
// for defect 4 (feat-fc3cc9e0): most plan-YAML mutators called bare
// planyaml.Load() then later planyaml.Save(), each a WHOLE-DOCUMENT
// read-modify-write. Save only mutex-guards its own marshal-and-write
// (plan/planyaml/io.go), not the load — so two concurrent writers can each
// load the same starting document, and whichever one saves last silently
// clobbers every other writer's change. Before this migration this was a
// row-level SQLite UPDATE and structurally immune to the race; the YAML
// whole-document model reintroduced it. storePlanFeedbackEntry already held
// filelock.Guard + planyaml.LockPlanForWrite across the whole window; this
// test proves updateSliceYAMLApproval — one of the mutators that didn't —
// now gets the same protection.
//
// N goroutines concurrently approve N DIFFERENT slices of the SAME plan
// document. Without serialization, only the last writer's save would survive
// and every other slice's approval would be lost. With the fix, all N
// approvals must survive.
func TestUpdateSliceYAMLApproval_ConcurrentWritersNoLostUpdate(t *testing.T) {
	const numSlices = 20

	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	plansDir := filepath.Join(wipnoteDir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}

	planID := "plan-lockrace01"
	plan := planyaml.NewPlan(planID, "Lock Race Plan", "concurrency regression fixture")
	for i := 1; i <= numSlices; i++ {
		plan.Slices = append(plan.Slices, planyaml.PlanSlice{
			ID:    fmt.Sprintf("feat-slice%02d", i),
			Num:   i,
			Title: fmt.Sprintf("Slice %d", i),
			What:  "placeholder",
		})
	}
	planPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("seed plan: %v", err)
	}

	// Barrier: line every goroutine's Load up as close together as possible
	// so the race is exercised, not merely possible.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, numSlices)
	for i := 1; i <= numSlices; i++ {
		wg.Add(1)
		go func(sliceNum int) {
			defer wg.Done()
			<-start
			errs[sliceNum-1] = updateSliceYAMLApproval(wipnoteDir, planID, sliceNum, "approved")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("updateSliceYAMLApproval(slice %d): %v", i+1, err)
		}
	}

	final, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	var lost []int
	for _, s := range final.Slices {
		if s.ApprovalStatus != "approved" {
			lost = append(lost, s.Num)
		}
	}
	if len(lost) != 0 {
		t.Errorf("lost %d of %d concurrent approvals to interleaved whole-document saves: slices %v",
			len(lost), numSlices, lost)
	}
}
