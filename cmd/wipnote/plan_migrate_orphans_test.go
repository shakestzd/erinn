package main

import (
	"testing"

	"github.com/shakestzd/wipnote/core/models"
)

func TestIsOrphanFeature(t *testing.T) {
	tests := []struct {
		name string
		feat *models.Node
		want bool
	}{
		{
			name: "feature with planned_in plan edge is not orphan",
			feat: &models.Node{
				ID: "feat-001",
				Edges: map[string][]models.Edge{
					string(models.RelPlannedIn): {{TargetID: "plan-abc12345"}},
				},
			},
			want: false,
		},
		{
			name: "feature with part_of plan edge is not orphan",
			feat: &models.Node{
				ID: "feat-002",
				Edges: map[string][]models.Edge{
					string(models.RelPartOf): {{TargetID: "plan-abc12345"}},
				},
			},
			want: false,
		},
		{
			name: "feature with only part_of track edge is orphan",
			feat: &models.Node{
				ID: "feat-003",
				Edges: map[string][]models.Edge{
					string(models.RelPartOf): {{TargetID: "trk-deadbeef"}},
				},
			},
			want: true,
		},
		{
			name: "feature already marked standalone is not orphan",
			feat: &models.Node{
				ID: "feat-004",
				Properties: map[string]any{
					"standalone_reason": "pre-enforcement",
				},
				Edges: map[string][]models.Edge{
					string(models.RelPartOf): {{TargetID: "trk-deadbeef"}},
				},
			},
			want: false,
		},
		{
			name: "feature with empty standalone_reason and no plan edge is still orphan",
			feat: &models.Node{
				ID: "feat-005",
				Properties: map[string]any{
					"standalone_reason": "",
				},
			},
			want: true,
		},
		{
			name: "feature with no edges and no properties is orphan",
			feat: &models.Node{ID: "feat-006"},
			want: true,
		},
		{
			name: "feature with mixed edges (plan + track) is not orphan",
			feat: &models.Node{
				ID: "feat-007",
				Edges: map[string][]models.Edge{
					string(models.RelPartOf):    {{TargetID: "trk-deadbeef"}},
					string(models.RelPlannedIn): {{TargetID: "plan-12345678"}},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOrphanFeature(tt.feat); got != tt.want {
				t.Errorf("isOrphanFeature() = %v, want %v", got, tt.want)
			}
		})
	}
}
