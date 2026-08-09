package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/filelock"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

var sliceQuestionSectionRe = regexp.MustCompile(`^slice-(\d+)-question-(.+)$`)

func readPlanFeedbackEntries(wipnoteDir, planID string) ([]dbpkg.PlanFeedback, error) {
	plan, err := planyaml.Load(planYAMLPath(wipnoteDir, planID))
	if err != nil {
		return nil, err
	}
	return feedbackEntriesFromPlan(planID, plan), nil
}

func storePlanFeedbackEntry(wipnoteDir, planID string, e planyaml.PlanFeedbackEntry) error {
	yamlPath := planYAMLPath(wipnoteDir, planID)
	releaseFile := filelock.Guard(yamlPath)
	defer releaseFile()
	releasePlan := planyaml.LockPlanForWrite(yamlPath)
	defer releasePlan()

	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		return err
	}
	upsertPlanFeedback(plan, e)
	mirrorPlanFeedback(plan, e)
	if err := planyaml.SaveLocked(yamlPath, plan); err != nil {
		return err
	}
	return renderPlanToFileQuiet(wipnoteDir, planID)
}

func planYAMLPath(wipnoteDir, planID string) string {
	return filepath.Join(wipnoteDir, "plans", planID+".yaml")
}

func upsertPlanFeedback(plan *planyaml.PlanYAML, e planyaml.PlanFeedbackEntry) {
	if plan.Feedback == nil {
		plan.Feedback = &planyaml.PlanFeedback{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if e.CreatedAt == "" {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	for i, existing := range plan.Feedback.Entries {
		if sameFeedbackKey(existing, e) {
			e.CreatedAt = existing.CreatedAt
			plan.Feedback.Entries[i] = e
			return
		}
	}
	plan.Feedback.Entries = append(plan.Feedback.Entries, e)
}

func sameFeedbackKey(a, b planyaml.PlanFeedbackEntry) bool {
	if a.Action == "annotation" || b.Action == "annotation" {
		return a.Section == b.Section && a.Action == b.Action && a.QuestionID == b.QuestionID && a.Anchor == b.Anchor
	}
	return a.Section == b.Section && a.Action == b.Action && a.QuestionID == b.QuestionID
}

func mirrorPlanFeedback(plan *planyaml.PlanYAML, e planyaml.PlanFeedbackEntry) {
	switch e.Action {
	case "approve":
		mirrorApproval(plan, e.Section, e.Value)
	case "comment":
		mirrorComment(plan, e.Section, e.Value)
	case "answer":
		mirrorAnswer(plan, e.Section, e.QuestionID, e.Value)
	case "set_execution_status":
		if idx := sliceIndexBySection(plan, e.Section); idx >= 0 {
			plan.Slices[idx].ExecutionStatus = e.Value
		}
	case "finalize":
		if e.Section == "meta" && e.Value == "true" {
			plan.Meta.Status = "finalized"
		}
	}
}

func mirrorApproval(plan *planyaml.PlanYAML, section, value string) {
	status := approvalStatusFromValue(value)
	switch section {
	case "design":
		plan.Design.Approved = status == "approved"
	default:
		if idx := sliceIndexBySection(plan, section); idx >= 0 {
			plan.Slices[idx].ApprovalStatus = status
			plan.Slices[idx].Approved = status == "approved"
		}
	}
}

func mirrorComment(plan *planyaml.PlanYAML, section, value string) {
	switch section {
	case "design":
		plan.Design.Comment = value
	default:
		if idx := sliceIndexBySection(plan, section); idx >= 0 {
			plan.Slices[idx].Comment = value
		}
	}
}

func mirrorAnswer(plan *planyaml.PlanYAML, section, questionID, value string) {
	if section == "questions" {
		setGlobalQuestionAnswer(plan, questionID, value)
		return
	}
	if m := sliceQuestionSectionRe.FindStringSubmatch(section); len(m) == 3 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			setSliceQuestionAnswer(plan, n, m[2], value)
		}
	}
}

func setGlobalQuestionAnswer(plan *planyaml.PlanYAML, questionID, value string) {
	for i := range plan.Questions {
		if plan.Questions[i].ID == questionID {
			v := value
			plan.Questions[i].Answer = &v
			return
		}
	}
}

func setSliceQuestionAnswer(plan *planyaml.PlanYAML, sliceNum int, questionID, value string) {
	for i := range plan.Slices {
		if plan.Slices[i].Num != sliceNum {
			continue
		}
		for j := range plan.Slices[i].Questions {
			if plan.Slices[i].Questions[j].ID == questionID {
				plan.Slices[i].Questions[j].Answer = value
				return
			}
		}
	}
}

func sliceIndexBySection(plan *planyaml.PlanYAML, section string) int {
	if !strings.HasPrefix(section, "slice-") || strings.Count(section, "-") != 1 {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimPrefix(section, "slice-"))
	if err != nil {
		return -1
	}
	for i := range plan.Slices {
		if plan.Slices[i].Num == n {
			return i
		}
	}
	return -1
}

func approvalStatusFromValue(value string) string {
	switch value {
	case "true", "1", "approved":
		return "approved"
	case "changes_requested":
		return "changes_requested"
	case "rejected":
		return "rejected"
	default:
		return "pending"
	}
}

func feedbackEntriesFromPlan(planID string, plan *planyaml.PlanYAML) []dbpkg.PlanFeedback {
	var out []dbpkg.PlanFeedback
	for i, e := range feedbackYAMLEntries(plan) {
		out = append(out, dbpkg.PlanFeedback{
			ID:               int64(i + 1),
			PlanID:           planID,
			Section:          e.Section,
			Action:           e.Action,
			Value:            e.Value,
			QuestionID:       e.QuestionID,
			Anchor:           e.Anchor,
			Consumed:         e.Consumed,
			Resolved:         e.Resolved,
			ResolutionTarget: e.ResolutionTarget,
			CreatedAt:        parseFeedbackTime(e.CreatedAt),
			UpdatedAt:        parseFeedbackTime(e.UpdatedAt),
		})
	}
	return out
}

func feedbackYAMLEntries(plan *planyaml.PlanYAML) []planyaml.PlanFeedbackEntry {
	if plan == nil || plan.Feedback == nil {
		return nil
	}
	return plan.Feedback.Entries
}

func parseFeedbackTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339, value)
	return t
}

