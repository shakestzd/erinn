package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// finalizeResult holds the output of a plan finalize operation.
type finalizeResult struct {
	TrackID          string
	FeatureIDs       []string
	AlreadyFinalized bool
	ExecuteCmd       string // CLI command to start working on the track
}

// executePlanFinalize reads a plan's steps, creates a track and features,
// wires edges, and marks the plan as finalized. Idempotent.
func executePlanFinalize(p *workitem.Project, wipnoteDir, planID string) (*finalizeResult, error) {
	// Get the plan node.
	planNode, err := p.Plans.Get(planID)
	if err != nil {
		return nil, fmt.Errorf("plan %s not found: %w", planID, err)
	}

	// Idempotent: if already finalized, find existing track + features and return.
	if planNode.Status == "finalized" || planNode.Status == "done" {
		trackID := findTrackForPlan(p, planID)
		featureIDs := findFeaturesForTrack(p, trackID)
		return &finalizeResult{
			TrackID:          trackID,
			FeatureIDs:       featureIDs,
			AlreadyFinalized: true,
			ExecuteCmd:       buildExecuteCmd(trackID),
		}, nil
	}

	// Use plan steps as slices — all steps are treated as approved.
	// Prefer YAML slices (source of truth) over HTML steps.
	slices := parsePlanSlicesFromYAML(wipnoteDir, planID)
	if len(slices) == 0 {
		slices = parsePlanStepsAsSlices(planNode)
	}

	// Create track from the plan title.
	trackNode, err := p.Tracks.Create(planNode.Title,
		workitem.TrackWithStatus("in-progress"),
	)
	if err != nil {
		return nil, fmt.Errorf("create track: %w", err)
	}

	// Create features for each slice.
	var featureIDs []string
	for _, s := range slices {
		opts := []workitem.FeatureOption{
			workitem.FeatWithTrack(trackNode.ID),
		}
		if planNode.Content != "" {
			opts = append(opts, workitem.FeatWithContent(planNode.Content))
		}

		featNode, err := p.Features.Create(s.title, opts...)
		if err != nil {
			return nil, fmt.Errorf("create feature for slice %d: %w", s.num, err)
		}

		featureIDs = append(featureIDs, featNode.ID)

		// Wire bidirectional track <-> feature edges.
		if err := wireTrackEdges(p, featNode.ID, trackNode.ID, s.title); err != nil {
			return nil, fmt.Errorf("wire track edges for %s: %w", featNode.ID, err)
		}

		// Link feature back to source plan (planned_in).
		plannedIn := models.Edge{
			TargetID:     planID,
			Relationship: models.RelPlannedIn,
			Title:        planID,
			Since:        time.Now().UTC(),
		}
		_, _ = p.Features.AddEdge(featNode.ID, plannedIn)
	}

	// Link plan to track: plan implemented_in track.
	edge := models.Edge{
		TargetID:     trackNode.ID,
		Relationship: models.RelImplementedIn,
		Title:        trackNode.ID,
		Since:        time.Now().UTC(),
	}
	_, _ = p.Plans.AddEdge(planNode.ID, edge)

	// Mark plan as finalized.
	if _, err := p.Plans.Complete(planID); err != nil {
		// Best-effort: try updating status directly.
		edit := p.Plans.Edit(planID)
		edit = edit.SetStatus("finalized")
		_ = edit.Save()
	}

	return &finalizeResult{
		TrackID:    trackNode.ID,
		FeatureIDs: featureIDs,
		ExecuteCmd: buildExecuteCmd(trackNode.ID),
	}, nil
}

// planSlice holds metadata for a single slice parsed from the plan.
type planSlice struct {
	num     int
	name    string
	title   string
	depNums []int
}

// parsePlanSlicesFromYAML reads slices from the YAML plan file.
// Returns nil if the YAML doesn't exist or has no slices.
func parsePlanSlicesFromYAML(wipnoteDir, planID string) []planSlice {
	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(yamlPath)
	if err != nil || len(plan.Slices) == 0 {
		return nil
	}
	var slices []planSlice
	for _, s := range plan.Slices {
		slices = append(slices, planSlice{
			num:     s.Num,
			name:    s.ID,
			title:   s.Title,
			depNums: s.Deps,
		})
	}
	return slices
}

// wireTrackEdges creates bidirectional part_of/contains edges between a
// feature and its track.
func wireTrackEdges(p *workitem.Project, featureID, trackID, featureTitle string) error {
	now := time.Now().UTC()

	// feature -> track (part_of)
	partOf := models.Edge{
		TargetID:     trackID,
		Relationship: models.RelPartOf,
		Title:        trackID,
		Since:        now,
	}
	if _, err := p.Features.AddEdge(featureID, partOf); err != nil {
		return fmt.Errorf("part_of: %w", err)
	}

	// track -> feature (contains)
	contains := models.Edge{
		TargetID:     featureID,
		Relationship: models.RelContains,
		Title:        featureTitle,
		Since:        now,
	}
	if _, err := p.Tracks.AddEdge(trackID, contains); err != nil {
		return fmt.Errorf("contains: %w", err)
	}

	return nil
}

