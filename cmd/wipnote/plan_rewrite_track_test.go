package main

import (
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/plan/planyaml"
)

func planWithTrack(id, trackID string) *planyaml.PlanYAML {
	return &planyaml.PlanYAML{
		Meta: planyaml.PlanMeta{
			ID:        id,
			TrackID:   trackID,
			Title:     "Track linkage test",
			Status:    "draft",
			CreatedAt: "2026-01-01T00:00:00Z",
			Version:   1,
		},
		Design: planyaml.PlanDesign{Problem: "p", Goals: []string{"g"}},
	}
}

// TestPreserveTrackLinkage_CarriesForwardWhenRewriteOmits is the bug-fddf5820
// finding-15 regression: a rewrite that omits meta.track_id must inherit the
// existing plan's track linkage instead of severing it.
func TestPreserveTrackLinkage_CarriesForwardWhenRewriteOmits(t *testing.T) {
	existing := planWithTrack("plan-edeb2163", "trk-23232c8d")
	rewrite := planWithTrack("plan-edeb2163", "") // rewrite dropped the track

	preserved := preserveTrackLinkage(existing, rewrite)
	if preserved != "trk-23232c8d" {
		t.Errorf("preserveTrackLinkage returned %q, want trk-23232c8d", preserved)
	}
	if rewrite.Meta.TrackID != "trk-23232c8d" {
		t.Errorf("rewrite.Meta.TrackID = %q, want trk-23232c8d (linkage must survive)", rewrite.Meta.TrackID)
	}
}

// TestPreserveTrackLinkage_RespectsExplicitRetarget verifies an explicit new
// track_id in the rewrite is NOT overwritten (intentional re-targeting).
func TestPreserveTrackLinkage_RespectsExplicitRetarget(t *testing.T) {
	existing := planWithTrack("plan-x", "trk-old")
	rewrite := planWithTrack("plan-x", "trk-new")

	preserved := preserveTrackLinkage(existing, rewrite)
	if preserved != "" {
		t.Errorf("preserveTrackLinkage returned %q, want \"\" (no preservation when rewrite is explicit)", preserved)
	}
	if rewrite.Meta.TrackID != "trk-new" {
		t.Errorf("rewrite.Meta.TrackID = %q, want trk-new (explicit retarget must win)", rewrite.Meta.TrackID)
	}
}

// TestPreserveTrackLinkage_NoExistingTrackNoOp verifies that when neither side
// has a track, nothing is invented.
func TestPreserveTrackLinkage_NoExistingTrackNoOp(t *testing.T) {
	existing := planWithTrack("plan-x", "")
	rewrite := planWithTrack("plan-x", "")
	if preserved := preserveTrackLinkage(existing, rewrite); preserved != "" {
		t.Errorf("preserveTrackLinkage returned %q, want \"\"", preserved)
	}
	if rewrite.Meta.TrackID != "" {
		t.Errorf("rewrite.Meta.TrackID = %q, want empty", rewrite.Meta.TrackID)
	}
}

// TestPreserveTrackLinkage_SurvivesYAMLRoundTrip exercises the on-disk path:
// save a plan WITH a track, then save a rewrite WITHOUT one after applying
// preserveTrackLinkage, reload, and assert the track persisted. This guards the
// finalize-attaches-to-existing-track contract end to end (finding 15).
func TestPreserveTrackLinkage_SurvivesYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan-edeb2163.yaml")

	if err := planyaml.Save(planPath, planWithTrack("plan-edeb2163", "trk-23232c8d")); err != nil {
		t.Fatalf("save existing: %v", err)
	}

	existing, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("load existing: %v", err)
	}

	rewrite := planWithTrack("plan-edeb2163", "") // dropped track
	preserveTrackLinkage(existing, rewrite)

	if err := planyaml.Save(planPath, rewrite); err != nil {
		t.Fatalf("save rewrite: %v", err)
	}

	reloaded, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Meta.TrackID != "trk-23232c8d" {
		t.Errorf("after rewrite round-trip, track_id = %q, want trk-23232c8d", reloaded.Meta.TrackID)
	}
}
