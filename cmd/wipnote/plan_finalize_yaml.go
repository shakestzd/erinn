package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/filelock"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

// planAmendment represents a structured amendment directive from the chat review system.
type planAmendment struct {
	SliceNum  int    `json:"slice_num"`
	Field     string `json:"field"`
	Operation string `json:"operation"`
	Content   string `json:"content"`
}

// planFinalizeYAMLCmd creates track + features from approved slices in a YAML plan.
func planFinalizeYAMLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "finalize-yaml <plan-id>",
		Short: "Create track and features from approved YAML plan slices (dashboard flow)",
		Long: `Read a YAML plan + SQLite plan_feedback approvals, create a track and
features for approved slices, wire dependency edges. Updates YAML status to
finalized.

This is the dashboard-review workflow: only slices with explicit approve
actions in plan_feedback get promoted, and the track is created from scratch
when one does not yet exist.

For the simpler hierarchy-only flow that requires an existing track and
promotes every slice unconditionally, use 'plan finalize' instead.

Example:
  wipnote plan finalize-yaml plan-a1b2c3d4`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runFinalizeYAML(args[0])
		},
	}
}

func runFinalizeYAML(planID string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	return finalizeYAML(wipnoteDir, planID)
}

// finalizeYAML is the testable inner implementation of runFinalizeYAML.
// It takes an explicit wipnoteDir rather than resolving it from the environment.
func finalizeYAML(wipnoteDir, planID string) error {
	featIDs, _, err := finalizeYAMLCanonical(wipnoteDir, planID)
	if err != nil {
		return err
	}

	// Print summary via CLI path (load plan again for summary context).
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, loadErr := planyaml.Load(planPath)
	if loadErr != nil {
		return nil // summary is optional; creation already succeeded
	}
	answers := loadPlanAnswersFromPlan(plan)
	var rejectedTitles []string
	approvals := loadPlanApprovalsFromPlan(plan)
	for _, s := range plan.Slices {
		if !approvals[fmt.Sprintf("slice-%d", s.Num)] {
			rejectedTitles = append(rejectedTitles, s.Title)
		}
	}
	var track *models.Node
	if plan.Meta.TrackID != "" {
		p, pErr := workitem.Open(wipnoteDir, agentForClaim())
		if pErr == nil {
			defer p.Close()
			track, _ = p.Tracks.Get(plan.Meta.TrackID)
		}
	}
	printFinalizeYAMLSummary(plan, track, answers, featIDs, rejectedTitles)
	return nil
}

func finalizeYAMLCanonical(wipnoteDir, planID string) (createdIDs []string, failures []finalizeFailure, err error) {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")

	// Hold the lock across the whole load→mutate→save window (defect 4,
	// feat-fc3cc9e0): finalize reads the plan, creates features and wires
	// edges in between, then saves the whole document back via
	// saveFinalizedPlan. A bare Load-then-Save let two concurrent writers
	// interleave and the second whole-document save silently clobber the
	// first. Mirrors storePlanFeedbackEntry.
	releaseFile := filelock.Guard(planPath)
	defer releaseFile()
	releasePlan := planyaml.LockPlanForWrite(planPath)
	defer releasePlan()

	plan, err := planyaml.Load(planPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load plan: %w", err)
	}
	approvals := loadPlanApprovalsFromPlan(plan)
	answers := loadPlanAnswersFromPlan(plan)
	applyAmendments(plan, loadPlanAmendmentsFromPlan(plan))

	p, pErr := workitem.Open(wipnoteDir, agentForClaim())
	if pErr != nil {
		return nil, nil, fmt.Errorf("open project: %w", pErr)
	}
	defer p.Close()

	if plan.Meta.Status == "finalized" {
		existing := existingFeatureIDsFromPlan(plan)
		if len(existing) == 0 {
			existing = findFeaturesForPlanCanonical(p, planID)
		}
		if len(existing) > 0 {
			return existing, nil, nil
		}
		plan.Meta.Status = ""
	}

	track, err := ensurePlanTrack(p, plan)
	if err != nil {
		return nil, nil, err
	}
	numToFeat, failures := createApprovedPlanFeatures(p, plan, track, approvals, answers)
	linkPlanFinalizationEdges(p, planID, track.ID, plan.Slices, numToFeat)
	if saveErr := saveFinalizedPlan(planPath, plan, track.ID, approvals, numToFeat); saveErr != nil {
		return nil, failures, saveErr
	}
	if commitErr := commitPlanChange(planPath, fmt.Sprintf("plan(%s): finalize — %d slices approved", planID, len(numToFeat))); commitErr != nil {
		return nil, failures, fmt.Errorf("autocommit finalize-yaml: %w", commitErr)
	}
	return createdFeatureIDs(plan.Slices, numToFeat), failures, nil
}

