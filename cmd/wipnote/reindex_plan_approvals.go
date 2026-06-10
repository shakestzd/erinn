package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/plan/planyaml"
)

// reindexPlanApprovals walks every plan YAML in .wipnote/plans/ and replays
// slice approval state from the canonical YAML into plan_feedback rows.
//
// This is the self-healing reindex path for bug-eca8141d: when the per-user
// SQLite cache is rebuilt (e.g. after upgrade or wipnote reindex --full), the
// plan_feedback table is empty. This function repopulates it from the YAML
// ApprovalStatus / Approved fields so the finalize gate works without requiring
// the user to re-approve every slice manually.
//
// Only slice-N sections are replayed; design/outline/meta rows are not created
// here (they are written on demand by the review UI). Existing rows are NOT
// overwritten — StorePlanFeedback uses INSERT OR IGNORE semantics when the row
// already exists via the unique constraint (plan_id, section, action,
// question_id). This keeps the live interactive state authoritative.
//
// Returns (planFiles, rowsReplayed, errors).
func reindexPlanApprovals(database *sql.DB, wipnoteDir string) (int, int, int) {
	pattern := filepath.Join(wipnoteDir, "plans", "*.yaml")
	files, _ := filepath.Glob(pattern)

	var total, replayed, errCount int
	for _, f := range files {
		total++
		plan, err := planyaml.Load(f)
		if err != nil {
			errCount++
			continue
		}
		if plan == nil || plan.Meta.ID == "" {
			continue
		}
		for _, s := range plan.Slices {
			// Determine canonical approval state from YAML fields.
			// ApprovalStatus is set by approve-slice / reject-slice (v2 field).
			// Approved is set by finalize-yaml (legacy bool field).
			var value string
			switch {
			case s.ApprovalStatus == "approved" || s.Approved:
				value = "true"
			case s.ApprovalStatus == "rejected" || s.ApprovalStatus == "changes_requested":
				value = "false"
			default:
				// Pending / unset — skip; don't create a spurious "not approved" row.
				continue
			}

			section := fmt.Sprintf("slice-%d", s.Num)
			replayErr := replayPlanFeedbackRow(database, plan.Meta.ID, section, "approve", value)
			if replayErr == nil {
				replayed++
			} else if replayErr != errRowAlreadyExists {
				errCount++
			}
			// errRowAlreadyExists: row preserved — not counted as error or replayed.
		}
	}
	return total, replayed, errCount
}

// errRowAlreadyExists is a sentinel returned by replayPlanFeedbackRow when the
// row already exists and was intentionally not overwritten (INSERT OR IGNORE hit
// the unique constraint). Callers treat this as a non-error skip.
var errRowAlreadyExists = fmt.Errorf("plan_feedback row already exists — skipped")

// replayPlanFeedbackRow inserts a plan_feedback row only if no existing row
// exists for the (plan_id, section, action, question_id) tuple. This preserves
// live interactive state written after the last reindex (the DB row wins over
// the YAML snapshot).
//
// Returns errRowAlreadyExists when the row was skipped (not an error condition).
func replayPlanFeedbackRow(db *sql.DB, planID, section, action, value string) error {
	// Use INSERT OR IGNORE to avoid overwriting live rows. The UNIQUE constraint
	// is (plan_id, section, action, question_id); question_id is '' for slice approvals.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`
		INSERT OR IGNORE INTO plan_feedback (plan_id, section, action, value, question_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, '', ?, ?)`,
		planID, section, action, value, now, now,
	)
	if err != nil {
		return fmt.Errorf("replay plan_feedback (plan=%s section=%s): %w", planID, section, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errRowAlreadyExists
	}
	return nil
}
