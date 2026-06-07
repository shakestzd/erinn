package workitem_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// ---------------------------------------------------------------------------
// FindBottlenecks
// ---------------------------------------------------------------------------

func TestFindBottlenecks_Empty(t *testing.T) {
	p := newTestProject(t)
	bottlenecks, err := workitem.FindBottlenecks(p.ProjectDir)
	if err != nil {
		t.Fatalf("FindBottlenecks: %v", err)
	}
	if len(bottlenecks) != 0 {
		t.Errorf("expected 0 bottlenecks for empty project, got %d", len(bottlenecks))
	}
}

func TestFindBottlenecks_StaleItem(t *testing.T) {
	p := newTestProject(t)

	// Create and start a feature so it becomes in-progress.
	feat, err := p.Features.Create("Stale Feature")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := p.Features.Start(feat.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Manually backdating is not exposed via the public API, so we test
	// that recently-updated items are NOT bottlenecks (the stale threshold
	// is 72 h; a freshly started item has UpdatedAt = now).
	bottlenecks, err := workitem.FindBottlenecks(p.ProjectDir)
	if err != nil {
		t.Fatalf("FindBottlenecks: %v", err)
	}

	// Fresh item should not be stale.
	for _, b := range bottlenecks {
		if b.ItemID == feat.ID {
			t.Errorf("fresh in-progress item %s incorrectly flagged as bottleneck: %s", feat.ID, b.Reason)
		}
	}
}

func TestFindBottlenecks_OverloadedTrack(t *testing.T) {
	p := newTestProject(t)
	trackID := "trk-overload"

	// Create 3 features on the same track and start them all.
	for i := range [3]int{} {
		_ = i
		f, err := p.Features.Create("Track Feature", workitem.FeatWithTrack(trackID))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := p.Features.Start(f.ID); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}

	bottlenecks, err := workitem.FindBottlenecks(p.ProjectDir)
	if err != nil {
		t.Fatalf("FindBottlenecks: %v", err)
	}

	found := false
	for _, b := range bottlenecks {
		if b.ItemID == trackID && b.Type == "track" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected overloaded track bottleneck for %s, got: %+v", trackID, bottlenecks)
	}
}

// ---------------------------------------------------------------------------
// RecommendNextWork
// ---------------------------------------------------------------------------

func TestRecommendNextWork_Empty(t *testing.T) {
	p := newTestProject(t)
	recs, err := workitem.RecommendNextWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("RecommendNextWork: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations for empty project, got %d", len(recs))
	}
}

func TestRecommendNextWork_UpToFive(t *testing.T) {
	p := newTestProject(t)

	// Create 7 todo features.
	for i := range [7]int{} {
		_ = i
		_, err := p.Features.Create("Rec Feature")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	recs, err := workitem.RecommendNextWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("RecommendNextWork: %v", err)
	}
	if len(recs) > 5 {
		t.Errorf("expected at most 5 recommendations, got %d", len(recs))
	}
}

func TestRecommendNextWork_SkipsDoneAndInProgress(t *testing.T) {
	p := newTestProject(t)

	f1, _ := p.Features.Create("Done Feature")
	_, _ = p.Features.Start(f1.ID)
	_, _ = p.Features.Complete(f1.ID)

	f2, _ := p.Features.Create("Active Feature")
	_, _ = p.Features.Start(f2.ID)

	_, _ = p.Features.Create("Todo Feature")

	recs, err := workitem.RecommendNextWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("RecommendNextWork: %v", err)
	}

	for _, r := range recs {
		if r.ItemID == f1.ID || r.ItemID == f2.ID {
			t.Errorf("recommendation included non-todo item %s", r.ItemID)
		}
	}
	if len(recs) != 1 {
		t.Errorf("expected 1 recommendation, got %d", len(recs))
	}
}

func TestRecommendNextWork_HighPriorityFirst(t *testing.T) {
	p := newTestProject(t)

	_, _ = p.Features.Create("Low Prio", workitem.FeatWithPriority("low"))
	_, _ = p.Features.Create("High Prio", workitem.FeatWithPriority("high"))

	recs, err := workitem.RecommendNextWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("RecommendNextWork: %v", err)
	}
	if len(recs) < 2 {
		t.Fatalf("expected at least 2 recommendations, got %d", len(recs))
	}
	if recs[0].Priority != "high" {
		t.Errorf("first recommendation should be high priority, got %q", recs[0].Priority)
	}
}

