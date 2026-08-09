package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/filelock"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

// planPromoteSliceCmd adds the cobra sub-command `plan promote-slice`.
func planPromoteSliceCmd() *cobra.Command {
	var waiveDeps, allowSpecSkip bool
	cmd := &cobra.Command{
		Use:   "promote-slice <plan-id> <slice-num>",
		Short: "Promote an approved plan slice to a feature work item",
		Long: `Promote a single approved slice from a YAML plan into a feature work item
without finalizing the full plan. The plan remains active/draft.

Rules:
  1. The slice must be approved (approve=true in plan_feedback).
  2. All dependency slices must have execution_status=done or superseded,
     unless --waive-deps is passed.
  3. If the slice already has a feature_id it is reused (idempotent).
  4. Edges emitted: feature->track (part_of), feature->plan (planned_in),
     feature->dep_feature (blocked_by) for each promoted dep.
  5. execution_status is set to 'promoted' in plan_feedback and YAML.

Example:
  wipnote plan promote-slice plan-abc12345 2
  wipnote plan promote-slice plan-abc12345 2 --waive-deps`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			sliceNum, err := parseSliceNum(args[1])
			if err != nil {
				return err
			}
			featID, err := promoteSliceFromYAML(wipnoteDir, args[0], sliceNum, waiveDeps, allowSpecSkip)
			if err != nil {
				return err
			}
			fmt.Println(featID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&waiveDeps, "waive-deps", false, "skip dependency readiness check")
	cmd.Flags().BoolVar(&allowSpecSkip, "allow-spec-skip", false, "bypass spec_enforcement.promote_slice gate; logs an audit comment on the slice")
	return cmd
}

// promoteSliceFromYAML is the testable implementation of plan promote-slice.
// It promotes exactly one approved slice, creating (or reusing) a feature.
//
// When config.spec_enforcement.promote_slice is true and allowSpecSkip is
// false, the call refuses if the slice's DecisionsNotes is empty — pointing
// the user at `wipnote plan elicit-decisions` (or the Claude skill) to
// capture decisions before promotion. When allowSpecSkip is true and the
// gate would have fired, an audit line is appended to slice.Comment so the
// override is visible in the plan history.
func promoteSliceFromYAML(wipnoteDir, planID string, sliceNum int, waiveDeps, allowSpecSkip bool) (string, error) {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")

	// Hold the lock across the whole load→mutate→save window (defect 4,
	// feat-fc3cc9e0): promote-slice reads the plan, does feature creation and
	// edge wiring in between, then saves the whole document back. A bare
	// Load-then-Save let two concurrent writers interleave and the second
	// save silently clobber the first. Mirrors storePlanFeedbackEntry.
	releaseFile := filelock.Guard(planPath)
	defer releaseFile()
	releasePlan := planyaml.LockPlanForWrite(planPath)
	defer releasePlan()

	plan, err := planyaml.Load(planPath)
	if err != nil {
		return "", fmt.Errorf("load plan: %w", err)
	}

	// Find the slice.
	sliceIdx, slice, err := findPlanSlice(plan, sliceNum)
	if err != nil {
		return "", err
	}

	// Open DB.
	db, err := openPlanDB(wipnoteDir)
	if err != nil {
		return "", err
	}
	defer db.Close()

	// Validate approval.
	approvals, err := dbpkg.GetSliceApprovals(db, planID)
	if err != nil {
		return "", fmt.Errorf("read approvals: %w", err)
	}
	sectionKey := fmt.Sprintf("slice-%d", sliceNum)
	// Accept approval from either plan_feedback (CLI-driven) or YAML
	// approval_status (pre-set in the source). Either source is sufficient;
	// runApproveSlice keeps both in sync, but a YAML-only seed should also work.
	if approvals[sectionKey] != "approved" && slice.ApprovalStatus != "approved" {
		return "", fmt.Errorf("slice %d is not approved (plan_feedback=%q, yaml=%q); run 'wipnote plan approve-slice %s %d' first",
			sliceNum, approvals[sectionKey], slice.ApprovalStatus, planID, sliceNum)
	}

	// CRISPI spec-enforcement gate: refuse if config opts in and slice has no
	// decisions captured. Audited override via --allow-spec-skip writes a
	// comment to the slice so deliberate skips are visible in plan history.
	enforcement := hooks.ReadSpecEnforcement(filepath.Dir(wipnoteDir))
	if enforcement.PromoteSlice && strings.TrimSpace(slice.DecisionsNotes) == "" {
		if !allowSpecSkip {
			return "", fmt.Errorf("slice %d has no decisions; run `wipnote plan elicit-decisions %s %d` first (or invoke /wipnote:spec-from-slice on Claude). Override with --allow-spec-skip if intentional.",
				sliceNum, planID, sliceNum)
		}
		auditNote := fmt.Sprintf("[%s] promote-slice --allow-spec-skip: promoted without decisions_notes",
			time.Now().UTC().Format(time.RFC3339))
		if existing := strings.TrimSpace(plan.Slices[sliceIdx].Comment); existing == "" {
			plan.Slices[sliceIdx].Comment = auditNote
		} else {
			plan.Slices[sliceIdx].Comment = existing + "\n" + auditNote
		}
		fmt.Fprintln(stderr, "promote-slice: --allow-spec-skip set; bypassing spec_enforcement.promote_slice gate")
	}

	// Validate dependency readiness.
	if !waiveDeps && len(slice.Deps) > 0 {
		if err := checkDepReadiness(db, plan, planID, slice.Deps); err != nil {
			return "", err
		}
	}

	// Idempotent: if feature_id already set, reuse it.
	if slice.FeatureID != "" {
		// Still refresh execution_status='promoted' in case it was lost.
		// Record it on the plan already loaded/locked above and persist via
		// SaveLocked — this used to be dbpkg.StorePlanFeedback(db, ...)
		// against the ephemeral :memory: handle openPlanDB just opened
		// (OpenEphemeralProjection). That handle is closed (defer db.Close()
		// above) the moment this function returns and was NEVER read by
		// anything else in the meantime, so re-promoting an already-promoted
		// slice persisted nothing at all — a live bug, not a test artifact
		// (feat-fc3cc9e0). recordExecutionStatus mirrors what
		// storePlanFeedbackEntry's upsertPlanFeedback+mirrorPlanFeedback do,
		// but writes straight into the plan this function already holds
		// planyaml.LockPlanForWrite on; calling storePlanFeedback here would
		// re-enter that same non-reentrant lock and deadlock.
		recordExecutionStatus(plan, sectionKey, "promoted")
		if err := planyaml.SaveLocked(planPath, plan); err != nil {
			fmt.Fprintf(stderr, "promote-slice: refresh execution_status warning: %v\n", err)
		}
		return slice.FeatureID, nil
	}

	// Open project for work item creation.
	p, err := workitem.Open(wipnoteDir, agentForClaim())
	if err != nil {
		return "", fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	// Resolve track.
	if plan.Meta.TrackID == "" {
		return "", fmt.Errorf("plan %s has no track_id; link a track first", planID)
	}
	track, err := p.Tracks.Get(plan.Meta.TrackID)
	if err != nil {
		return "", fmt.Errorf("get track %s: %w", plan.Meta.TrackID, err)
	}

	// Create feature.
	feat, err := p.Features.Create(slice.Title,
		workitem.FeatWithTrack(track.ID),
		workitem.FeatWithContent(slice.What),
	)
	if err != nil {
		return "", fmt.Errorf("create feature: %w", err)
	}

	// Emit edges.
	if err := wireTrackEdges(p, feat.ID, track.ID, feat.Title); err != nil {
		return "", fmt.Errorf("wire track edges: %w", err)
	}
	p.Features.AddEdge(feat.ID, models.Edge{ //nolint:errcheck
		TargetID:     planID,
		Relationship: models.RelPlannedIn,
		Title:        planID,
		Since:        time.Now().UTC(),
	})

	// blocked_by edges for promoted dep slices that have a feature_id.
	for _, depNum := range slice.Deps {
		depSliceIdx := -1
		for i, s := range plan.Slices {
			if s.Num == depNum {
				depSliceIdx = i
				break
			}
		}
		if depSliceIdx >= 0 && plan.Slices[depSliceIdx].FeatureID != "" {
			p.Features.AddEdge(feat.ID, models.Edge{ //nolint:errcheck
				TargetID:     plan.Slices[depSliceIdx].FeatureID,
				Relationship: "blocked_by",
				Since:        time.Now().UTC(),
			})
		}
	}

	// Write feature_id back to YAML and record execution_status (both
	// plan.Feedback.Entries and slice.ExecutionStatus, via
	// recordExecutionStatus) on the plan already loaded/locked above, then
	// persist once. See the idempotent branch above for why this replaced a
	// dead dbpkg.StorePlanFeedback call against the ephemeral SQL handle.
	plan.Slices[sliceIdx].FeatureID = feat.ID
	recordExecutionStatus(plan, sectionKey, "promoted")
	if err := planyaml.SaveLocked(planPath, plan); err != nil {
		return "", fmt.Errorf("save plan: %w", err)
	}

	// Auto-commit (non-fatal on failure, matching finalize-yaml behaviour).
	commitMsg := fmt.Sprintf("plan(%s): promote slice-%d → %s", planID, sliceNum, feat.ID)
	if err := commitPlanChange(planPath, commitMsg); err != nil {
		fmt.Fprintf(stderr, "promote-slice: autocommit warning: %v\n", err)
	}

	return feat.ID, nil
}

// recordExecutionStatus mirrors a set_execution_status feedback entry into an
// ALREADY-LOADED plan — both plan.Feedback.Entries (upsertPlanFeedback) and
// slice.ExecutionStatus (mirrorPlanFeedback) — matching what
// storePlanFeedbackEntry (plan_feedback_yaml.go) does for the canonical CLI
// write path, minus its own file locking. Callers here already hold
// planyaml.LockPlanForWrite(planPath) across their whole load→mutate→save
// window (defect 4, feat-fc3cc9e0); calling storePlanFeedback/
// storePlanFeedbackEntry from inside that window would re-enter the same
// non-reentrant mutex and deadlock. Callers are responsible for the eventual
// planyaml.SaveLocked call.
func recordExecutionStatus(plan *planyaml.PlanYAML, sectionKey, status string) {
	entry := planyaml.PlanFeedbackEntry{Section: sectionKey, Action: "set_execution_status", Value: status}
	upsertPlanFeedback(plan, entry)
	mirrorPlanFeedback(plan, entry)
}

// findPlanSlice returns the index and value of the slice with the given num.
func findPlanSlice(plan *planyaml.PlanYAML, sliceNum int) (int, planyaml.PlanSlice, error) {
	for i, s := range plan.Slices {
		if s.Num == sliceNum {
			return i, s, nil
		}
	}
	return 0, planyaml.PlanSlice{}, fmt.Errorf("slice %d not found in plan %s", sliceNum, plan.Meta.ID)
}

// checkDepReadiness returns an error listing any dependency slices whose
// execution_status is not 'done' or 'superseded'.
func checkDepReadiness(db *sql.DB, _ *planyaml.PlanYAML, planID string, deps []int) error {
	statuses, err := getSliceExecutionStatuses(db, planID)
	if err != nil {
		return fmt.Errorf("read execution statuses: %w", err)
	}

	var blocking []string
	for _, depNum := range deps {
		key := fmt.Sprintf("slice-%d", depNum)
		st := statuses[key]
		if st != "done" && st != "superseded" {
			// Also accept 'promoted' as a non-blocking state per plan semantics,
			// but not 'not_started', 'in_progress', 'blocked', or ''.
			blocking = append(blocking, fmt.Sprintf("%s (status=%q)", key, st))
		}
	}
	if len(blocking) > 0 {
		return fmt.Errorf("blocking dependency slices not yet done: %s — use --waive-deps to override",
			strings.Join(blocking, ", "))
	}
	return nil
}

// getSliceExecutionStatuses returns a map of section key (e.g. "slice-1") to
// the most recent set_execution_status value recorded in plan_feedback.
func getSliceExecutionStatuses(db *sql.DB, planID string) (map[string]string, error) {
	rows, err := db.Query(`
		SELECT section, value
		FROM plan_feedback
		WHERE plan_id = ? AND action = 'set_execution_status' AND section LIKE 'slice-%'
		ORDER BY updated_at ASC`, planID)
	if err != nil {
		return nil, fmt.Errorf("query execution statuses (plan=%s): %w", planID, err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var section, value string
		if err := rows.Scan(&section, &value); err != nil {
			return nil, fmt.Errorf("scan execution status row: %w", err)
		}
		result[section] = value
	}
	return result, rows.Err()
}