type finalizedFeature struct {
	id    string
	title string
}

// finalizeFailure records a slice number and error for feature creation failures.
type finalizeFailure struct {
	SliceNum int    `json:"slice_num"`
	Title    string `json:"title"`
	Error    string `json:"error"`
}

// finalizeYAMLWithDB creates a track and features from approved slices using a
// caller-supplied database connection. It returns the IDs of created features
// and any per-slice failures. Partial success is not an error — callers should
// inspect both return values.
func finalizeYAMLWithDB(db *sql.DB, wipnoteDir, planID string) (createdIDs []string, failures []finalizeFailure, err error) {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load plan: %w", err)
	}

	approvals := loadPlanApprovals(db, planID)
	answers := loadPlanAnswers(db, planID)

	// Read accepted amendments from SQLite.
	amendRows, qErr := db.Query(
		"SELECT value FROM plan_feedback WHERE plan_id = ? AND section = 'amendment' AND action = 'accepted'",
		planID,
	)
	if qErr != nil {
		return nil, nil, fmt.Errorf("query amendments: %w", qErr)
	}
	defer amendRows.Close()

	var amendments []planAmendment
	for amendRows.Next() {
		var raw string
		amendRows.Scan(&raw)
		var a planAmendment
		if json.Unmarshal([]byte(raw), &a) == nil {
			amendments = append(amendments, a)
		}
	}

	// Apply accepted amendments to plan slices in memory.
	applyAmendments(plan, amendments)

	// Open project for work item creation.
	p, pErr := workitem.Open(wipnoteDir, agentForClaim())
	if pErr != nil {
		return nil, nil, fmt.Errorf("open project: %w", pErr)
	}
	defer p.Close()

	// Idempotent re-finalize: plan already finalized AND features already exist —
	// short-circuit to avoid duplicate creation. If the plan was marked finalized
	// but features were never created (e.g. due to a bug in a prior run), fall
	// through so this run creates them.
	if plan.Meta.Status == "finalized" {
		existing := existingFeatureIDsFromPlan(plan)
		if len(existing) == 0 {
			existing = findFeaturesForPlanCanonical(p, planID)
		}
		if len(existing) > 0 {
			return existing, nil, nil
		}
		// Fall through: finalized but no features yet — create them now.
		// Reset status so Save at the end of this function re-writes it.
		plan.Meta.Status = ""
	}

	// Reuse existing track when meta.track_id references a valid track;
	// otherwise create a new one from the plan title.
	var track *models.Node
	if plan.Meta.TrackID != "" {
		existing, getErr := p.Tracks.Get(plan.Meta.TrackID)
		if getErr == nil && existing != nil {
			track = existing
		}
	}
	if track == nil {
		track, err = p.Tracks.Create(plan.Meta.Title)
		if err != nil {
			return nil, nil, fmt.Errorf("create track: %w", err)
		}
	}

	// Create features for approved slices, embedding design decisions.
	type createdFeat struct {
		id    string
		title string
	}
	numToFeat := map[int]createdFeat{}
	for _, s := range plan.Slices {
		approved := approvals[fmt.Sprintf("slice-%d", s.Num)]
		if !approved {
			continue
		}
		content := buildFeatureContent(s.What, plan.Questions, answers)
		feat, featErr := p.Features.Create(s.Title,
			workitem.FeatWithTrack(track.ID),
			workitem.FeatWithContent(content),
		)
		if featErr != nil {
			failures = append(failures, finalizeFailure{
				SliceNum: s.Num,
				Title:    s.Title,
				Error:    featErr.Error(),
			})
			continue
		}
		numToFeat[s.Num] = createdFeat{id: feat.ID, title: feat.Title}

		// Link feature back to source plan (planned_in).
		p.Features.AddEdge(feat.ID, models.Edge{
			TargetID:     planID,
			Relationship: models.RelPlannedIn,
			Title:        planID,
			Since:        time.Now().UTC(),
		})

		// Wire part_of (feature→track) and contains (track→feature) edges.
		wireTrackEdges(p, feat.ID, track.ID, feat.Title) //nolint:errcheck
	}

	// Link plan to track: plan implemented_in track.
	p.Plans.AddEdge(planID, models.Edge{
		TargetID:     track.ID,
		Relationship: models.RelImplementedIn,
		Title:        track.ID,
		Since:        time.Now().UTC(),
	})

	// Wire blocked_by edges from slice deps.
	for _, s := range plan.Slices {
		cf, ok := numToFeat[s.Num]
		if !ok {
			continue
		}
		for _, depNum := range s.Deps {
			depCF, ok := numToFeat[depNum]
			if !ok {
				continue
			}
			p.Features.AddEdge(cf.id, models.Edge{
				TargetID:     depCF.id,
				Relationship: "blocked_by",
			})
		}
	}

	// Update YAML status.
	plan.Meta.Status = "finalized"
	plan.Meta.TrackID = track.ID
	for i := range plan.Slices {
		plan.Slices[i].Approved = approvals[fmt.Sprintf("slice-%d", plan.Slices[i].Num)]
		if cf, ok := numToFeat[plan.Slices[i].Num]; ok {
			plan.Slices[i].FeatureID = cf.id
			plan.Slices[i].ExecutionStatus = "promoted"
		}
	}
	if saveErr := planyaml.Save(planPath, plan); saveErr != nil {
		return nil, failures, fmt.Errorf("save plan: %w", saveErr)
	}

	if commitErr := commitPlanChange(planPath, fmt.Sprintf("plan(%s): finalize — %d slices approved", planID, len(numToFeat))); commitErr != nil {
		return nil, failures, fmt.Errorf("autocommit finalize-yaml: %w", commitErr)
	}

	// Build feat IDs list in slice order.
	for _, s := range plan.Slices {
		if cf, ok := numToFeat[s.Num]; ok {
			createdIDs = append(createdIDs, cf.id)
		}
	}
	return createdIDs, failures, nil
}

