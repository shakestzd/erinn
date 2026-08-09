package workitem_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

func TestConcurrentIndependentMutationsDoNotLoseUpdates(t *testing.T) {
	p := newTestProject(t)
	feat, err := p.Features.Create("Concurrent Mutation Test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const count = 12
	targetIDs := make([]string, count)
	for i := 0; i < count; i++ {
		target, err := p.Features.Create(fmt.Sprintf("Target %02d", i))
		if err != nil {
			t.Fatalf("create target %d: %v", i, err)
		}
		targetIDs[i] = target.ID
	}

	var wg sync.WaitGroup
	errs := make(chan error, count*3)
	for i := 0; i < count; i++ {
		i := i
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, err := p.Features.AddEdge(feat.ID, models.Edge{
				TargetID:     targetIDs[i],
				Relationship: models.RelBlocks,
				Title:        fmt.Sprintf("Target %02d", i),
			})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			errs <- p.Features.Edit(feat.ID).AddStep(fmt.Sprintf("Step %02d", i)).Save()
		}()
		go func() {
			defer wg.Done()
			errs <- p.Features.AddNote(feat.ID, fmt.Sprintf("Note %02d", i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent mutation: %v", err)
		}
	}

	got, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(got.Edges[string(models.RelBlocks)]) != count {
		t.Fatalf("blocks edges: got %d, want %d", len(got.Edges[string(models.RelBlocks)]), count)
	}
	for i := 0; i < count; i++ {
		wantStep := fmt.Sprintf("Step %02d", i)
		wantNote := fmt.Sprintf("Note %02d", i)
		if !hasStep(got.Steps, wantStep) {
			t.Errorf("missing step %q; got %#v", wantStep, got.Steps)
		}
		if !strings.Contains(got.Content, wantNote) {
			t.Errorf("missing note %q in content %q", wantNote, got.Content)
		}
	}
}

func TestConcurrentRepeatedStartIsIdempotent(t *testing.T) {
	p := newTestProject(t)
	feat, err := p.Features.Create("Concurrent Start Test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	runConcurrently(t, 16, func() error {
		_, err := p.Features.Start(feat.ID)
		return err
	})

	got, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != models.StatusInProgress {
		t.Fatalf("status = %q, want %q", got.Status, models.StatusInProgress)
	}
	if got.AgentAssigned != "test-agent" {
		t.Fatalf("AgentAssigned = %q, want test-agent", got.AgentAssigned)
	}
}

func TestConcurrentRepeatedCompleteIsIdempotent(t *testing.T) {
	p := newTestProject(t)
	feat, err := p.Features.Create("Concurrent Complete Test",
		workitem.FeatWithSteps("Step 1", "Step 2", "Step 3"),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	runConcurrently(t, 16, func() error {
		_, err := p.Features.Complete(feat.ID)
		return err
	})

	got, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != models.StatusDone {
		t.Fatalf("status = %q, want %q", got.Status, models.StatusDone)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("steps: got %d, want 3", len(got.Steps))
	}
	for i, step := range got.Steps {
		if !step.Completed {
			t.Fatalf("step %d was not completed: %#v", i, step)
		}
	}
}

func TestConcurrentStatusMutationsKeepSQLiteDerivedStatusInOrder(t *testing.T) {
	p := newTestProject(t)
	feat, err := p.Features.Create("Concurrent SQLite Status Test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const count = 40
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = p.Features.Start(feat.ID)
			} else {
				_, err = p.Features.Complete(feat.ID)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent status mutation: %v", err)
		}
	}

	got, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != models.StatusInProgress && got.Status != models.StatusDone {
		t.Fatalf("canonical status = %q, want one of the concurrent terminal states", got.Status)
	}
}

func runConcurrently(t *testing.T, count int, fn func() error) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- fn()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent operation: %v", err)
		}
	}
}

func hasStep(steps []models.Step, description string) bool {
	for _, step := range steps {
		if step.Description == description {
			return true
		}
	}
	return false
}