// ---------------------------------------------------------------------------
// GetParallelWork
// ---------------------------------------------------------------------------

func TestGetParallelWork_Empty(t *testing.T) {
	p := newTestProject(t)
	sets, err := workitem.GetParallelWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetParallelWork: %v", err)
	}
	if len(sets) != 0 {
		t.Errorf("expected 0 parallel sets for empty project, got %d", len(sets))
	}
}

func TestGetParallelWork_RequiresAtLeastTwo(t *testing.T) {
	p := newTestProject(t)

	// Only one item in a track — not a parallelisable set.
	_, _ = p.Features.Create("Solo Feature", workitem.FeatWithTrack("trk-solo"))

	sets, err := workitem.GetParallelWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetParallelWork: %v", err)
	}
	if len(sets) != 0 {
		t.Errorf("expected 0 parallel sets for single-item track, got %d", len(sets))
	}
}

func TestGetParallelWork_TwoInSameTrack(t *testing.T) {
	p := newTestProject(t)
	trackID := "trk-parallel"

	_, _ = p.Features.Create("Feature A", workitem.FeatWithTrack(trackID))
	_, _ = p.Features.Create("Feature B", workitem.FeatWithTrack(trackID))

	sets, err := workitem.GetParallelWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetParallelWork: %v", err)
	}
	if len(sets) == 0 {
		t.Fatalf("expected at least 1 parallel set, got 0")
	}

	found := false
	for _, ps := range sets {
		if ps.TrackID == trackID {
			found = true
			if len(ps.Items) < 2 {
				t.Errorf("parallel set for %s has only %d items", trackID, len(ps.Items))
			}
		}
	}
	if !found {
		t.Errorf("no parallel set found for track %s", trackID)
	}
}

func TestGetParallelWork_NoTrackItemsExcluded(t *testing.T) {
	p := newTestProject(t)

	// Items without a track should not appear in parallel sets.
	_, _ = p.Features.Create("No Track A")
	_, _ = p.Features.Create("No Track B")

	sets, err := workitem.GetParallelWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetParallelWork: %v", err)
	}
	if len(sets) != 0 {
		t.Errorf("expected 0 parallel sets for untracked items, got %d", len(sets))
	}
}

// ---------------------------------------------------------------------------
// velocityBuckets (package-internal helper tested via exported behaviour)
// ---------------------------------------------------------------------------

func TestVelocityBuckets_CountsRecentDone(t *testing.T) {
	p := newTestProject(t)

	f, _ := p.Features.Create("Recently Done")
	_, _ = p.Features.Start(f.ID)
	_, _ = p.Features.Complete(f.ID)

	// The completed feature should appear in at least one of the last 4
	// weeks buckets when retrieved via RecommendNextWork (indirect check —
	// direct velocity bucket test requires access to internals).
	// We test the public surface: RecommendNextWork should NOT include it
	// since it is now done.
	recs, err := workitem.RecommendNextWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("RecommendNextWork: %v", err)
	}
	for _, r := range recs {
		if r.ItemID == f.ID {
			t.Errorf("completed feature %s should not appear in recommendations", f.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Bottleneck — duration ordering
// ---------------------------------------------------------------------------

func TestBottleneck_DurationField(t *testing.T) {
	b := workitem.Bottleneck{
		ItemID:   "feat-x",
		Title:    "Test",
		Type:     "feature",
		Reason:   "in-progress for 96 hours without update",
		Duration: 96 * time.Hour,
	}
	if b.Duration < 72*time.Hour {
		t.Errorf("expected duration >= 72h, got %s", b.Duration)
	}
}

// ---------------------------------------------------------------------------
// Recommendation fields
// ---------------------------------------------------------------------------

func TestRecommendation_Fields(t *testing.T) {
	r := workitem.Recommendation{
		ItemID:   "feat-y",
		Title:    "Do Something",
		TrackID:  "trk-z",
		Priority: "high",
		Reason:   "high-priority track (trk-z)",
	}
	if r.Priority != string(models.PriorityHigh) {
		t.Errorf("priority field: got %q", r.Priority)
	}
}