func ensurePlanTrack(p *workitem.Project, plan *planyaml.PlanYAML) (*models.Node, error) {
	if plan.Meta.TrackID != "" {
		existing, err := p.Tracks.Get(plan.Meta.TrackID)
		if err == nil && existing != nil {
			return existing, nil
		}
	}
	track, err := p.Tracks.Create(plan.Meta.Title)
	if err != nil {
		return nil, fmt.Errorf("create track: %w", err)
	}
	return track, nil
}

func createApprovedPlanFeatures(
	p *workitem.Project,
	plan *planyaml.PlanYAML,
	track *models.Node,
	approvals map[string]bool,
	answers map[string]string,
) (map[int]finalizedFeature, []finalizeFailure) {
	numToFeat := map[int]finalizedFeature{}
	var failures []finalizeFailure
	for _, s := range plan.Slices {
		if !approvals[fmt.Sprintf("slice-%d", s.Num)] {
			continue
		}
		content := buildFeatureContent(s.What, plan.Questions, answers)
		feat, err := p.Features.Create(s.Title, workitem.FeatWithTrack(track.ID), workitem.FeatWithContent(content))
		if err != nil {
			failures = append(failures, finalizeFailure{SliceNum: s.Num, Title: s.Title, Error: err.Error()})
			continue
		}
		numToFeat[s.Num] = finalizedFeature{id: feat.ID, title: feat.Title}
		p.Features.AddEdge(feat.ID, models.Edge{
			TargetID:     plan.Meta.ID,
			Relationship: models.RelPlannedIn,
			Title:        plan.Meta.ID,
			Since:        time.Now().UTC(),
		})
		wireTrackEdges(p, feat.ID, track.ID, feat.Title) //nolint:errcheck
	}
	return numToFeat, failures
}

