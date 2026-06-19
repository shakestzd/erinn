package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// PropagateFamilyAttribution forward-propagates work-item attribution across the
// members of a session family. Claude Code (and the other harnesses) split a
// logical session: the SessionStart hook fires on a short-lived parent stub that
// receives the active_feature_id, while the real long-running child session IDs
// never get a SessionStart the hook observes and therefore carry no attribution.
// Both share the same session_family_id, so once a child row exists we can copy
// the family's work item onto every member that still lacks one.
//
// The donor is any member with a resolvable work item (active_work_items first,
// then the legacy active_feature_id column). Recipients are members whose
// attribution is empty — existing attributions are never overwritten, so a
// member already pinned to a different item keeps it. Returns the number of
// session rows updated. Best-effort: a missing family or absent donor is a no-op.
func PropagateFamilyAttribution(db *sql.DB, familyID string) (int, error) {
	if db == nil || strings.TrimSpace(familyID) == "" {
		return 0, nil
	}
	members, err := GetSessionsByFamily(db, familyID)
	if err != nil {
		return 0, err
	}
	if len(members) < 2 {
		// A family of one has no sibling to donate to or from.
		return 0, nil
	}

	donor := ""
	for _, sid := range members {
		if wi := GetActiveWorkItemWithFallback(db, sid, AgentRootSentinel); wi != "" {
			donor = wi
			break
		}
	}
	if donor == "" {
		return 0, nil
	}

	updated := 0
	now := time.Now().UTC().Format(time.RFC3339)
	for _, sid := range members {
		if GetActiveWorkItemWithFallback(db, sid, AgentRootSentinel) != "" {
			continue // already attributed — never clobber.
		}
		if _, err := db.Exec(
			`UPDATE sessions SET active_feature_id = ?, updated_at = ? WHERE session_id = ?`,
			donor, now, sid,
		); err != nil {
			return updated, fmt.Errorf("propagate family attribution to %s: %w", sid, err)
		}
		updated++
	}
	return updated, nil
}