func canonicalPlanApproved(wipnoteDir, planID string) (bool, []int, error) {
	plan, err := planyaml.Load(planYAMLPath(wipnoteDir, planID))
	if err != nil {
		return false, nil, err
	}
	var pending []int
	for _, s := range plan.Slices {
		if s.ApprovalStatus != "approved" && !s.Approved {
			pending = append(pending, s.Num)
		}
	}
	if len(plan.Slices) > 0 {
		return len(pending) == 0, pending, nil
	}
	return plan.Design.Approved, nil, nil
}

func storePlanFeedback(wipnoteDir, planID, section, action, value, questionID string) error {
	return storePlanFeedbackEntry(wipnoteDir, planID, planyaml.PlanFeedbackEntry{
		Section:    section,
		Action:     action,
		Value:      value,
		QuestionID: questionID,
	})
}

func storePlanAnnotation(wipnoteDir, planID string, req planFeedbackRequest) error {
	if req.Anchor == "" {
		return fmt.Errorf("annotation anchor is required")
	}
	return storePlanFeedbackEntry(wipnoteDir, planID, planyaml.PlanFeedbackEntry{
		Section:          req.Section,
		Action:           "annotation",
		Value:            req.Value,
		QuestionID:       req.QuestionID,
		Anchor:           req.Anchor,
		Consumed:         req.Consumed,
		Resolved:         req.Resolved,
		ResolutionTarget: req.ResolutionTarget,
	})
}

func planChatSessionID(wipnoteDir, planID string) string {
	entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Section == "chat" && e.Action == "session_id" {
			return e.Value
		}
	}
	return ""
}

func appendPlanChatMessage(wipnoteDir, planID, role, content string) error {
	entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
	if err != nil {
		return err
	}
	var messages []chatMessageEntry
	for _, e := range entries {
		if e.Section != "chat" || e.Action != "messages" || e.Value == "" {
			continue
		}
		_ = json.Unmarshal([]byte(e.Value), &messages)
		break
	}
	messages = append(messages, chatMessageEntry{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	data, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	return storePlanFeedback(wipnoteDir, planID, "chat", "messages", string(data), "")
}