func linkPlanFinalizationEdges(
	p *workitem.Project,
	planID, trackID string,
	slices []planyaml.PlanSlice,
	numToFeat map[int]finalizedFeature,
) {
	p.Plans.AddEdge(planID, models.Edge{TargetID: trackID, Relationship: models.RelImplementedIn, Title: trackID, Since: time.Now().UTC()})
	for _, s := range slices {
		cf, ok := numToFeat[s.Num]
		if !ok {
			continue
		}
		for _, depNum := range s.Deps {
			if depCF, ok := numToFeat[depNum]; ok {
				p.Features.AddEdge(cf.id, models.Edge{TargetID: depCF.id, Relationship: "blocked_by"})
			}
		}
	}
}

// saveFinalizedPlan's only caller, finalizeYAMLCanonical, already holds
// planyaml.LockPlanForWrite(planPath) across its whole load→mutate→save
// window (defect 4, feat-fc3cc9e0) — SaveLocked (not Save) avoids re-entering
// that non-reentrant mutex.
func saveFinalizedPlan(
	planPath string,
	plan *planyaml.PlanYAML,
	trackID string,
	approvals map[string]bool,
	numToFeat map[int]finalizedFeature,
) error {
	plan.Meta.Status = "finalized"
	plan.Meta.TrackID = trackID
	for i := range plan.Slices {
		num := plan.Slices[i].Num
		plan.Slices[i].Approved = approvals[fmt.Sprintf("slice-%d", num)]
		if cf, ok := numToFeat[num]; ok {
			plan.Slices[i].FeatureID = cf.id
			plan.Slices[i].ExecutionStatus = "promoted"
		}
	}
	if err := storeFinalizedFeedbackMarker(plan); err != nil {
		return err
	}
	if err := planyaml.SaveLocked(planPath, plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}
	return nil
}

func storeFinalizedFeedbackMarker(plan *planyaml.PlanYAML) error {
	upsertPlanFeedback(plan, planyaml.PlanFeedbackEntry{Section: "meta", Action: "finalize", Value: "true"})
	mirrorPlanFeedback(plan, planyaml.PlanFeedbackEntry{Section: "meta", Action: "finalize", Value: "true"})
	return nil
}

func createdFeatureIDs(slices []planyaml.PlanSlice, numToFeat map[int]finalizedFeature) []string {
	var ids []string
	for _, s := range slices {
		if cf, ok := numToFeat[s.Num]; ok {
			ids = append(ids, cf.id)
		}
	}
	return ids
}

func existingFeatureIDsFromPlan(plan *planyaml.PlanYAML) []string {
	var ids []string
	for _, s := range plan.Slices {
		if s.FeatureID != "" {
			ids = append(ids, s.FeatureID)
		}
	}
	return ids
}

// loadPlanApprovals reads approve actions from plan_feedback and returns a map
// from section key (e.g. "slice-1") to approved state.
func loadPlanApprovals(db *sql.DB, planID string) map[string]bool {
	approvals := map[string]bool{}
	rows, err := db.Query(
		"SELECT section, value FROM plan_feedback WHERE plan_id = ? AND action = 'approve'",
		planID,
	)
	if err != nil {
		return approvals
	}
	defer rows.Close()
	for rows.Next() {
		var section, value string
		rows.Scan(&section, &value)
		approvals[section] = dbpkg.IsPlanApprovalValueApproved(value)
	}
	return approvals
}

// loadPlanAnswers reads answer actions from plan_feedback and returns a map
// from question ID to the selected option key.
func loadPlanAnswers(db *sql.DB, planID string) map[string]string {
	answers := map[string]string{}
	rows, err := db.Query(
		"SELECT question_id, value FROM plan_feedback WHERE plan_id = ? AND action = 'answer'",
		planID,
	)
	if err != nil {
		return answers
	}
	defer rows.Close()
	for rows.Next() {
		var qID, value string
		rows.Scan(&qID, &value)
		answers[qID] = value
	}
	return answers
}

func loadPlanApprovalsFromPlan(plan *planyaml.PlanYAML) map[string]bool {
	approvals := map[string]bool{}
	if plan == nil {
		return approvals
	}
	for _, s := range plan.Slices {
		approvals[fmt.Sprintf("slice-%d", s.Num)] = s.ApprovalStatus == "approved" || s.Approved
	}
	for _, e := range feedbackYAMLEntries(plan) {
		if e.Action == "approve" {
			approvals[e.Section] = approvalStatusFromValue(e.Value) == "approved"
		}
	}
	return approvals
}

