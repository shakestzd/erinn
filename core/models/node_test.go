package models_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

func TestNodeJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	original := &models.Node{
		ID:            "feat-abc123",
		Title:         "Test Feature",
		Type:          "feature",
		Status:        models.StatusInProgress,
		Priority:      models.PriorityHigh,
		CreatedAt:     now,
		UpdatedAt:     now,
		AgentAssigned: "claude-code",
		TrackID:       "trk-xyz",
		Steps: []models.Step{
			{StepID: "step-1", Description: "First step", Completed: true},
			{StepID: "step-2", Description: "Second step", Completed: false, DependsOn: []string{"step-1"}},
		},
		Edges: map[string][]models.Edge{
			"implemented_in": {
				{TargetID: "sess-001", Relationship: models.RelImplementedIn, Title: "sess-001", Since: now},
			},
		},
		Content: "Some content here",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded models.Node
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Title != original.Title {
		t.Errorf("Title: got %q, want %q", decoded.Title, original.Title)
	}
	if decoded.Status != original.Status {
		t.Errorf("Status: got %q, want %q", decoded.Status, original.Status)
	}
	if decoded.Priority != original.Priority {
		t.Errorf("Priority: got %q, want %q", decoded.Priority, original.Priority)
	}
	if decoded.TrackID != original.TrackID {
		t.Errorf("TrackID: got %q, want %q", decoded.TrackID, original.TrackID)
	}
	if len(decoded.Steps) != 2 {
		t.Fatalf("Steps count: got %d, want 2", len(decoded.Steps))
	}
	if !decoded.Steps[0].Completed {
		t.Error("Step 0 should be completed")
	}
	if decoded.Steps[1].Completed {
		t.Error("Step 1 should not be completed")
	}
	if len(decoded.Steps[1].DependsOn) != 1 || decoded.Steps[1].DependsOn[0] != "step-1" {
		t.Errorf("Step 1 DependsOn: got %v", decoded.Steps[1].DependsOn)
	}
	if edges, ok := decoded.Edges["implemented_in"]; !ok || len(edges) != 1 {
		t.Error("Expected 1 implemented_in edge")
	}
	if decoded.Content != original.Content {
		t.Errorf("Content: got %q, want %q", decoded.Content, original.Content)
	}
}

func TestNodeValidation(t *testing.T) {
	tests := []struct {
		name    string
		node    models.Node
		wantErr bool
	}{
		{"valid", models.Node{ID: "f-1", Title: "ok"}, false},
		{"empty ID", models.Node{ID: "", Title: "ok"}, true},
		{"empty title", models.Node{ID: "f-1", Title: ""}, true},
		{"spike subtype on non-spike", models.Node{ID: "f-1", Title: "ok", Type: "feature", SpikeSubtype: "technical"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.node.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompletionPercentage(t *testing.T) {
	n := &models.Node{ID: "f-1", Title: "t"}
	if got := n.CompletionPercentage(); got != 0 {
		t.Errorf("no steps, todo: got %d, want 0", got)
	}

	n.Status = models.StatusDone
	if got := n.CompletionPercentage(); got != 100 {
		t.Errorf("no steps, done: got %d, want 100", got)
	}

	n.Status = models.StatusInProgress
	n.Steps = []models.Step{
		{Description: "a", Completed: true},
		{Description: "b", Completed: false},
		{Description: "c", Completed: true},
		{Description: "d", Completed: false},
	}
	if got := n.CompletionPercentage(); got != 50 {
		t.Errorf("2/4 steps: got %d, want 50", got)
	}
}

func TestNextStep(t *testing.T) {
	n := &models.Node{
		ID:    "f-1",
		Title: "t",
		Steps: []models.Step{
			{StepID: "s1", Description: "first", Completed: true},
			{StepID: "s2", Description: "second", Completed: false, DependsOn: []string{"s1"}},
			{StepID: "s3", Description: "third", Completed: false, DependsOn: []string{"s2"}},
		},
	}

	next := n.NextStep()
	if next == nil || next.StepID != "s2" {
		t.Errorf("NextStep: got %v, want s2", next)
	}
}

func TestAddEdge(t *testing.T) {
	n := &models.Node{ID: "f-1", Title: "t"}
	n.AddEdge(models.Edge{
		TargetID:     "sess-1",
		Relationship: models.RelImplementedIn,
		Title:        "sess-1",
	})

	edges := n.Edges[string(models.RelImplementedIn)]
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].TargetID != "sess-1" {
		t.Errorf("edge target: got %q, want %q", edges[0].TargetID, "sess-1")
	}
}

func TestRemoveEdge(t *testing.T) {
	n := &models.Node{ID: "f-1", Title: "t"}
	n.AddEdge(models.Edge{TargetID: "feat-2", Relationship: models.RelBlocks})
	n.AddEdge(models.Edge{TargetID: "feat-3", Relationship: models.RelBlocks})
	n.AddEdge(models.Edge{TargetID: "feat-4", Relationship: models.RelRelatesTo})

	// Remove one edge from a group of two.
	removed := n.RemoveEdge("feat-2", models.RelBlocks)
	if !removed {
		t.Fatal("RemoveEdge should return true for existing edge")
	}
	if edges := n.Edges[string(models.RelBlocks)]; len(edges) != 1 {
		t.Fatalf("expected 1 blocks edge after removal, got %d", len(edges))
	}
	if n.Edges[string(models.RelBlocks)][0].TargetID != "feat-3" {
		t.Error("wrong edge remained after removal")
	}

	// relates_to should be untouched.
	if edges := n.Edges[string(models.RelRelatesTo)]; len(edges) != 1 {
		t.Fatalf("relates_to edges should be untouched, got %d", len(edges))
	}

	// Remove last edge in a relationship group — key should be cleaned up.
	removed = n.RemoveEdge("feat-3", models.RelBlocks)
	if !removed {
		t.Fatal("RemoveEdge should return true for existing edge")
	}
	if _, exists := n.Edges[string(models.RelBlocks)]; exists {
		t.Error("empty edge group should be removed from map")
	}

	// Remove non-existent edge.
	removed = n.RemoveEdge("feat-999", models.RelBlocks)
	if removed {
		t.Error("RemoveEdge should return false for non-existent edge")
	}

	// Remove from nil edges map.
	n2 := &models.Node{ID: "f-2", Title: "t2"}
	removed = n2.RemoveEdge("feat-1", models.RelBlocks)
	if removed {
		t.Error("RemoveEdge on nil edges should return false")
	}
}
