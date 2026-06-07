package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// planWireCmd wires existing features to a plan and track via graph edges,
// then marks the plan as finalized. This is structural-only — it does not
// create new features; the agent must have already created them.
func planWireCmd() *cobra.Command {
	var trackID string

	cmd := &cobra.Command{
		Use:   "wire <plan-id>",
		Short: "Wire existing features to plan and track, then finalize",
		Long: `Connect existing features to the plan and track via graph edges,
wire blocked_by dependency edges from slice deps, and mark the plan
as finalized. Features are matched to plan slices by title (case-insensitive).

The agent must have already created the features before running this command.

Example:
  wipnote plan wire plan-abc123 --track trk-def456`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			return wirePlan(wipnoteDir, args[0], trackID)
		},
	}

	cmd.Flags().StringVar(&trackID, "track", "", "Track ID to wire the plan to (required)")
	_ = cmd.MarkFlagRequired("track")

	return cmd
}

// wirePlan is the testable implementation of planWireCmd.
func wirePlan(wipnoteDir, planID, trackID string) error {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	p, err := workitem.Open(wipnoteDir, agentForClaim())
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	// Validate that the track exists.
	if _, err := p.Tracks.Get(trackID); err != nil {
		return fmt.Errorf("track %s not found: %w", trackID, err)
	}

	// Collect approved slice titles for matching.
	approvedTitles := map[string]planyaml.PlanSlice{}
	for _, s := range plan.Slices {
		if s.Approved {
			approvedTitles[strings.ToLower(s.Title)] = s
		}
	}
	// If no slices are marked approved, treat all as approved (agent-created path).
	if len(approvedTitles) == 0 {
		for _, s := range plan.Slices {
			approvedTitles[strings.ToLower(s.Title)] = s
		}
	}

	// Find all features belonging to the track (by TrackID field or contains edges).
	// Use WithTrackID filter first (works when FeatWithTrack was used at creation).
	trackFeats, err := p.Features.List(workitem.WithTrackID(trackID))
	if err != nil {
		return fmt.Errorf("list features for track: %w", err)
	}

	// Also include features linked via contains edges on the track (alternative wiring).
	containsIDs := findFeaturesForTrack(p, trackID)
	seenIDs := map[string]bool{}
	for _, f := range trackFeats {
		seenIDs[f.ID] = true
	}
	for _, id := range containsIDs {
		if !seenIDs[id] {
			if fn, ferr := p.Features.Get(id); ferr == nil {
				trackFeats = append(trackFeats, fn)
				seenIDs[id] = true
			}
		}
	}

	// Match each track feature to a plan slice by title.
	type wiredFeat struct {
		id    string
		title string
		slice planyaml.PlanSlice
	}
	titleToFeat := map[string]wiredFeat{}
	var unmatched []string

	for _, featNode := range trackFeats {
		key := strings.ToLower(featNode.Title)
		if slice, ok := approvedTitles[key]; ok {
			titleToFeat[key] = wiredFeat{id: featNode.ID, title: featNode.Title, slice: slice}
		} else {
			unmatched = append(unmatched, featNode.ID)
		}
	}

	// Wire edges for each matched feature.
	now := time.Now().UTC()
	linkedCount := 0

	for _, wf := range titleToFeat {
		// planned_in: feature → plan
		p.Features.AddEdge(wf.id, models.Edge{ //nolint:errcheck
			TargetID:     planID,
			Relationship: models.RelPlannedIn,
			Title:        planID,
			Since:        now,
		})

		// part_of + contains (idempotent helper)
		wireTrackEdges(p, wf.id, trackID, wf.title) //nolint:errcheck

		linkedCount++
	}

	// Wire blocked_by edges based on slice deps (match by slice num → feature).
	numToFeat := map[int]wiredFeat{}
	for _, wf := range titleToFeat {
		numToFeat[wf.slice.Num] = wf
	}

	depsWired := 0
	for _, wf := range titleToFeat {
		for _, depNum := range wf.slice.Deps {
			depFeat, ok := numToFeat[depNum]
			if !ok {
				continue
			}
			p.Features.AddEdge(wf.id, models.Edge{ //nolint:errcheck
				TargetID:     depFeat.id,
				Relationship: models.RelBlockedBy,
				Since:        now,
			})
			depsWired++
		}
	}

	// implemented_in: plan → track
	p.Plans.AddEdge(planID, models.Edge{ //nolint:errcheck
		TargetID:     trackID,
		Relationship: models.RelImplementedIn,
		Title:        trackID,
		Since:        now,
	})

	// Update YAML status to finalized.
	plan.Meta.Status = "finalized"
	plan.Meta.TrackID = trackID
	if err := planyaml.Save(planPath, plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	if err := commitPlanChange(planPath, fmt.Sprintf("plan(%s): wire — %d features linked", planID, linkedCount)); err != nil {
		return fmt.Errorf("autocommit wire: %w", err)
	}

	// Print summary.
	fmt.Printf("Wired plan %s to track %s\n", planID, trackID)
	fmt.Printf("Features linked: %d\n", linkedCount)
	fmt.Printf("Dependencies wired: %d\n", depsWired)
	fmt.Printf("Status: finalized (v%d)\n", plan.Meta.Version)

	if len(unmatched) > 0 {
		fmt.Printf("\nNote: %d feature(s) under track not matched to any slice:\n", len(unmatched))
		for _, id := range unmatched {
			fmt.Printf("  %s\n", id)
		}
	}

	if linkedCount < len(approvedTitles) {
		fmt.Printf("\nNote: %d approved slice(s) had no matching feature under the track:\n",
			len(approvedTitles)-linkedCount)
		for title := range approvedTitles {
			if _, found := titleToFeat[title]; !found {
				fmt.Printf("  %s\n", approvedTitles[title].Title)
			}
		}
	}

	return nil
}