func loadPlanAnswersFromPlan(plan *planyaml.PlanYAML) map[string]string {
	answers := map[string]string{}
	if plan == nil {
		return answers
	}
	for _, q := range plan.Questions {
		if q.Answer != nil {
			answers[q.ID] = *q.Answer
		}
	}
	for _, e := range feedbackYAMLEntries(plan) {
		if e.Action == "answer" && e.QuestionID != "" {
			answers[e.QuestionID] = e.Value
		}
	}
	return answers
}

func loadPlanAmendmentsFromPlan(plan *planyaml.PlanYAML) []planAmendment {
	var amendments []planAmendment
	for _, e := range feedbackYAMLEntries(plan) {
		if e.Section != "amendment" || e.Action != "accepted" {
			continue
		}
		var a planAmendment
		if json.Unmarshal([]byte(e.Value), &a) == nil {
			amendments = append(amendments, a)
		}
	}
	return amendments
}

// buildFeatureContent constructs a feature description from a slice's "what" field
// plus an "Accepted Design Decisions" section derived from answered questions.
func buildFeatureContent(what string, questions []planyaml.PlanQuestion, answers map[string]string) string {
	if len(questions) == 0 {
		return what
	}

	var sb strings.Builder
	sb.WriteString(what)

	hasDecisions := false
	for _, q := range questions {
		optionKey := answers[q.ID]
		if optionKey == "" && q.Recommended == "" {
			continue // nothing to embed
		}
		hasDecisions = true
		break
	}
	if !hasDecisions {
		return what
	}

	sb.WriteString("\n\n## Accepted Design Decisions\n")
	for _, q := range questions {
		optionKey := answers[q.ID]
		label := ""
		isUnanswered := false

		if optionKey == "" {
			// Fall back to recommended option.
			optionKey = q.Recommended
			isUnanswered = true
		}

		for _, opt := range q.Options {
			if opt.Key == optionKey {
				label = opt.Label
				break
			}
		}
		if label == "" {
			label = optionKey
		}

		suffix := ""
		if isUnanswered {
			suffix = " (unanswered, using recommended)"
		}
		fmt.Fprintf(&sb, "- **%s** → %s (Q: %s)%s\n", q.Text, label, q.ID, suffix)
	}

	return sb.String()
}

// printFinalizeYAMLSummary prints the structured dispatch summary to stdout.
// featIDs may reference existing features (re-finalize) or newly created ones.
// rejectedTitles lists slice titles that were not approved.
func printFinalizeYAMLSummary(
	plan *planyaml.PlanYAML,
	track *models.Node,
	answers map[string]string,
	featIDs []string,
	rejectedTitles []string,
) {
	trackID := ""
	trackTitle := ""
	if track != nil {
		trackID = track.ID
		trackTitle = track.Title
	} else if plan.Meta.TrackID != "" {
		trackID = plan.Meta.TrackID
	}

	totalSlices := len(plan.Slices)
	approvedCount := len(featIDs)
	rejectedCount := len(rejectedTitles)
	explicitAnswers := 0
	recommendedFallbacks := 0
	for _, q := range plan.Questions {
		if answers[q.ID] != "" {
			explicitAnswers++
		} else if q.Recommended != "" {
			recommendedFallbacks++
		}
	}

	fmt.Printf("\nPlan %s dispatched.\n", plan.Meta.ID)
	fmt.Println()
	if trackTitle != "" {
		fmt.Printf("Track:        %s (%s)\n", trackID, trackTitle)
	} else {
		fmt.Printf("Track:        %s\n", trackID)
	}
	fmt.Printf("Approved:     %d of %d slices\n", approvedCount, totalSlices)
	if rejectedCount > 0 {
		fmt.Printf("Rejected:     %d slices (excluded from dispatch)\n", rejectedCount)
	}

	if len(featIDs) > 0 {
		fmt.Println("\nFeatures created:")
		for i, fid := range featIDs {
			sliceTitle := ""
			if i < len(plan.Slices) {
				// Find matching approved slice title.
				sliceTitle = plan.Slices[i].Title
			}
			fmt.Printf("  %-20s  %s\n", fid, sliceTitle)
		}
	}

	if len(rejectedTitles) > 0 {
		fmt.Println("\nRejected (excluded):")
		for _, t := range rejectedTitles {
			fmt.Printf("  %s  (not approved — excluded)\n", t)
		}
	}

	// Design decisions section: resolved questions with explicit/fallback breakdown.
	type resolvedDecision struct {
		qID   string
		text  string
		label string
		isRec bool // true if resolved via recommended fallback
	}
	var resolvedDecisions []resolvedDecision
	for _, q := range plan.Questions {
		optKey := answers[q.ID]
		isRec := false
		if optKey == "" {
			optKey = q.Recommended
			isRec = true
		}
		if optKey == "" {
			continue
		}
		label := optKey
		for _, opt := range q.Options {
			if opt.Key == optKey {
				label = opt.Label
				break
			}
		}
		resolvedDecisions = append(resolvedDecisions, resolvedDecision{
			qID:   q.ID,
			text:  q.Text,
			label: label,
			isRec: isRec,
		})
	}
	if len(resolvedDecisions) > 0 {
		fmt.Printf("\nDesign decisions (%d, %d explicit / %d recommended defaults):\n",
			len(resolvedDecisions), explicitAnswers, recommendedFallbacks)
		for _, d := range resolvedDecisions {
			fmt.Printf("  %-30s  → %s\n", d.qID, d.label)
		}
	}

	// Use the shared helper so the command format is defined in one place
	// (api_plans.go:planNextCommands) and matches what the dashboard surfaces.
	nextCmd, yoloCmd := planNextCommands(plan.Meta.ID, trackID)
	fmt.Println("\nNext:")
	fmt.Printf("  %s   (in Claude — dispatches tasks)\n", nextCmd)
	fmt.Printf("  OR: %s   (autonomous mode)\n", yoloCmd)
}

