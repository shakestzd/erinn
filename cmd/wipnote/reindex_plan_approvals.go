package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/plan/planyaml"
)

// reindexPlanFeedback walks every plan YAML in .wipnote/plans/ and replays its
// canonical plan_feedback state into plan_feedback rows, rebuilding the
// ephemeral hydration table from disk. Renamed from reindexPlanApprovals
// (feat-fc3cc9e0): the old name and its `action == 'approve'` allowlist meant
// every OTHER action type recorded in plan.Feedback.Entries —
// set_execution_status, amendment, comment, answer, annotation, chat/*,
// finalize — was invisible to any fresh ephemeral projection no matter what
// canonical YAML held, because nothing replayed it. That is the read-side
// twin of a dead write: the write reaches canonical storage correctly and is
// then discarded on the way back in by a filter nobody remembers to widen
// when a new action type is added. set_execution_status was simply the first
// one anyone noticed (promoteSliceFromYAML's dependency-readiness check and
// its own execution_status verification, both blocked); amendment rows
// written via storePlanFeedback (plan_interview.go, api_plans.go) are the
// same shape and were equally invisible to any reader that goes through this
// table.
//
// Two passes:
//
//  1. replayFeedbackEntries replays EVERY plan.Feedback.Entries row
//     unconditionally — no allowlist. This is the fix: whatever action a
//     future write path invents, it is visible here without anyone having to
//     remember to widen a filter.
//  2. replayLegacyApprovals is a narrow, explicitly-labelled backward-compat
//     shim for plans that carry slice.ApprovalStatus/Approved WITHOUT a
//     corresponding "approve" entry in Feedback.Entries — e.g. hand-authored
//     YAML, or plans predating storePlanFeedbackEntry recording an entry for
//     every mutation. This reproduces reindexPlanApprovals' original
//     behaviour for that shape; see TestReindexPlanFeedback_ReplaysLegacyApprovals
//     and its sibling tests, which construct exactly this case.
//
// Existing rows are NOT overwritten by either pass — replayPlanFeedbackRow
// uses INSERT OR IGNORE semantics via the (plan_id, section, action,
// question_id) unique constraint. This keeps live interactive state
// authoritative over a re-derived snapshot.
//
// Returns (planFiles, rowsReplayed, errors).
func reindexPlanFeedback(database *sql.DB, wipnoteDir string) (int, int, int) {
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

		r, e := replayFeedbackEntries(database, plan)
		replayed += r
		errCount += e

		r, e = replayLegacyApprovals(database, plan)
		replayed += r
		errCount += e
	}
	return total, replayed, errCount
}

// replayFeedbackEntries replays every plan.Feedback.Entries row with no
// filter on Action — see reindexPlanFeedback's doc comment for why an
// allowlist here is the bug this replaces. A malformed entry (missing
// Section or Action) is skipped; there is nothing meaningful to key a row on.
func replayFeedbackEntries(database *sql.DB, plan *planyaml.PlanYAML) (replayed, errCount int) {
	if plan.Feedback == nil {
		return 0, 0
	}
	for _, e := range plan.Feedback.Entries {
		if e.Section == "" || e.Action == "" {
			continue
		}
		replayErr := replayPlanFeedbackRow(database, plan.Meta.ID, e.Section, e.Action, e.Value, e.QuestionID)
		switch {
		case replayErr == nil:
			replayed++
		case replayErr != errRowAlreadyExists:
			errCount++
		}
		// errRowAlreadyExists: row preserved — not counted as error or replayed.
	}
	return replayed, errCount
}

// replayLegacyApprovals synthesizes an "approve" row from
// slice.ApprovalStatus / slice.Approved for plans whose approval state was
// never recorded as a plan.Feedback.Entries row in the first place (see
// reindexPlanFeedback's doc comment). replayFeedbackEntries already replayed
// every entry that DOES exist; INSERT OR IGNORE makes this pass a no-op for
// any slice it already covered, so the two passes never race on the same row.
func replayLegacyApprovals(database *sql.DB, plan *planyaml.PlanYAML) (replayed, errCount int) {
	for _, s := range plan.Slices {
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
		replayErr := replayPlanFeedbackRow(database, plan.Meta.ID, section, "approve", value, "")
		switch {
		case replayErr == nil:
			replayed++
		case replayErr != errRowAlreadyExists:
			errCount++
		}
	}
	return replayed, errCount
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
// questionID is threaded through (not hardcoded to '') because
// replayFeedbackEntries replays real entries now, including "answer" actions
// that share a (plan_id, section, action) prefix across multiple questions —
// e.g. every global question lives under section="questions" — and only
// question_id distinguishes them under the table's unique constraint.
// replayLegacyApprovals, which has no question concept, passes "".
//
// Returns errRowAlreadyExists when the row was skipped (not an error condition).
func replayPlanFeedbackRow(db *sql.DB, planID, section, action, value, questionID string) error {
	// Use INSERT OR IGNORE to avoid overwriting live rows. The UNIQUE constraint
	// is (plan_id, section, action, question_id).
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`
		INSERT OR IGNORE INTO plan_feedback (plan_id, section, action, value, question_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		planID, section, action, value, questionID, now, now,
	)
	if err != nil {
		return fmt.Errorf("replay plan_feedback (plan=%s section=%s action=%s): %w", planID, section, action, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errRowAlreadyExists
	}
	return nil
}
