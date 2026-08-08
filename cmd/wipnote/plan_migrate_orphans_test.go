package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
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

// TestExecuteMigrateOrphans_ApplyIsIdempotent is the disk round-trip
// regression test for bug-c65a5f4e. isOrphanFeature's standalone_reason check
// only means anything if that property actually survives a write+re-parse —
// before the fix, Node.Properties was never rendered to HTML, so
// `--apply` re-marked (and re-printed) the same "orphan" on every run.
func TestExecuteMigrateOrphans_ApplyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"features", "tracks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	feat, err := p.Features.Create("Orphan feature")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// First apply: the feature has no plan linkage, so it gets marked
	// standalone.
	first := captureStdout(t, func() {
		if applyErr := executeMigrateOrphans(p, true); applyErr != nil {
			t.Fatalf("first apply: %v", applyErr)
		}
	})
	if !strings.Contains(first, "Marked 1 of 1") {
		t.Fatalf("first apply did not mark the orphan feature:\n%s", first)
	}

	// The marker must survive a disk round-trip — refetch from HTML (not the
	// stale in-memory node) to prove Properties actually persisted.
	refetched, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got := refetched.Properties["standalone_reason"]; got != "pre-enforcement" {
		t.Fatalf("standalone_reason did not round-trip through disk: got %#v", got)
	}

	// Second apply: with the marker now visible on disk, isOrphanFeature must
	// exclude the feature. This is the idempotency the bug report calls out
	// as broken: "wipnote plan migrate-orphans --apply will re-mark the same
	// features on every run."
	second := captureStdout(t, func() {
		if applyErr := executeMigrateOrphans(p, true); applyErr != nil {
			t.Fatalf("second apply: %v", applyErr)
		}
	})
	if !strings.Contains(second, "No orphan features found.") {
		t.Fatalf("second apply was not a no-op:\n%s", second)
	}
}