// applyAmendments applies accepted amendment directives to plan slices in memory.
// Amendments are applied in order; later amendments for the same field win.
func applyAmendments(plan *planyaml.PlanYAML, amendments []planAmendment) {
	for _, a := range amendments {
		idx := -1
		for i, s := range plan.Slices {
			if s.Num == a.SliceNum {
				idx = i
				break
			}
		}
		if idx < 0 {
			fmt.Fprintf(os.Stderr, "  Amendment skipped: slice %d not found\n", a.SliceNum)
			continue
		}
		s := &plan.Slices[idx]

		switch a.Operation {
		case "add":
			switch a.Field {
			case "done_when":
				s.DoneWhen = append(s.DoneWhen, a.Content)
			case "files":
				s.Files = append(s.Files, a.Content)
			}
		case "remove":
			switch a.Field {
			case "done_when":
				s.DoneWhen = removeStr(s.DoneWhen, a.Content)
			case "files":
				s.Files = removeStr(s.Files, a.Content)
			}
		case "set":
			switch a.Field {
			case "title":
				s.Title = a.Content
			case "what":
				s.What = a.Content
			case "why":
				s.Why = a.Content
			case "effort":
				s.Effort = a.Content
			case "risk":
				s.Risk = a.Content
			}
		}
		fmt.Printf("  Applied amendment: slice-%d %s %s\n", a.SliceNum, a.Operation, a.Field)
	}
}

// removeStr returns a new slice with all occurrences of target removed.
func removeStr(slice []string, target string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != target {
			result = append(result, s)
		}
	}
	return result
}

// findFeaturesForPlan returns feature IDs that have a planned_in edge pointing
// to planID, queried directly from the graph_edges SQLite table. This is the
// correct lookup for the re-finalize path because the yaml finalize first-run
// only writes planned_in edges, not part_of/contains edges.
func findFeaturesForPlan(db *sql.DB, planID string) []string {
	if db == nil {
		return nil
	}
	rows, err := db.Query(
		"SELECT from_node_id FROM graph_edges WHERE to_node_id = ? AND relationship_type = 'planned_in' AND from_node_id LIKE 'feat-%'",
		planID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func findFeaturesForPlanCanonical(p *workitem.Project, planID string) []string {
	features, err := p.Features.List()
	if err != nil {
		return nil
	}
	var ids []string
	for _, feat := range features {
		for _, edge := range feat.Edges[string(models.RelPlannedIn)] {
			if edge.TargetID == planID {
				ids = append(ids, feat.ID)
				break
			}
		}
	}
	return ids
}