// buildExecuteCmd returns the CLI command to start working on a finalized track.
func buildExecuteCmd(trackID string) string {
	if trackID == "" {
		return ""
	}
	return "wipnote yolo --track " + trackID
}

// findFeaturesForTrack returns feature IDs linked to a track via contains edges.
func findFeaturesForTrack(p *workitem.Project, trackID string) []string {
	if trackID == "" {
		return nil
	}
	node, err := p.Tracks.Get(trackID)
	if err != nil {
		return nil
	}
	var ids []string
	for _, edge := range node.Edges[string(models.RelContains)] {
		if strings.HasPrefix(edge.TargetID, "feat-") {
			ids = append(ids, edge.TargetID)
		}
	}
	return ids
}

// findTrackForPlan searches for an existing track linked to the plan via
// an implemented_in edge. Returns the track ID or empty string.
func findTrackForPlan(p *workitem.Project, planID string) string {
	node, err := p.Plans.Get(planID)
	if err != nil {
		return ""
	}
	for _, edge := range node.Edges[string(models.RelImplementedIn)] {
		if strings.HasPrefix(edge.TargetID, "trk-") {
			return edge.TargetID
		}
	}
	return ""
}

// ---- New YAML-first finalize (plan finalize v2) --------------------------------

// planFinalizeFromYAMLCmd creates a cobra command for the new plan finalize.
// This replaces the old finalize which created a new track. The new design:
//  1. Validates: plan has a track, problem statement, and ≥1 slice.
//  2. Creates real features for each slice, linked to plan + track.
//  3. Writes the promoted feature_id back to each YAML slice.
//  4. Marks plan status "finalized" and re-renders HTML.
func planFinalizeFromYAMLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "finalize <plan-id>",
		Short: "Promote plan slices to real features and lock the plan (hierarchy flow)",
		Long: `Validate and finalize a YAML plan: promote each slice to a real feature
linked to both the plan and its track. Writes feature IDs back to YAML.
After finalize the plan is locked — use 'plan reopen' to unlock.

This is the hierarchy-only flow: the plan must already have a track, and
every slice in the YAML is promoted unconditionally. It does NOT consult
SQLite plan_feedback for per-slice approvals.

For the dashboard-review workflow that creates a track and only promotes
slices with explicit approve actions, use 'plan finalize-yaml' instead.

Requires:
  - plan has a track (set meta.track_id in YAML)
  - plan has a non-empty problem statement
  - plan has at least one slice

Example:
  wipnote plan finalize plan-a1b2c3d4`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			p, err := workitem.Open(wipnoteDir, agentForClaim())
			if err != nil {
				return fmt.Errorf("open project: %w", err)
			}
			defer p.Close()

			result, err := executePlanFinalizeFromYAML(p, wipnoteDir, args[0])
			if err != nil {
				return err
			}

			fmt.Printf("%d features created, plan locked\n", len(result.FeatureIDs))
			fmt.Printf("Track: %s\n", result.TrackID)
			for _, fid := range result.FeatureIDs {
				fmt.Printf("  %s\n", fid)
			}
			if result.ExecuteCmd != "" {
				fmt.Printf("\nNext: %s\n", result.ExecuteCmd)
			}
			return nil
		},
	}
}

