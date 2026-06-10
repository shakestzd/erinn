package db

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"time"
)

// PlanFeedback represents a single feedback entry captured from a CRISPI plan review.
type PlanFeedback struct {
	ID         int64
	PlanID     string
	Section    string
	Action     string
	Value      string
	QuestionID string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// StorePlanFeedback upserts a feedback entry for a plan section.
// Re-submitting feedback for the same (plan_id, section, action, question_id)
// updates the existing row rather than creating a duplicate.
func StorePlanFeedback(db *sql.DB, planID, section, action, value, questionID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO plan_feedback (plan_id, section, action, value, question_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(plan_id, section, action, question_id) DO UPDATE SET
			value      = excluded.value,
			updated_at = excluded.updated_at`,
		planID, section, action, nullStr(value), questionID, now, now,
	)
	if err != nil {
		return fmt.Errorf("store plan feedback (plan=%s section=%s action=%s): %w", planID, section, action, err)
	}
	return nil
}

// GetPlanFeedback retrieves all feedback entries for a plan, ordered by created_at.
func GetPlanFeedback(db *sql.DB, planID string) ([]PlanFeedback, error) {
	rows, err := db.Query(`
		SELECT id, plan_id, section, action, value, question_id, created_at, updated_at
		FROM plan_feedback
		WHERE plan_id = ?
		ORDER BY created_at ASC`, planID)
	if err != nil {
		return nil, fmt.Errorf("get plan feedback (plan=%s): %w", planID, err)
	}
	defer rows.Close()
	return scanPlanFeedbackRows(rows)
}

// GetPlanFeedbackBySection retrieves feedback for a specific section of a plan.
func GetPlanFeedbackBySection(db *sql.DB, planID, section string) ([]PlanFeedback, error) {
	rows, err := db.Query(`
		SELECT id, plan_id, section, action, value, question_id, created_at, updated_at
		FROM plan_feedback
		WHERE plan_id = ? AND section = ?
		ORDER BY created_at ASC`, planID, section)
	if err != nil {
		return nil, fmt.Errorf("get plan feedback by section (plan=%s section=%s): %w", planID, section, err)
	}
	defer rows.Close()
	return scanPlanFeedbackRows(rows)
}

// IsPlanApprovalSection returns true if the section is one of the approvable sections
// exposed in the CRISPI plan UI: "design", "outline", or slice-N (where N is numeric).
// All other sections (slice-N-question-*, critique, q-*, meta, chat, etc.) are excluded.
func IsPlanApprovalSection(section string) bool {
	if section == "design" || section == "outline" {
		return true
	}
	// Match slice-N where N is one or more digits.
	matched, err := regexp.MatchString(`^slice-\d+$`, section)
	return err == nil && matched
}

// IsPlanApprovalValueApproved reports whether a stored plan_feedback approve
// value represents approval. "approved" is a legacy UI value that may still be
// visible to already-open DB handles that predate normalization.
func IsPlanApprovalValueApproved(value string) bool {
	switch value {
	case "true", "approved":
		return true
	default:
		return false
	}
}

// PlanSliceApproval is a minimal slice descriptor used by canonical-first approval
// checks. Callers in cmd/ populate this from planyaml.PlanSlice to avoid a
// circular import (cmd/ → plan/planyaml is fine; core/db → plan/planyaml is not).
type PlanSliceApproval struct {
	Num            int
	ApprovalStatus string // "approved" | "rejected" | "pending" | ""
	Approved       bool   // legacy bool field, written by finalize-yaml
}

// IsPlanFullyApproved reports whether a plan is approved enough to finalize.
//
// For v2 (slice-card) plans — any plan that has slice-N approval rows — the gate
// is SLICES ONLY: every slice with an approval row must be approved and none
// rejected. design/outline approval is NOT required and does not block. This
// matches the dashboard review rail (the canonical behavior) and the plan-page
// finalize button, so the three finalize paths agree.
//
// Canonical-first fallback (bug-eca8141d): if plan_feedback has NO slice rows
// but the caller supplies yamlSlices, the function reads approval state from the
// YAML fields (ApprovalStatus / Approved). This recovers plans whose cache was
// rebuilt after all slices were approved — the exact user-facing symptom where
// 8/8 slices showed approved in the UI but Finalize rejected "not all sections
// approved". Pass nil for yamlSlices to retain the previous pure-SQLite behavior.
//
// For legacy plans with no slice sections at all, it falls back to the original
// behavior (design/outline must be approved, none disapproved), so older plans
// keep finalizing as before.
//
// Returns false (not an error) when nothing approvable exists yet.
func IsPlanFullyApproved(db *sql.DB, planID string, yamlSlices []PlanSliceApproval) (bool, error) {
	// v2 path: if the plan has any slice approval rows, gate on slices only.
	sliceApprovals, err := GetSliceApprovals(db, planID)
	if err != nil {
		return false, err
	}
	if len(sliceApprovals) > 0 {
		for _, status := range sliceApprovals {
			if status != "approved" {
				return false, nil
			}
		}
		return true, nil
	}

	// Canonical-first fallback: no SQLite rows, but YAML slices supplied.
	// Check every slice's ApprovalStatus / Approved field from the YAML.
	// This is the recovery path after a cache rebuild (bug-eca8141d).
	if len(yamlSlices) > 0 {
		for _, s := range yamlSlices {
			approved := s.ApprovalStatus == "approved" || s.Approved
			if !approved {
				return false, nil
			}
		}
		fmt.Fprintf(os.Stderr, "wipnote: plan %s: IsPlanFullyApproved: no plan_feedback rows — used canonical YAML approval state as fallback (cache rebuild recovery)\n", planID)
		return true, nil
	}

	// Legacy fallback: no slice sections — preserve original design/outline gate.
	return legacyPlanFullyApproved(db, planID)
}

// legacyPlanFullyApproved is the pre-slice-gating behavior: every UI-exposed
// section (design, outline) with an approve row must be approved and none
// disapproved. Retained for plans that predate slice-card sections.
func legacyPlanFullyApproved(db *sql.DB, planID string) (bool, error) {
	// Fetch all approve/disapprove feedback rows for this plan.
	rows, err := db.Query(`
		SELECT DISTINCT section, value
		FROM plan_feedback
		WHERE plan_id = ? AND action = 'approve'
		ORDER BY section ASC`, planID)
	if err != nil {
		return false, fmt.Errorf("fetch approve feedback (plan=%s): %w", planID, err)
	}
	defer rows.Close()

	// Track UI-exposed sections and their approval status.
	exposedApproved := make(map[string]bool)
	exposedDisapproved := make(map[string]bool)

	for rows.Next() {
		var section, value string
		if err := rows.Scan(&section, &value); err != nil {
			return false, fmt.Errorf("scan approve feedback: %w", err)
		}

		// Only consider UI-exposed sections.
		if !IsPlanApprovalSection(section) {
			continue
		}

		if IsPlanApprovalValueApproved(value) {
			exposedApproved[section] = true
		} else {
			exposedDisapproved[section] = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("scan approve feedback rows: %w", err)
	}

	// Plan is fully approved iff:
	// 1. At least one UI-exposed section exists and is approved (not blocked case).
	// 2. No UI-exposed section has a disapproval.
	hasAnyApproved := len(exposedApproved) > 0
	hasAnyDisapproved := len(exposedDisapproved) > 0

	return hasAnyApproved && !hasAnyDisapproved, nil
}

// FinalizePlan marks a plan as done. If the plan exists in the features table,
// updates its status there. Plans that exist only as HTML files (not indexed)
// are finalized successfully without a features table update.
func FinalizePlan(db *sql.DB, planID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE features SET status = 'done', updated_at = ?
		WHERE id = ? AND type = 'plan'`,
		now, planID,
	)
	if err != nil {
		return fmt.Errorf("finalize plan %s: %w", planID, err)
	}
	// Best-effort: don't fail if plan isn't in features table.
	// HTML is canonical — the on-disk HTML file gets data-status="finalized".
	return nil
}

// GetSliceApprovals returns a map of section key → approval status string
// ("approved" or "rejected") for all slice-N keyed feedback rows for the plan.
// Only sections matching the slice-<num> pattern are returned; legacy design/
// outline/questions sections are excluded.
// This is a read-only helper introduced in slice-4 for the approve-slice /
// reject-slice lifecycle commands.
func GetSliceApprovals(db *sql.DB, planID string) (map[string]string, error) {
	rows, err := db.Query(`
		SELECT section, value
		FROM plan_feedback
		WHERE plan_id = ? AND action = 'approve' AND section LIKE 'slice-%'
		ORDER BY section ASC`, planID)
	if err != nil {
		return nil, fmt.Errorf("get slice approvals (plan=%s): %w", planID, err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var section, value string
		if err := rows.Scan(&section, &value); err != nil {
			return nil, fmt.Errorf("scan slice approval row: %w", err)
		}
		// Only include actual slice-N sections (not slice-N-question-*).
		if IsPlanApprovalSection(section) {
			if IsPlanApprovalValueApproved(value) {
				result[section] = "approved"
			} else {
				result[section] = "rejected"
			}
		}
	}
	return result, rows.Err()
}

// DeletePlanFeedback deletes all feedback entries for a plan.
func DeletePlanFeedback(db *sql.DB, planID string) error {
	_, err := db.Exec(`DELETE FROM plan_feedback WHERE plan_id = ?`, planID)
	if err != nil {
		return fmt.Errorf("delete plan feedback (plan=%s): %w", planID, err)
	}
	return nil
}

// NormalizePlanFeedbackValues migrates existing rows that were written by the
// slice-card UI before the value-mapping fix. The buggy writer stored display
// values ('approved', 'changes_requested', 'rejected') instead of the canonical
// boolean strings ('true', 'false'). This function normalizes them. It is safe
// to call repeatedly — once migrated, no rows match the WHERE clauses.
func NormalizePlanFeedbackValues(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE plan_feedback SET value='true' WHERE action='approve' AND value='approved'`); err != nil {
		return fmt.Errorf("normalize plan_feedback approved→true: %w", err)
	}
	if _, err := db.Exec(`UPDATE plan_feedback SET value='false' WHERE action='approve' AND value IN ('rejected','changes_requested')`); err != nil {
		return fmt.Errorf("normalize plan_feedback rejected/changes_requested→false: %w", err)
	}
	return nil
}

// scanPlanFeedbackRows scans a *sql.Rows cursor into a []PlanFeedback slice.
func scanPlanFeedbackRows(rows *sql.Rows) ([]PlanFeedback, error) {
	var results []PlanFeedback
	for rows.Next() {
		var pf PlanFeedback
		var value sql.NullString
		var createdStr, updatedStr string
		if err := rows.Scan(
			&pf.ID, &pf.PlanID, &pf.Section, &pf.Action,
			&value, &pf.QuestionID, &createdStr, &updatedStr,
		); err != nil {
			return nil, fmt.Errorf("scan plan feedback row: %w", err)
		}
		pf.Value = value.String
		pf.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		pf.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		results = append(results, pf)
	}
	return results, rows.Err()
}