// executePlanFinalizeFromYAML implements the new plan finalize logic.
// It validates the YAML plan, creates features for each slice linked to plan
// and track, writes feature_id back to YAML, and marks the plan finalized.
func executePlanFinalizeFromYAML(p *workitem.Project, wipnoteDir, planID string) (*finalizeResult, error) {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return nil, fmt.Errorf("load plan YAML: %w", err)
	}

	// Guard: already finalized → must use plan reopen first.
	if plan.Meta.Status == "finalized" {
		return nil, fmt.Errorf("plan %s is locked (status: finalized) — use 'plan reopen %s' to unlock", planID, planID)
	}

	// Validate: must have a track.
	if plan.Meta.TrackID == "" {
		return nil, fmt.Errorf("plan must be on a track — set meta.track_id in YAML or use 'plan attach-track %s <trk-id>'", planID)
	}

	// Validate: must have a problem statement.
	if strings.TrimSpace(plan.Design.Problem) == "" {
		return nil, fmt.Errorf("plan must have a non-empty problem statement — set design.problem in YAML")
	}

	// Validate: must have at least one slice.
	if len(plan.Slices) == 0 {
		return nil, fmt.Errorf("plan must have at least one slice — add slices with 'plan add-slice-yaml %s <title>'", planID)
	}

	trackID := plan.Meta.TrackID

	// Create features for each slice (idempotent: reuse existing FeatureIDs).
	numToFeatureID := make(map[int]string, len(plan.Slices))
	var featureIDs []string

	for i, s := range plan.Slices {
		// Idempotency — trust-and-skip: if the YAML already records a FeatureID
		// the feature was created in a previous finalize run. Features are
		// independent work items after creation; any changes to title or content
		// must be made via `wipnote feature edit`, not by re-running plan
		// finalize. This is the one-way-mutation invariant.
		if s.FeatureID != "" {
			numToFeatureID[s.Num] = s.FeatureID
			featureIDs = append(featureIDs, s.FeatureID)
			fmt.Printf("%s  Slice %d — %s (already finalized)\n", s.FeatureID, s.Num, s.Title)
			continue
		}

		// Fall through: create a new feature for this slice.
		content := buildSliceFeatureContent(s)
		feat, err := p.Features.Create(s.Title,
			workitem.FeatWithTrack(trackID),
			workitem.FeatWithContent(content),
		)
		if err != nil {
			return nil, fmt.Errorf("create feature for slice %d (%q): %w", s.Num, s.Title, err)
		}

		fmt.Printf("%s  Slice %d — %s (created)\n", feat.ID, s.Num, s.Title)
		numToFeatureID[s.Num] = feat.ID
		featureIDs = append(featureIDs, feat.ID)

		// Write feature_id back to YAML slice immediately.
		plan.Slices[i].FeatureID = feat.ID

		// Wire part_of (feature→track) and contains (track→feature). A failure
		// here means a stale meta.track_id — fail the finalize rather than
		// locking the plan with inconsistent hierarchy edges.
		if err := wireTrackEdges(p, feat.ID, trackID, feat.Title); err != nil {
			return nil, fmt.Errorf("wire track edges for slice %d (%s → %s): %w", s.Num, feat.ID, trackID, err)
		}

		// Link feature → plan via planned_in edge, recording slice-num for
		// slice-scoped provenance checks on re-finalize.
		if _, err := p.Features.AddEdge(feat.ID, models.Edge{
			TargetID:     planID,
			Relationship: models.RelPlannedIn,
			Title:        planID,
			Since:        time.Now().UTC(),
			Properties:   map[string]string{"slice-num": strconv.Itoa(s.Num)},
		}); err != nil {
			return nil, fmt.Errorf("link feature %s to plan %s: %w", feat.ID, planID, err)
		}
	}

	// Wire blocked_by edges from slice deps.
	for _, s := range plan.Slices {
		for _, depNum := range s.Deps {
			depFeatID, ok := numToFeatureID[depNum]
			if !ok {
				continue
			}
			p.Features.AddEdge(numToFeatureID[s.Num], models.Edge{ //nolint:errcheck
				TargetID:     depFeatID,
				Relationship: "blocked_by",
				Since:        time.Now().UTC(),
			})
		}
	}

	// Link plan → track via implemented_in.
	p.Plans.AddEdge(planID, models.Edge{ //nolint:errcheck
		TargetID:     trackID,
		Relationship: models.RelImplementedIn,
		Title:        trackID,
		Since:        time.Now().UTC(),
	})

	// Lock the plan: set status to finalized in YAML.
	plan.Meta.Status = "finalized"
	if err := planyaml.Save(planPath, plan); err != nil {
		return nil, fmt.Errorf("save plan YAML: %w", err)
	}

	// Re-render the plan HTML so it reflects finalized state.
	_ = renderPlanToFile(wipnoteDir, planID)

	commitMsg := fmt.Sprintf("plan(%s): finalize — %d features created on %s", planID, len(featureIDs), trackID)
	if err := commitPlanChange(planPath, commitMsg); err != nil {
		return nil, fmt.Errorf("autocommit finalize: %w", err)
	}

	return &finalizeResult{
		TrackID:    trackID,
		FeatureIDs: featureIDs,
		ExecuteCmd: buildExecuteCmd(trackID),
	}, nil
}

// buildSliceFeatureContent constructs a feature description from a YAML slice's
// what/why/done-when fields.
func buildSliceFeatureContent(s planyaml.PlanSlice) string {
	if s.What == "" {
		return s.Why
	}
	var sb strings.Builder
	sb.WriteString(s.What)
	if s.Why != "" {
		sb.WriteString("\n\n## Why\n")
		sb.WriteString(s.Why)
	}
	if len(s.DoneWhen) > 0 {
		sb.WriteString("\n\n## Done When\n")
		for _, d := range s.DoneWhen {
			sb.WriteString("- " + d + "\n")
		}
	}
	return sb.String()
}
